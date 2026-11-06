package hub

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/proto"
	"github.com/NekroAI/kitestring/internal/store"
)

// 可交互原型（R-197）。
//
// 交付：Agent 把目录 / 单个 .html / .zip 打成 zip，传到 POST /api/agent/prototypes，hub 校验（大小、文件数、入口）
// 后把 zip 原样存成附件，另记一行原型（版本、入口、封面、访问令牌）。证据、决策里存的是原型快照。
//
// 隔离（D-1341、D-1344）：原型不新开端口或域名，走单独路径 /p/<令牌>/…，令牌每个版本一个、随机长串，不认登录 cookie。
// 这条路径的每个响应（含 404）都带 Content-Security-Policy: sandbox allow-scripts allow-forms——不论嵌在框里还是
// 在新标签直接打开，页面都是「无来源」：读不到纸鸢的 cookie、本机存储和页面；它发出的请求按跨站处理，
// SameSite=Lax 的登录 cookie 不会带上，/api 一律未登录；requireWeb 另外拒绝 Origin: null 的请求。
// 不给 allow-same-origin、allow-popups、allow-top-navigation。外部脚本 / 样式来源不限（D-1342 用户选定），
// 外部链接点了不跳转，由注入的小脚本提示。
//
// 访客：访客页拿到的原型地址是 /p/g/<链接 ID>/<原型 ID>/<签名>/…，签名对「链接 ID + 原型 ID + 当前密码哈希」，
// 每次请求都查链接和分享是否还有效、原型所属需求在不在分享里：撤回访客链接即失效，改密码后旧地址失效。

// protoCSP 是原型路径上每个响应都带的安全策略。frame-ancestors 只许纸鸢自己的页面嵌它。
const protoCSP = "sandbox allow-scripts allow-forms; frame-ancestors 'self'"

func protoHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", protoCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// 原型页面是无来源的，它引用自己包里的样式、脚本（含 type=module）、数据都算跨源：放开读取（只限原型路径，内容本来就凭令牌可读）
	h.Set("Access-Control-Allow-Origin", "*")
}

func protoErr(w http.ResponseWriter, code int, msg string) {
	protoHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, msg)
}

// protoMaxBytes 是原型包（zip）的大小上限，设置里「上传」可调，默认 20 MB。
func (u UploadLimits) protoBytes() int64 {
	v := u.PrototypeMB
	if v <= 0 {
		v = proto.DefaultMaxMB
	}
	return int64(min(v, maxUploadMB)) << 20
}

type protoUploadOut struct {
	store.Prototype
	External []string `json:"external,omitempty" doc:"页面里引用的外部地址（只提示，不拦）"`
}

