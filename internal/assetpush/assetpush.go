// Package assetpush 把一批素材（manifest.json + 预览图目录）整理成资源库推送（R-351），供 ks asset push 与 MCP asset_push 共用。
//
// manifest 按 rollcall-squad 的格式读（game/assets/<批次>/manifest.json）：顶层 batch、assets；
// 每项 id、file（相对 manifest 所在目录，或相对仓库根）、category、triangles、budget_max / budget、
// 尺寸 bounds_m.size / bounds_local_m.size / dimensions_m（米）、dimensions_px（图）。
// file 是 GLB 的记为零件（part），是图片的记为独立的图（image）。
// 预览图目录里的图：文件名是「零件 id」或「零件 id_后缀」的挂到那个零件上（取最长的匹配）；其余是独立的图，
// id 用文件名去扩展名，类别按文件名猜（catalog → 目录图，concept → 概念图，compar → 对比图），
// 末尾是 _A / _B 这类单个大写字母的归为同一组（概念图的候选并排比较）。
package assetpush

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // 读 GIF 尺寸
	_ "image/jpeg" // 读 JPEG 尺寸
	_ "image/png"  // 读 PNG 尺寸
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// Options 是一次推送的输入。
type Options struct {
	Manifest    string   // manifest.json 路径，可空（只推预览目录里的独立的图）
	Previews    []string // 预览图目录，可多个
	Batch       string   // 批次；不写用 manifest 里的 batch
	Category    string   // 没写类别的素材用它
	Status      string   // wip / pending
	Branch      string   // 不写时从 git 读
	Commit      string
	Project     string
	Requirement string
	SessionKey  string
}

var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true}

var groupRe = regexp.MustCompile(`^(.+)[_-][A-Z]$`)

type entry struct {
	ID         string          `json:"id"`
	File       string          `json:"file"`
	Category   string          `json:"category"`
	Triangles  int             `json:"triangles"`
	BudgetMax  int             `json:"budget_max"`
	Budget     json.RawMessage `json:"budget"`
	Bounds     *bounds         `json:"bounds_m"`
	BoundsLoc  *bounds         `json:"bounds_local_m"`
	Dimensions []float64       `json:"dimensions_m"`
	Pixels     []int           `json:"dimensions_px"`
}

type bounds struct {
	Size []float64 `json:"size"`
}

// Build 读 manifest 和预览目录，返回推送内容（文件写本机绝对路径）和提示。
func Build(o Options) (protocol.AssetPush, []string, error) {
	p := protocol.AssetPush{Batch: strings.TrimSpace(o.Batch), Status: o.Status, Project: o.Project, Requirement: o.Requirement, SessionKey: o.SessionKey, Branch: o.Branch, Commit: o.Commit}
	var notes []string
	base := ""
	if o.Manifest != "" {
		items, batch, err := readManifest(o.Manifest, o.Category)
		if err != nil {
			return p, nil, err
		}
		switch {
		case p.Batch == "":
			p.Batch = batch
		case batch != "" && batch != p.Batch:
			notes = append(notes, fmt.Sprintf("manifest 里的批次是 %s，按 --batch %s 推送", batch, p.Batch))
		}
		p.Items = items
		base = filepath.Dir(o.Manifest)
	}
	if p.Batch == "" {
		return p, nil, errors.New("要写批次：--batch，或在 manifest 里写 batch")
	}
	for _, dir := range o.Previews {
		if base == "" {
			base = dir
		}
		if err := addPreviews(&p, dir, o.Category); err != nil {
			return p, nil, err
		}
	}
	if len(p.Items) == 0 {
		return p, nil, errors.New("没有素材：manifest 里没有 assets，预览目录里也没有图")
	}
	for _, it := range p.Items {
		if it.Kind == protocol.AssetPart && len(it.Previews) == 0 {
			notes = append(notes, "零件 "+it.Key+" 没有预览图")
		}
	}
	if base != "" && (p.Branch == "" || p.Commit == "") {
		b, c := gitHead(base)
		if p.Branch == "" {
			p.Branch = b
		}
		if p.Commit == "" {
			p.Commit = c
		}
	}
	return p, notes, nil
}

