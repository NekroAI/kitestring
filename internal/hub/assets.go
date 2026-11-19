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
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// 资源库（R-351）第 2 步：数据与推送。
//
// 推送分三步，都由 daemon 发起（CLI / MCP 只把本机路径交给 daemon，见 internal/daemon/assets.go）：
//  1. POST /api/agent/assets/have：给出 sha256 列表，返回 Hub 已有的附件，这些不再重传（按 sha256 去重）；
//  2. POST /api/agent/assets/files：逐个传缺的文件（multipart 字段 file）；GLB 按设置里单独的上限，图片按图片上限；
//  3. POST /api/agent/assets/push：提交这一批（文件写成附件 ID），按 push_id 幂等。
//
// 文件和其它附件一样放在附件目录（R-353，可指定到外置硬盘）；目录不可用时三步都返回 503 和原因，不收。
// 网页（第 3 步）读 GET /api/assets、GET /api/assets/{ref}；文件本身走 /api/files/{id}。

const glbMime = "model/gltf-binary"

func (u UploadLimits) glbBytes() int64 {
	v := u.GLBMB
	if v <= 0 {
		v = defaultGLBMB
	}
	return int64(min(v, maxUploadMB)) << 20
}

// assetFileMime 认资源库收的文件：GLB（文件头 glTF）或图片；别的返回空。
func assetFileMime(head []byte) string {
	if len(head) >= 4 && bytes.Equal(head[:4], []byte("glTF")) {
		return glbMime
	}
	if m := sniff(head); protocol.IsImage(m) {
		return m
	}
	return ""
}

// storageGate 检查附件目录（R-353），不可用时返回 503 和原因。
func (h *Hub) storageGate() error {
	if err := h.checkFiles(); err != nil {
		return huma.Error503ServiceUnavailable("资源库暂不收素材：" + err.Error())
	}
	return nil
}

type assetFileOut struct {
	store.FileRef
	Reused bool `json:"reused" doc:"Hub 已有同样内容的文件，没有另存一份"`
}