// handleProtoUpload 接收原型包：multipart 字段 file（zip）、title、entry、cover（封面附件 ID）、requirement、version_of、session_key。
func (h *Hub) handleProtoUpload(w http.ResponseWriter, r *http.Request) {
	if h.filesDir == "" {
		writeErr(w, http.StatusServiceUnavailable, "未配置附件目录")
		return
	}
	set, err := h.Settings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	limit := set.Upload.protoBytes()
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	f, _, err := r.FormFile("file")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("原型包不能超过 %d MB（可在设置里调整）", limit>>20))
			return
		}
		writeErr(w, http.StatusBadRequest, "没有收到原型包")
		return
	}
	defer f.Close()
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		writeErr(w, http.StatusBadRequest, "原型要有标题（title）")
		return
	}
	if rs := []rune(title); len(rs) > 60 {
		title = string(rs[:60])
	}
	if err := os.MkdirAll(h.filesDir, 0o700); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tmp, err := os.CreateTemp(h.filesDir, "upload-*")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	sum := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, sum), f)
	if err != nil {
		tmp.Close()
		writeErr(w, http.StatusBadRequest, "读取原型包失败")
		return
	}
	if size > limit {
		tmp.Close()
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("原型包不能超过 %d MB（可在设置里调整）", limit>>20))
		return
	}
	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		tmp.Close()
		writeErr(w, http.StatusBadRequest, "原型包不是有效的 zip")
		return
	}
	info, err := proto.Validate(zr, r.FormValue("entry"))
	tmp.Close()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	reqID := ""
	if ref := strings.TrimSpace(r.FormValue("requirement")); ref != "" && !strings.EqualFold(ref, "none") {
		req, err := h.store.GetRequirement(ctx, ref)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "关联需求不存在："+ref)
			return
		}
		reqID = req.ID
	}
	cover := strings.TrimSpace(r.FormValue("cover"))
	if cover != "" {
		if cf, err := h.store.GetFile(ctx, cover); err != nil || !strings.HasPrefix(cf.Mime, "image/") {
			cover = ""
		}
	}
	d, _ := deviceFrom(ctx)
	ref, err := h.store.CreateFile(ctx, safeZipName(title), "application/zip", hex.EncodeToString(sum.Sum(nil)), size, "device:"+d.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(tmp.Name(), h.filePath(ref.ID)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	p, err := h.store.CreatePrototype(ctx, store.NewPrototype{RequirementID: reqID, Title: title, Entry: info.Entry, FileID: ref.ID, CoverID: cover,
		Files: info.Files, Size: size, SessionKey: r.FormValue("session_key"), VersionOf: strings.TrimSpace(r.FormValue("version_of"))})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(protoUploadOut{Prototype: p, External: info.External})
}

func safeZipName(title string) string {
	n := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return '-'
		}
		return r
	}, title)
	return n + ".zip"
}

// handleProto 是原型路径 /p/<令牌>/<路径>：不认登录，令牌就是凭据。
func (h *Hub) handleProto(w http.ResponseWriter, r *http.Request) {
	p, err := h.store.PrototypeByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		protoErr(w, http.StatusNotFound, "原型不存在或已删除")
		return
	}
	h.serveProto(w, r, p, r.PathValue("path"), store.ProtoURL(p.Token, ""))
}

// handleGuestProto 是访客的原型路径 /p/g/<链接 ID>/<原型 ID>/<签名>/<路径>。
func (h *Hub) handleGuestProto(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	linkID, protoID, sig := r.PathValue("link"), r.PathValue("proto"), r.PathValue("sig")
	l, err := h.store.ShareLinkByID(ctx, linkID)
	if err != nil {
		protoErr(w, http.StatusNotFound, shareLinkInvalid)
		return
	}
	x, l, err := h.guestLink(ctx, l.Token)
	if err != nil {
		protoErr(w, http.StatusGone, shareLinkInvalid)
		return
	}
	want, err := h.guestProtoSig(ctx, x, l, protoID)
	if err != nil || !hmac.Equal([]byte(sig), []byte(want)) {
		protoErr(w, http.StatusNotFound, shareNotIncluded)
		return
	}
	p, err := h.store.GetPrototype(ctx, protoID)
	if err != nil || p.RequirementID == "" {
		protoErr(w, http.StatusNotFound, shareNotIncluded)
		return
	}
	sc, err := h.store.ResolveShare(ctx, x.Items)
	if err != nil || !sc.Reqs[p.RequirementID] {
		protoErr(w, http.StatusNotFound, shareNotIncluded)
		return
	}
	h.serveProto(w, r, p, r.PathValue("path"), guestProtoBase(l.ID, p.ID, sig))
}

func guestProtoBase(linkID, protoID, sig string) string {
	return "/p/g/" + linkID + "/" + protoID + "/" + sig + "/"
}

// guestProtoSig：对「链接 ID + 原型 ID + 当前密码哈希」的签名，改密码后旧地址失效。
func (h *Hub) guestProtoSig(ctx context.Context, x store.Share, l store.ShareLink, protoID string) (string, error) {
	key, err := h.guestKey(ctx)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("proto\x00" + l.ID + "\x00" + protoID + "\x00" + x.PasswordHash))
	return hex.EncodeToString(m.Sum(nil))[:32], nil
}

