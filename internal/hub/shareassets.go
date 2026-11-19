package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// 资源库接入分享（R-365，D-1785）。
//
// 范围：分享可以挂资源库的批次、类别（可「包含以后新增的」素材）和单个素材（可「包含以后新增的」版本；没开时只给到挂上时的那一版）。
// 访客侧：GET /api/share/<token>/assets、/assets/<ref> 看图片墙、大图、3D、版本对比；文件仍走 /api/share/<token>/files/<id>，
// 只放行范围里那几版的主文件、预览图和附件。下载原文件按分享的开关，默认关：关时访客页不显示下载按钮，
// 带 download 的请求、直接打开文件地址（导航）一律拒绝；3D 查看器取 GLB 照常放行（GLB 取到浏览器里技术上仍能存下，所以不称「防下载」）。
// 每条链接一档权限：只看 / 可留言 / 可上传。访客对素材某一版的留言、上传的文件都先进待收队列（share_comments），
// 访客写不了素材、审核和别的表；审核（通过 / 返工）只有你能做。
// 所有者侧：留言放入时由你选「作为返工意见」（改返工）或「只送意见」（不改状态），以访客为作者写进审核记录、送提交素材的会话；
// 上传收下为新版本（类型要和素材一致，回到待审）或附件，都标明访客名字。访客的话写明来源、只作参考，不当指令送给会话。

const (
	guestUploadMax    = 20 // 一条链接 10 分钟内最多上传的文件个数
	fbxMime           = "application/vnd.autodesk.fbx"
	guestPushDedup    = time.Minute
	guestPushTextRune = 60
)

// guestUploadMime 认访客能上传的文件：GLB、图片、录屏（MP4 / WebM / MOV）、FBX 动画；别的返回空。
func guestUploadMime(head []byte) string {
	if m := assetFileMime(head); m != "" {
		return m
	}
	if m := sniff(head); protocol.IsVideo(m) {
		return m
	}
	if bytes.HasPrefix(head, []byte("Kaydara FBX Binary")) {
		return fbxMime
	}
	return ""
}

// ---------- 访客看到的素材 ----------

// guestAsset 把素材裁成访客能看的：只留范围里的版本（最新的那一版当作最新版本），去掉会话键、分支、提交、manifest 原文、审核记录。
func (g *guestCtx) guestAsset(d store.AssetDetail) (store.AssetDetail, bool) {
	c, ok := g.scope.Assets[d.ID]
	if !ok {
		return d, false
	}
	vs := []store.AssetVersion{}
	for _, v := range d.Versions {
		if c == 0 || v.Version <= c {
			vs = append(vs, guestVersion(v))
		}
	}
	if len(vs) == 0 {
		return d, false
	}
	d.Versions = vs
	d.Latest = &d.Versions[0]
	d.Version = d.Versions[0].Version
	d.Submitter = guestSubmitter(d.Submitter)
	d.RequirementID, d.ProjectID = "", ""
	d.Reviews = []store.AssetReview{}
	atts := []store.AssetAttachment{}
	for _, a := range d.Attachments {
		if c == 0 || a.Version <= c {
			a.Submitter = guestSubmitter(a.Submitter)
			atts = append(atts, a)
		}
	}
	d.Attachments = atts
	return d, true
}

func guestVersion(v store.AssetVersion) store.AssetVersion {
	v.Branch, v.Commit, v.Meta = "", "", nil
	v.Submitter = guestSubmitter(v.Submitter)
	return v
}

// guestSubmitter：会话键、访客链接 ID 不给访客；会话只写「会话」，访客写名字。
func guestSubmitter(s store.AssetSubmitter) store.AssetSubmitter {
	s.Ref = ""
	if s.Kind == store.SubmitterSession {
		s.Name = ""
	}
	return s
}

// GuestAssetsView 是访客页的图片墙。
type GuestAssetsView struct {
	Assets        []store.Asset     `json:"assets" doc:"分享里的素材（最新版本是分享里能看的最新一版）"`
	Facets        store.AssetFacets `json:"facets" doc:"分享里素材的批次、类别和各状态数量"`
	Statuses      []string          `json:"statuses"`
	Perm          string            `json:"perm" enum:"view,comment,upload" doc:"这条链接的权限：view 只看 / comment 可留言 / upload 可上传"`
	AllowDownload bool              `json:"allow_download" doc:"分享允许下载素材原文件"`
}