func readManifest(path, defCategory string) ([]protocol.AssetItem, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("读不到 manifest：%w", err)
	}
	var m struct {
		Batch  string            `json:"batch"`
		Assets []json.RawMessage `json:"assets"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, "", fmt.Errorf("manifest 不是有效的 JSON：%w", err)
	}
	dir, _ := filepath.Abs(filepath.Dir(path))
	root := gitRoot(dir)
	var items []protocol.AssetItem
	seen := map[string]bool{}
	for i, r := range m.Assets {
		var e entry
		if err := json.Unmarshal(r, &e); err != nil {
			return nil, "", fmt.Errorf("manifest 第 %d 项无法解析：%w", i+1, err)
		}
		e.ID = strings.TrimSpace(e.ID)
		if e.ID == "" {
			return nil, "", fmt.Errorf("manifest 第 %d 项没有 id", i+1)
		}
		if seen[e.ID] {
			return nil, "", fmt.Errorf("manifest 里 id %s 出现了两次", e.ID)
		}
		seen[e.ID] = true
		if e.File == "" {
			return nil, "", fmt.Errorf("素材 %s 没写 file", e.ID)
		}
		file := resolve(dir, root, e.File)
		if file == "" {
			return nil, "", fmt.Errorf("素材 %s 的文件找不到：%s", e.ID, e.File)
		}
		it := protocol.AssetItem{Key: e.ID, Category: e.Category, File: &protocol.AssetFile{Path: file, Name: filepath.Base(file)}, Triangles: e.Triangles, Meta: r}
		if it.Category == "" {
			it.Category = defCategory
		}
		it.Budget = e.BudgetMax
		if it.Budget == 0 && len(e.Budget) > 0 {
			_ = json.Unmarshal(e.Budget, &it.Budget) // 有的批次 budget 是数字，有的是对象，认不出就不记
		}
		switch {
		case e.Bounds != nil && len(e.Bounds.Size) == 3:
			it.Size = e.Bounds.Size
		case e.BoundsLoc != nil && len(e.BoundsLoc.Size) == 3:
			it.Size = e.BoundsLoc.Size
		case len(e.Dimensions) == 3:
			it.Size = e.Dimensions
		}
		ext := strings.ToLower(filepath.Ext(file))
		switch {
		case ext == ".glb":
			it.Kind = protocol.AssetPart
		case imageExt[ext]:
			it.Kind = protocol.AssetImage
			it.Pixels = e.Pixels
			if len(it.Pixels) != 2 {
				it.Pixels = imageSize(file)
			}
		default:
			return nil, "", fmt.Errorf("素材 %s 的文件类型不支持：%s（资源库只收 GLB 和图片）", e.ID, filepath.Base(file))
		}
		items = append(items, it)
	}
	return items, m.Batch, nil
}

// resolve 找 manifest 里写的文件：先相对 manifest 所在目录，再相对仓库根，最后在 manifest 目录里按文件名找。
func resolve(dir, root, f string) string {
	cands := []string{}
	if filepath.IsAbs(f) {
		cands = append(cands, f)
	} else {
		cands = append(cands, filepath.Join(dir, f))
		if root != "" {
			cands = append(cands, filepath.Join(root, f))
		}
	}
	cands = append(cands, filepath.Join(dir, filepath.Base(f)))
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func addPreviews(p *protocol.AssetPush, dir, defCategory string) error {
	des, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("读不到预览图目录：%w", err)
	}
	abs, _ := filepath.Abs(dir)
	var names []string
	for _, de := range des {
		if !de.IsDir() && imageExt[strings.ToLower(filepath.Ext(de.Name()))] && !strings.HasPrefix(de.Name(), ".") {
			names = append(names, de.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(abs, name)
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		best := -1
		for i, it := range p.Items {
			if it.Kind != protocol.AssetPart {
				continue
			}
			if stem == it.Key || strings.HasPrefix(stem, it.Key+"_") || strings.HasPrefix(stem, it.Key+"-") {
				if best < 0 || len(it.Key) > len(p.Items[best].Key) {
					best = i
				}
			}
		}
		if best >= 0 {
			p.Items[best].Previews = append(p.Items[best].Previews, protocol.AssetFile{Path: path, Name: name})
			continue
		}
		dup := false
		for _, it := range p.Items {
			if it.Key == stem {
				dup = true
				break
			}
		}
		if dup {
			continue // manifest 里已有这张图
		}
		it := protocol.AssetItem{Key: stem, Kind: protocol.AssetImage, Category: guessCategory(abs, stem, defCategory), File: &protocol.AssetFile{Path: path, Name: name}, Pixels: imageSize(path)}
		if m := groupRe.FindStringSubmatch(stem); m != nil {
			it.Group = m[1]
		}
		p.Items = append(p.Items, it)
	}
	return nil
}

func guessCategory(dir, stem, def string) string {
	s := strings.ToLower(stem)
	d := strings.ToLower(filepath.Base(dir))
	switch {
	case strings.Contains(s, "catalog"):
		return "目录图"
	case strings.Contains(s, "concept") || strings.Contains(d, "concept"):
		return "概念图"
	case strings.Contains(s, "compar"):
		return "对比图"
	case def != "":
		return def
	}
	return "图"
}

func imageSize(path string) []int {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	c, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil
	}
	return []int{c.Width, c.Height}
}

func gitRoot(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitHead(dir string) (branch, commit string) {
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		branch = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output(); err == nil {
		commit = strings.TrimSpace(string(out))
	}
	return branch, commit
}

var resultLabel = map[string]string{"created": "新素材", "updated": "新版本", "unchanged": "未变"}

// Text 是 ks asset push 与 MCP asset_push 的输出。
func Text(p protocol.AssetPush, out protocol.AssetPushReply, notes []string) string {
	var b strings.Builder
	if out.Queued {
		fmt.Fprintf(&b, "%s，批次 %s 的 %d 条素材已排队，连上 Hub 后自动补传（ks asset queue 查看）。\n", out.Reason, p.Batch, len(p.Items))
	} else if r := out.Result; r != nil {
		counts := map[string]int{}
		for _, it := range r.Items {
			counts[it.Result]++
		}
		var parts []string
		for _, k := range []string{"created", "updated", "unchanged"} {
			if counts[k] > 0 {
				parts = append(parts, fmt.Sprintf("%s %d 条", resultLabel[k], counts[k]))
			}
		}
		fmt.Fprintf(&b, "已推送批次 %s（项目「%s」）：%s", r.Batch, r.Project, strings.Join(parts, "，"))
		if r.Reused > 0 {
			fmt.Fprintf(&b, "；Hub 已有的 %d 个文件未重传", r.Reused)
		}
		b.WriteString("。\n")
		for _, it := range r.Items {
			fmt.Fprintf(&b, "  %s  %s  第 %d 版（%s）\n", it.Code, it.Key, it.Version, resultLabel[it.Result])
		}
	}
	for _, n := range notes {
		b.WriteString("提示：" + n + "\n")
	}
	return b.String()
}