// serveProto 从 zip 里按路径读文件返回。base 是这个原型的根地址（以 / 结尾），空路径时跳到入口或下载整包。
func (h *Hub) serveProto(w http.ResponseWriter, r *http.Request, p store.PrototypeRow, rel, base string) {
	protoHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		protoErr(w, http.StatusMethodNotAllowed, "原型只能读")
		return
	}
	zipPath := h.filePath(p.FileID)
	if rel == "" && r.URL.Query().Has("download") {
		f, err := os.Open(zipPath)
		if err != nil {
			protoErr(w, http.StatusNotFound, "原型文件丢失")
			return
		}
		defer f.Close()
		name := safeZipName(fmt.Sprintf("%s-v%d", p.Title, p.Version))
		w.Header().Set("Content-Type", "application/zip")
		if cd := mime.FormatMediaType("attachment", map[string]string{"filename": name}); cd != "" {
			w.Header().Set("Content-Disposition", cd)
		}
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.ServeContent(w, r, "", p.CreatedAt, f)
		return
	}
	if rel == "" {
		segs := strings.Split(p.Entry, "/")
		for i, s := range segs {
			segs[i] = urlPathEscape(s)
		}
		http.Redirect(w, r, base+strings.Join(segs, "/"), http.StatusFound)
		return
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		protoErr(w, http.StatusNotFound, "原型文件丢失")
		return
	}
	defer zr.Close()
	f := proto.Lookup(&zr.Reader, rel)
	if f == nil {
		protoErr(w, http.StatusNotFound, "原型里没有这个文件："+rel)
		return
	}
	rc, err := f.Open()
	if err != nil {
		protoErr(w, http.StatusInternalServerError, "读不出原型里的文件")
		return
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, int64(proto.MaxUnpacked)))
	if err != nil {
		protoErr(w, http.StatusInternalServerError, "读不出原型里的文件")
		return
	}
	ctype := protoMime(f.Name, b)
	if proto.IsHTML(f.Name) {
		b = proto.Inject(b)
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "", p.CreatedAt, bytes.NewReader(b))
}

func urlPathEscape(s string) string { return url.PathEscape(s) }

// protoRel 取原型地址里根地址之后的部分（/p/<令牌>/<这部分>）。
func protoRel(u string) string {
	rest := strings.TrimPrefix(u, "/p/")
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[i+1:]
	}
	return ""
}

// protoMime：原型里的文件按扩展名定类型（常用的写死），认不出的按内容猜。
func protoMime(name string, b []byte) string {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".ttf":
		return "font/ttf"
	case ".wasm":
		return "application/wasm"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".ico":
		return "image/x-icon"
	}
	if t := extMime[ext]; t != "" {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return http.DetectContentType(b)
}

// protoResolver 把原型 ID 解析成快照（要决策的点里选项的原型）。
func (h *Hub) protoResolver(ctx context.Context) blocks.ProtoResolver {
	return func(id string) (blocks.Prototype, bool) {
		p, err := h.store.PrototypeSnapshot(ctx, strings.TrimSpace(id))
		return p, err == nil
	}
}

// guestProtos 把访客看得到的原型地址换成访客地址（不暴露令牌）。
func (g *guestCtx) protos(ps []store.Prototype) {
	for i := range ps {
		g.proto(&ps[i])
	}
}

func (g *guestCtx) proto(p *store.Prototype) {
	if g.protoBase == nil {
		p.URL, p.DownloadURL = "", ""
		return
	}
	base := g.protoBase(p.ID)
	p.URL, p.DownloadURL = base+protoRel(p.URL), base+"?download"
	for i := range p.Versions {
		v := &p.Versions[i]
		vb := g.protoBase(v.ID)
		v.URL, v.DownloadURL = vb+protoRel(v.URL), vb+"?download"
	}
}

// guestProtoBaser 返回给访客算原型根地址的函数。
func (h *Hub) guestProtoBaser(ctx context.Context, x store.Share, l store.ShareLink) func(string) string {
	return func(id string) string {
		sig, err := h.guestProtoSig(ctx, x, l, id)
		if err != nil {
			return ""
		}
		return guestProtoBase(l.ID, id, sig)
	}
}