type guestAssetsOut struct{ Body GuestAssetsView }

type guestAssetOut struct{ Body store.AssetDetail }

// guestAssets 列出分享里的素材（访客能看的样子）。
func (h *Hub) guestAssets(ctx context.Context, g *guestCtx) ([]store.Asset, error) {
	out := []store.Asset{}
	if len(g.scope.Assets) == 0 {
		return out, nil
	}
	all, err := h.store.ListAssets(ctx, store.AssetFilter{})
	if err != nil {
		return nil, err
	}
	for _, a := range all {
		c, ok := g.scope.Assets[a.ID]
		if !ok {
			continue
		}
		if c > 0 && a.Version > c { // 只给到挂上时的那一版：从详情里取那一版
			d, err := h.store.GetAsset(ctx, a.ID)
			if err != nil {
				continue
			}
			if v, ok := g.guestAsset(d); ok {
				out = append(out, v.Asset)
			}
			continue
		}
		v, ok := g.guestAsset(store.AssetDetail{Asset: a, Versions: []store.AssetVersion{*a.Latest}})
		if ok {
			out = append(out, v.Asset)
		}
	}
	return out, nil
}

func guestFacets(list []store.Asset) store.AssetFacets {
	f := store.AssetFacets{Batches: []string{}, Categories: []string{}, Counts: map[string]int{}}
	for _, st := range store.AssetStatuses {
		f.Counts[st] = 0
	}
	for _, a := range list {
		if !slices.Contains(f.Batches, a.Batch) {
			f.Batches = append(f.Batches, a.Batch)
		}
		if a.Category != "" && !slices.Contains(f.Categories, a.Category) {
			f.Categories = append(f.Categories, a.Category)
		}
		f.Counts[a.Status]++
	}
	slices.Sort(f.Batches)
	slices.Sort(f.Categories)
	return f
}