// handleAssetFile 接收资源库的一个文件（multipart 字段 file）。同样内容（sha256 与类型都相同）的已有附件直接返回，不另存。
func (h *Hub) handleAssetFile(w http.ResponseWriter, r *http.Request) {
	if err := h.checkFiles(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "资源库暂不收素材："+err.Error())
		return
	}
	set, err := h.Settings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	maxImage, _, _ := set.Upload.bytes()
	maxGLB := set.Upload.glbBytes()
	tooBig := func(mime string) string {
		if mime == glbMime {
			return fmt.Sprintf("GLB 模型不能超过 %d MB（可在设置里调整）", maxGLB>>20)
		}
		return fmt.Sprintf("图片不能超过 %d MB（可在设置里调整）", maxImage>>20)
	}
	r.Body = http.MaxBytesReader(w, r.Body, max(maxImage, maxGLB)+1<<20)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("文件太大：GLB 模型不能超过 %d MB、图片 %d MB（可在设置里调整）", maxGLB>>20, maxImage>>20))
			return
		}
		writeErr(w, http.StatusBadRequest, "没有收到文件")
		return
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	typ := assetFileMime(head)
	if typ == "" {
		writeErr(w, http.StatusBadRequest, "资源库只收 GLB 模型和图片（PNG、JPEG、WebP、GIF）："+filepath.Base(hdr.Filename))
		return
	}
	limit := maxImage
	if typ == glbMime {
		limit = maxGLB
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
		writeErr(w, http.StatusBadRequest, "读取文件失败")
		return
	}
	if size > limit {
		writeErr(w, http.StatusRequestEntityTooLarge, filepath.Base(hdr.Filename)+"："+tooBig(typ))
		return
	}
	sha := hex.EncodeToString(sum.Sum(nil))
	w.Header().Set("Content-Type", "application/json")
	if old, err := h.store.FileBySHA(r.Context(), sha, typ); err == nil && fileExists(h.readPath(old.ID)) {
		_ = json.NewEncoder(w).Encode(assetFileOut{FileRef: old, Reused: true})
		return
	}
	name := filepath.Base(hdr.Filename)
	if name == "." || name == "/" || name == "" {
		name = "asset" + protocol.MediaExt(typ)
	}
	d, _ := deviceFrom(r.Context())
	ref, err := h.store.CreateFile(r.Context(), name, typ, sha, size, "device:"+d.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(tmp.Name(), h.filePath(ref.ID)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(assetFileOut{FileRef: ref})
}

type assetHaveIn struct {
	Body struct {
		SHA256 []string `json:"sha256" maxItems:"2000" doc:"要查的文件 sha256"`
	}
}

type assetHaveOut struct {
	Body struct {
		Files map[string]store.FileRef `json:"files" doc:"Hub 已有的文件：sha256 → 附件"`
	}
}

type assetPushIn struct {
	Body protocol.AssetPush
}

type assetPushOut struct {
	Body protocol.AssetPushResult
}

// AssetListView 是资源库列表。
type AssetListView struct {
	Assets   []store.Asset     `json:"assets"`
	Facets   store.AssetFacets `json:"facets" doc:"这个项目全部素材的批次、类别和各状态数量（不受筛选影响）"`
	Statuses []string          `json:"statuses" doc:"素材状态全集，按展示顺序"`
}

type assetListIn struct {
	Project    string `query:"project" doc:"项目 ID；不写列出全部项目"`
	Batch      string `query:"batch"`
	Category   string `query:"category"`
	Status     string `query:"status" enum:"wip,pending,approved,rework,"`
	Kind       string `query:"kind" enum:"part,image,"`
	ID         string `query:"id" doc:"R-376：素材 id（如 core:weapon.carbine）或编号 A-x；末尾带 * 按前缀查（如 core:weapon.*）"`
	SessionKey string `query:"session_key" doc:"Agent 用：不写 project 时列会话所在项目"`
}

type assetListOut struct{ Body AssetListView }

type assetGetOut struct{ Body store.AssetDetail }

type agentAssetGetIn struct {
	Ref        string `path:"ref" doc:"编号 A-x、素材 ID 或素材 id（如 core:weapon.carbine）"`
	Project    string `query:"project" doc:"项目 ID：按素材 id 找时限定项目"`
	SessionKey string `query:"session_key" doc:"不写 project 时按会话所在项目找"`
}

func (h *Hub) listAssets(ctx context.Context, in *assetListIn, project string) (*assetListOut, error) {
	out := &assetListOut{}
	var err error
	out.Body.Assets, err = h.store.ListAssets(ctx, store.AssetFilter{ProjectID: project, Batch: in.Batch, Category: in.Category, Status: in.Status, Kind: in.Kind, ID: in.ID})
	if err != nil {
		return nil, err
	}
	if out.Body.Assets == nil {
		out.Body.Assets = []store.Asset{}
	}
	if out.Body.Facets, err = h.store.ListAssetFacets(ctx, project); err != nil {
		return nil, err
	}
	out.Body.Statuses = store.AssetStatuses
	return out, nil
}

func (h *Hub) registerAssetsWeb(api huma.API) {
	tag := []string{"assets"}
	huma.Register(api, huma.Operation{OperationID: "list-assets", Method: http.MethodGet, Path: "/api/assets", Tags: tag, Summary: "资源库素材列表（带最新版本），可按项目、批次、类别、状态、类型筛选"},
		func(ctx context.Context, in *assetListIn) (*assetListOut, error) {
			return h.listAssets(ctx, in, in.Project)
		})
	huma.Register(api, huma.Operation{OperationID: "get-asset", Method: http.MethodGet, Path: "/api/assets/{ref}", Tags: tag, Summary: "一条素材和它的全部版本（新的在前）"},
		func(ctx context.Context, in *refPath) (*assetGetOut, error) {
			d, err := h.store.GetAsset(ctx, in.Ref)
			return &assetGetOut{Body: d}, apiErr(err)
		})
}

func (h *Hub) registerAssetsAgent(api huma.API) {
	tag := []string{"agent"}
	huma.Register(api, huma.Operation{OperationID: "agent-asset-have", Method: http.MethodPost, Path: "/api/agent/assets/have", Tags: tag, Summary: "按 sha256 查 Hub 已有的文件（推送前去重）"},
		func(ctx context.Context, in *assetHaveIn) (*assetHaveOut, error) {
			if err := h.storageGate(); err != nil {
				return nil, err
			}
			out := &assetHaveOut{}
			out.Body.Files = map[string]store.FileRef{}
			for _, sha := range in.Body.SHA256 {
				if f, err := h.store.FileBySHA(ctx, sha, ""); err == nil && fileExists(h.readPath(f.ID)) {
					out.Body.Files[sha] = f
				}
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-asset-push", Method: http.MethodPost, Path: "/api/agent/assets/push", Tags: tag, Summary: "提交一批素材（文件先传好）；同一 push_id 重复提交返回第一次的结果"},
		func(ctx context.Context, in *assetPushIn) (*assetPushOut, error) {
			if err := h.storageGate(); err != nil {
				return nil, err
			}
			res, err := h.pushAssets(ctx, in.Body)
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("assets")
			return &assetPushOut{Body: res}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-list-assets", Method: http.MethodGet, Path: "/api/agent/assets", Tags: tag, Summary: "资源库素材列表（默认会话所在项目）"},
		func(ctx context.Context, in *assetListIn) (*assetListOut, error) {
			project := in.Project
			if project == "" && in.SessionKey != "" {
				if x, err := h.store.GetSession(ctx, in.SessionKey); err == nil {
					project = x.ProjectID
				}
			}
			return h.listAssets(ctx, in, project)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-get-asset", Method: http.MethodGet, Path: "/api/agent/assets/{ref}", Tags: tag, Summary: "一条素材和它的全部版本；ref 是编号 A-x 或素材 id（按 id 时默认在会话所在项目里找）"},
		func(ctx context.Context, in *agentAssetGetIn) (*assetGetOut, error) {
			project := in.Project
			if project == "" && in.SessionKey != "" {
				if x, err := h.store.GetSession(ctx, in.SessionKey); err == nil {
					project = x.ProjectID
				}
			}
			d, err := h.store.FindAsset(ctx, project, in.Ref)
			if errors.Is(err, store.ErrAssetAmbiguous) {
				return nil, huma.Error409Conflict(err.Error() + "（--project）")
			}
			return &assetGetOut{Body: d}, apiErr(err)
		})
}

// pushAssets 校验一批素材（项目、需求、文件类型）后入库。
func (h *Hub) pushAssets(ctx context.Context, p protocol.AssetPush) (protocol.AssetPushResult, error) {
	var res protocol.AssetPushResult
	batch := strings.TrimSpace(p.Batch)
	if p.ByID {
		batch = "" // 按 asset.json 推送：批次写在每条素材的 batches 里
	} else if batch == "" {
		return res, errors.New("要写批次（batch），例如 G11e1")
	}
	if len(p.Items) == 0 {
		return res, errors.New("这一批没有素材")
	}
	var proj store.Project
	if q := strings.TrimSpace(p.Project); q != "" {
		var err error
		if proj, err = h.targetProject(ctx, q); err != nil {
			return res, huma.Error400BadRequest(strings.Replace(err.Error(), "未记下需求", "未推送素材", 1))
		}
	} else {
		x, err := h.store.GetSession(ctx, p.SessionKey)
		if err != nil || x.ProjectID == "" {
			return res, errors.New("无法确定项目：会话不属于任何项目，用 --project 指定")
		}
		if proj, err = h.store.GetProject(ctx, x.ProjectID); err != nil {
			return res, err
		}
	}
	reqID := ""
	if ref := strings.TrimSpace(p.Requirement); ref != "" {
		r, err := h.store.GetRequirement(ctx, ref)
		if err != nil {
			return res, errors.New("关联需求不存在：" + ref)
		}
		reqID = r.ID
	}
	seen := map[string]bool{}
	for i, it := range p.Items {
		it.Key = strings.TrimSpace(it.Key)
		if it.Key == "" {
			return res, fmt.Errorf("第 %d 条素材没有 key", i+1)
		}
		if seen[it.Key] {
			return res, fmt.Errorf("素材 %s 在这一批里出现了两次", it.Key)
		}
		seen[it.Key] = true
		if p.ByID {
			if msg := protocol.CheckAssetID(it.Key); msg != "" {
				return res, fmt.Errorf("素材 id %s %s", it.Key, msg)
			}
			if strings.TrimSpace(it.Category) == "" {
				return res, fmt.Errorf("素材 %s 没写类别（category）", it.Key)
			}
			if len(it.Variants) > 0 && !json.Valid(it.Variants) {
				it.Variants = nil
			}
		} else if len(it.Batches) > 0 || it.Source != "" || it.License != "" || len(it.Variants) > 0 {
			return res, fmt.Errorf("素材 %s：batches / source / license / variants 只用于按 asset.json 推送", it.Key)
		}
		if it.File == nil || it.File.ID == "" {
			return res, fmt.Errorf("素材 %s 没有主文件", it.Key)
		}
		f, err := h.store.GetFile(ctx, it.File.ID)
		if err != nil {
			return res, fmt.Errorf("素材 %s 的文件不存在：%s", it.Key, it.File.ID)
		}
		switch it.Kind {
		case protocol.AssetPart:
			if f.Mime != glbMime {
				return res, fmt.Errorf("零件 %s 的主文件要是 GLB：%s", it.Key, f.Name)
			}
		case protocol.AssetImage:
			if !protocol.IsImage(f.Mime) {
				return res, fmt.Errorf("图 %s 的主文件要是图片：%s", it.Key, f.Name)
			}
			if len(it.Previews) > 0 {
				return res, fmt.Errorf("独立的图 %s 不挂预览图", it.Key)
			}
		default:
			return res, fmt.Errorf("素材 %s 的类型只能是 part（GLB 零件）或 image（独立的图）", it.Key)
		}
		for _, pv := range it.Previews {
			pf, err := h.store.GetFile(ctx, pv.ID)
			if err != nil || !protocol.IsImage(pf.Mime) {
				return res, fmt.Errorf("零件 %s 的预览图不存在或不是图片：%s", it.Key, pv.Name)
			}
		}
		if len(it.Meta) > 16<<10 {
			it.Meta = nil // manifest 原文过长时不存，面数、尺寸等已单独记下
		}
		if len(it.Meta) > 0 && !json.Valid(it.Meta) {
			it.Meta = nil
		}
		p.Items[i] = it
	}
	// 提交者：Agent 推送一律是会话（键 + 当时的会话标题）；网页上传（user）和分享页访客（guest，R-365）以后接这里
	sub := store.AssetSubmitter{Kind: store.SubmitterSession, Ref: p.SessionKey}
	if x, err := h.store.GetSession(ctx, p.SessionKey); err == nil {
		sub.Name = x.Title
	}
	out, err := h.store.PushAssets(ctx, store.NewAssetPush{PushID: p.PushID, ProjectID: proj.ID, ProjectName: proj.Name, Submitter: sub,
		RequirementID: reqID, Batch: batch, Branch: p.Branch, Commit: p.Commit, Status: p.Status, Items: p.Items, ByID: p.ByID})
	if err == nil {
		h.broker.publish("assets") // 资源库页（R-351 第 3 步）收到后重新拉列表
		h.refreshReviewTargets(ctx, out)
	}
	return out, err
}