func (h *Hub) registerShareGuestAssets(api huma.API) {
	tag := []string{"share-guest"}
	huma.Register(api, huma.Operation{OperationID: "guest-assets", Method: http.MethodGet, Path: "/api/share/{token}/assets", Tags: tag, Summary: "访客：分享里的素材（图片墙）"},
		func(ctx context.Context, in *guestIn) (*guestAssetsOut, error) {
			g, err := h.guestAuth(ctx, in.Token, in.Cookie)
			if err != nil {
				return nil, err
			}
			list, err := h.guestAssets(ctx, g)
			if err != nil {
				return nil, err
			}
			return &guestAssetsOut{Body: GuestAssetsView{Assets: list, Facets: guestFacets(list), Statuses: store.AssetStatuses, Perm: g.link.Perm, AllowDownload: g.share.AllowDownload}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "guest-asset", Method: http.MethodGet, Path: "/api/share/{token}/assets/{ref}", Tags: tag, Summary: "访客：一条素材和分享里它的版本、附件"},
		func(ctx context.Context, in *guestRefIn) (*guestAssetOut, error) {
			g, err := h.guestAuth(ctx, in.Token, in.Cookie)
			if err != nil {
				return nil, err
			}
			d, err := h.store.GetAsset(ctx, in.Ref)
			if err != nil {
				return nil, notIncluded()
			}
			v, ok := g.guestAsset(d)
			if !ok {
				return nil, notIncluded()
			}
			return &guestAssetOut{Body: v}, nil
		})
}

// guestAssetFile 判断附件 id 是不是分享里素材的文件：allowed 能看，orig 是原文件（某一版的主文件或素材的附件，受下载开关管）。
func (h *Hub) guestAssetFile(ctx context.Context, g *guestCtx, id string) (allowed, orig bool, assetID string, err error) {
	uses, err := h.store.AssetFileUses(ctx, id)
	if err != nil {
		return false, false, "", err
	}
	for _, u := range uses {
		if !g.scope.AssetVisible(u.AssetID, u.Version) {
			continue
		}
		allowed = true
		assetID = u.AssetID
		if u.Main || u.Attachment {
			orig = true
		}
	}
	return allowed, orig, assetID, nil
}

// wantsDownload：带 download 的请求，或浏览器直接打开文件地址（导航），都算下载。3D 查看器和图片取文件不是导航。
func wantsDownload(r *http.Request) bool {
	if r.URL.Query().Has("download") {
		return true
	}
	return r.Header.Get("Sec-Fetch-Dest") == "document"
}

// ---------- 访客上传（可上传的链接） ----------

// handleGuestUpload：POST /api/share/<token>/uploads，multipart 字段 file（文件）、asset（素材编号 A-x）、version（看的是第几版）、note（说明）。
// 只有「可上传」的链接能用；文件只进待收队列，你收下后才成为新版本或附件。
func (h *Hub) handleGuestUpload(w http.ResponseWriter, r *http.Request) {
	g := h.guestAuthHTTP(w, r)
	if g == nil {
		return
	}
	if store.PermRank(g.link.Perm) < store.PermRank(store.PermUpload) {
		writeErr(w, http.StatusForbidden, shareUploadNoPerm)
		return
	}
	if err := h.checkFiles(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, shareUploadUnavailable)
		return
	}
	ctx := r.Context()
	if n, _, err := h.store.RecentLinkUploads(ctx, g.link.ID, time.Now().Add(-guestRateWindow)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if n >= guestUploadMax {
		writeErr(w, http.StatusTooManyRequests, shareUploadTooMany)
		return
	}
	set, err := h.Settings(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	maxImage, maxVideo, _ := set.Upload.bytes()
	maxGLB := set.Upload.glbBytes()
	r.Body = http.MaxBytesReader(w, r.Body, max(maxImage, maxVideo, maxGLB)+1<<20)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			writeErr(w, http.StatusRequestEntityTooLarge, shareUploadTooBig(maxGLB>>20, maxImage>>20, maxVideo>>20))
			return
		}
		writeErr(w, http.StatusBadRequest, shareUploadNoFile)
		return
	}
	defer f.Close()
	note := strings.TrimSpace(r.FormValue("note"))
	if utf8.RuneCountInString(note) > store.GuestTextMax {
		writeErr(w, http.StatusBadRequest, shareCommentTooLong(store.GuestTextMax))
		return
	}
	version, _ := strconv.Atoi(r.FormValue("version"))
	ci, err := h.guestTarget(ctx, g, store.GuestOnAsset, r.FormValue("asset"), version)
	if err != nil {
		var se huma.StatusError
		if errors.As(err, &se) {
			writeErr(w, se.GetStatus(), se.Error())
		} else {
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	typ := guestUploadMime(head)
	if typ == "" {
		writeErr(w, http.StatusBadRequest, shareUploadType)
		return
	}
	limit := maxGLB
	switch {
	case protocol.IsImage(typ):
		limit = maxImage
	case protocol.IsVideo(typ):
		limit = maxVideo
	}
	tmp, err := os.CreateTemp(h.filesDir, "upload-*")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	sum := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, sum), io.MultiReader(bytes.NewReader(head), io.LimitReader(f, limit+1)))
	tmp.Close()
	if err != nil {
		writeErr(w, http.StatusBadRequest, shareUploadNoFile)
		return
	}
	if size > limit {
		writeErr(w, http.StatusRequestEntityTooLarge, shareUploadTooBig(maxGLB>>20, maxImage>>20, maxVideo>>20))
		return
	}
	name := filepath.Base(hdr.Filename)
	if name == "." || name == "/" || name == "" {
		name = "upload" + protocol.MediaExt(typ)
	}
	ref, err := h.store.CreateFile(ctx, name, typ, hex.EncodeToString(sum.Sum(nil)), size, "guest:"+g.link.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(tmp.Name(), h.filePath(ref.ID)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ci.ShareID, ci.LinkID, ci.GuestName, ci.Text, ci.FileID = g.share.ID, g.link.ID, g.link.Name, note, ref.ID
	c, err := h.store.CreateShareComment(ctx, ci)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.broker.publish("shares")
	h.notifyGuest(c)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(guestComment(c))
}

// notifyGuest：访客留言、上传时推送给你（R-365）。同一条链接 1 分钟内只推一次，跟随「新决策」的订阅。
func (h *Hub) notifyGuest(c store.ShareComment) {
	dedup := "guest|" + c.LinkID
	h.mu.Lock()
	if t, ok := h.notified[dedup]; ok && time.Since(t) < guestPushDedup {
		h.mu.Unlock()
		return
	}
	h.notified[dedup] = time.Now()
	h.mu.Unlock()
	on := c.RequirementCode
	switch {
	case c.TargetKind == store.GuestOnAsset:
		on = fmt.Sprintf("%s「%s」v%d", c.TargetCode, c.AssetKey, c.AssetVersion)
	case c.TargetKind == store.GuestOnStage:
		on = c.TargetCode
	}
	text := c.Text
	if c.File != nil {
		text = c.File.Name
	}
	if r := []rune(text); len(r) > guestPushTextRune {
		text = string(r[:guestPushTextRune]) + "…"
	}
	title := pushGuestCommentTitle(c.GuestName)
	if c.File != nil {
		title = pushGuestUploadTitle(c.GuestName)
	}
	h.sendNotify(c.ProjectID, notify.Message{Event: notify.EventShareGuest, Title: title, Body: pushGuestBody(on, text), Group: "kitestring"}, "/shares/"+c.ShareID)
}

// ---------- 所有者侧：放入素材上的留言、收下上传 ----------

// placeAssetComment 处理一条访客对素材的留言或上传。as：留言是 rework（作为返工意见）/ note（只送意见）；上传是 version（新版本）/ attachment（附件）。
func (h *Hub) placeAssetComment(ctx context.Context, c store.ShareComment, as, text, addend string) (store.ShareComment, error) {
	upload := c.File != nil
	if upload && as != store.PlacedVersion && as != store.PlacedAttachment {
		return c, huma.Error400BadRequest(shareUploadPickAs)
	}
	if !upload && as != store.AssetRework && as != store.AssetNote {
		return c, huma.Error400BadRequest(shareAssetPickAs)
	}
	d, err := h.store.GetAsset(ctx, c.TargetRef)
	if err != nil {
		return c, apiErr(err)
	}
	if upload && as == store.PlacedVersion {
		ok := (d.Kind == protocol.AssetPart && c.File.Mime == glbMime) || (d.Kind == protocol.AssetImage && protocol.IsImage(c.File.Mime))
		if !ok {
			return c, huma.Error400BadRequest(shareUploadKindMismatch(d.Kind == protocol.AssetPart))
		}
		if err := h.storageGate(); err != nil {
			return c, err
		}
	}
	if err := h.store.ClaimShareComment(ctx, c.ID); err != nil {
		if errors.Is(err, store.ErrGuestHandled) {
			return c, huma.Error409Conflict(err.Error())
		}
		return c, apiErr(err)
	}
	guest := store.AssetSubmitter{Kind: store.SubmitterGuest, Ref: c.LinkID, Name: c.GuestName}
	placedAs, result := as, ""
	switch {
	case !upload:
		_, r, err := h.reviewAsset(ctx, store.NewAssetReview{Asset: d.ID, Verdict: as, Comment: text, Author: guest, Via: c.ShareName, Addend: addend, Version0: c.AssetVersion})
		if err != nil {
			h.store.ReleaseShareComment(ctx, c.ID)
			return c, apiErr(err)
		}
		placedAs, result = store.PlacedReview, r.ID
	case as == store.PlacedVersion:
		res, err := h.store.PushAssets(ctx, store.NewAssetPush{PushID: "guest:" + c.ID, ProjectID: d.ProjectID, Submitter: guest, Batch: d.Batch, Status: store.AssetPending,
			Items: []protocol.AssetItem{{Key: d.Key, Kind: d.Kind, Category: d.Category, Group: d.Group, File: &protocol.AssetFile{ID: c.File.ID, Name: c.File.Name, Size: c.File.Size}}}})
		if err != nil {
			h.store.ReleaseShareComment(ctx, c.ID)
			return c, apiErr(err)
		}
		if len(res.Items) > 0 {
			result = "v" + strconv.Itoa(res.Items[0].Version)
		}
		h.refreshReviewTargets(ctx, res)
	default:
		a, err := h.store.AddAssetAttachment(ctx, d.ID, c.File.ID, c.Text, guest)
		if err != nil {
			h.store.ReleaseShareComment(ctx, c.ID)
			return c, apiErr(err)
		}
		result = a.ID
	}
	if err := h.store.SetShareCommentResult(ctx, c.ID, placedAs, result); err != nil {
		return c, err
	}
	h.broker.publish("assets", "shares")
	return h.store.GetShareComment(ctx, c.ID)
}

// guestAssetSource 是访客意见送给会话时写的来源（和需求上的访客留言同一种写法）。
func guestAssetSource(r store.AssetReview) string {
	return "来自访客「" + r.Author.Name + "」（经分享链接「" + r.Via + "」），由用户放入"
}
