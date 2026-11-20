package assetpush

// asset.json 推送（R-376）：素材以带命名空间的稳定 id（如 core:weapon.carbine）为主键，批次降为可多值的元数据。
// 认两种写法：一件素材一个 asset.json（顶层就是这件素材，可以推整个目录，逐个找 asset.json），
// 或一个 asset.json 里写 "assets": [...] 列出多件。通用字段由纸鸢定义，其它字段原样存进 meta、网页上展示。
// 写法细节见 FormatHelp（ks asset help 打印）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// FormatHelp 是 asset.json 的写法（ks asset help 打印）。示例文件名只用跨平台安全的字符。
const FormatHelp = `asset.json 的写法（ks asset push <asset.json 或目录>）：
一件素材一个 asset.json（推目录时逐个找 asset.json），或一个文件里用 "assets": [...] 列出多件。

{
  "id": "core:weapon.carbine",          必填：命名空间:名字。命名空间用小写字母、数字和 _ . -；名字用字母、数字和 _ . - /
  "category": "weapon",                 必填：类别
  "file": "carbine.glb",                主文件（GLB、图片、SVG、音效 OGG / WAV 或字体 TTF / OTF / WOFF2），相对 asset.json；不写时取 variants 里默认变体的文件
  "variants": {"default": {"files": {"model": "carbine.glb"}}, "desert": {"file": "carbine_desert.glb", "previews": ["previews/carbine@desert__front.png"]}},
                                        可选：具名变体。默认变体（default_variant，没写时是 default）就是素材本身；
                                        其余变体各是一条素材「core:weapon.carbine@desert」，有自己的版本和审核，网页上可并排比较。
                                        变体可写 file / files、previews、triangles、source、license，没写的沿用素材本身的；
                                        预览图也可按文件名「<名字>@<变体名>__<机位>.png」归属。asset.json 里删掉的变体不会自动删除
  "default_variant": "default",
  "previews": [{"path": "previews/carbine__front.png", "view": "front"}],   可选：预览图（文件或目录，也可只写路径）
  "batches": ["G3", "G15"],             可选：批次，可多个
  "sources": ["art/_shared/carbine.blend"], "license": {"name": "CC0", "notes": "..."},   可选：来源、许可
  "budget": {"triangles": 1200, "max_triangles": 1500}, "size": {"dimensions_m": [0.7, 0.2, 0.1]},
  "display": {"name": "卡宾枪"},        可选：中文名（给人看的名字，不想填就别填，别为了填而编）；有时网页以它作主标题、素材 id 作副标题，也能按它搜；变体沿用素材的
  "title": "卡宾枪"                     其它字段是项目特有的，原样存进 meta，网页上展示
}
音效（file 是 .ogg / .opus / .wav）：时长、声道、采样率由推送端从文件读；响度（LUFS）、峰值（dBFS）和波形 WAV 直接算，
  OGG 要本机有 ffmpeg 解码。读不出的可声明："audio": {"loudness_lufs": -18, "peak_dbfs": -1, "duration_s": 1.2, "channels": 2, "sample_rate": 48000}；
  文件里读出的优先，声明只补读不出的（网页上标「声明」），不一致时提示。多个版本的音效写成 variants（如 {"default": {"file": "hit_01.ogg"},
  "b": {"file": "hit_02.ogg"}}），网页上可逐个试听；previews 可放封面图，不放时图片墙画波形。
字体（file 是 .ttf / .otf / .woff2）：网页上显示字形预览；files 里有多个字重（font_regular、font_bold……）时取 regular 当主文件。
SVG（file 是 .svg）：归独立的图，当图片显示，里面的脚本不会执行。字体和 SVG 都按内容校验（文件头、表目录 / XML 结构），不只看扩展名。
多件写法：{"namespace": "core", "batches": ["G12"], "previews_dir": "previews", "assets": [{...}, {...}]}，
  namespace 给没写命名空间的 id 补上，batches 每件都带，previews_dir 是按文件名归属预览图的目录。

项目配置：仓库里放一份 .kitestring/assets.json，推送时从 asset.json 所在目录往上找：
  {"categories": ["weapon", "prop"], "previews_base": "art/{category}/{name}"}
  categories 是允许的类别（没有时只校验非空和格式，--categories 可另给一份）；
  previews_base 是预览图路径的起点（相对仓库根，可用 {category} {name} {path} {namespace}），asset.json 里的预览图路径先相对它找，
  它下面 previews 目录里没列出的图也按所在目录挂到这件素材上；previews_base 空着时只按 previews 列表和 asset.json 所在目录找。
预览图文件名写「<名字>__<机位>.png」，名字是 id 冒号后的部分或去掉类别前缀的部分（core:weapon.carbine → weapon.carbine 或 carbine），
  如 previews/carbine__front.png；文件名不写完整 id（冒号在 Windows 上不能用），只用字母、数字和 _ . -。网页按机位分组。
推送前在本地逐件校验并报告：id 格式与重复、类别、文件在不在、类型、Windows 不允许的字符（: * ? " < > | 等）、
  只差大小写的重名；不合命名、挂不上素材的预览图逐个列出，不推送。有错时整批不推，--skip-invalid 只推合格的，--dry-run 只校验；
  --category、--id（可带 * 按前缀）只推其中一部分。
同一 id 内容有变的重推记为新版本（旧版本保留、回到待审）；内容没变不新增版本，只并入新的批次。
旧条目换成新 id 后：ks asset supersede --map 对照表 把旧条目标为「已被某 id 取代」（旧条目保留，网页和 list / get 显示取代关系，
  挂在批次 / 类别上的分享照常能看，并带上取代它的素材）。推错的条目只能在网页上删除。
`

// ConfigFile 是项目级的素材配置（类别表、预览图起点），从 asset.json 所在目录往上找到仓库根为止。
const ConfigFile = ".kitestring/assets.json"

// genericKeys 是纸鸢认识并单独存下的通用字段，不再进 meta。budget、size 写成对象时整个留在 meta（里面常有项目自己的检查项）。
var genericKeys = map[string]bool{"format": true, "id": true, "category": true, "file": true, "previews": true, "batch": true, "batches": true,
	"variants": true, "default_variant": true, "source": true, "sources": true, "license": true, "group": true, "triangles": true,
	"budget_max": true, "pixels": true, "dimensions_px": true, "audio": true, "display": true}

// IDOptions 是按 asset.json 推送的输入。
type IDOptions struct {
	Paths       []string // asset.json 文件或目录（目录里逐个找 asset.json）
	Previews    []string // 额外按文件名归属预览图的目录（--previews）
	Batch       string   // --batch：每件素材都带上
	Categories  string   // 类别允许表；不写时用项目配置里的 categories
	OnlyCat     string   // --category：只推这个类别
	OnlyID      string   // --id：只推这个 id（末尾 * 按前缀）
	SkipInvalid bool     // 有错的素材跳过，只推合格的
	Status      string
	Branch      string
	Commit      string
	Project     string
	Requirement string
	SessionKey  string
	NoFFmpeg    bool // 不用本机 ffmpeg 解 OGG（测试用；默认有 ffmpeg 就用它测 OGG 的响度、峰值和波形）
}

// Check 是一件素材的本地校验结果。
type Check struct {
	ID       string   `json:"id"`
	Source   string   `json:"source" doc:"来自哪个 asset.json"`
	Category string   `json:"category,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	File     string   `json:"file,omitempty"`
	Views    []string `json:"views,omitempty" doc:"预览图的机位"`
	Audio    string   `json:"audio,omitempty" doc:"音效（R-381）：时长、声道、采样率、响度、峰值，一句话"`
	Name     string   `json:"name,omitempty" doc:"中文名（R-400，asset.json 的 display.name）"`
	Batches  []string `json:"batches,omitempty"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Stray 是挂不上素材的预览图。
type Stray struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Report 是推送前的本地校验报告。
type Report struct {
	Paths      []string `json:"paths"`
	Config     string   `json:"config,omitempty" doc:"用到的项目配置"`
	Categories []string `json:"categories,omitempty" doc:"允许的类别；空 = 只校验非空和格式"`
	CatSource  string   `json:"categories_file,omitempty"`
	Items      []Check  `json:"items"`
	Strays     []Stray  `json:"strays,omitempty"`
	Filtered   int      `json:"filtered,omitempty" doc:"不在 --category / --id 范围、没有校验的素材数"`
}

// Bad 是有错的素材数。
func (r Report) Bad() int {
	n := 0
	for _, c := range r.Items {
		if len(c.Errors) > 0 {
			n++
		}
	}
	return n
}

// IsAssetJSON 判断要不要按 asset.json 推送（而不是旧的按批次的 manifest）：给的是目录，文件名是 asset.json / assets.json，
// 或顶层写了 id / namespace，或有素材 id 带命名空间（含冒号）。
func IsAssetJSON(path string) bool {
	st, err := os.Stat(path)
	if err == nil && st.IsDir() {
		return true
	}
	if b := strings.ToLower(filepath.Base(path)); b == "asset.json" || b == "assets.json" {
		return true
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var m struct {
		ID        string `json:"id"`
		Namespace string `json:"namespace"`
		Assets    []struct {
			ID string `json:"id"`
		} `json:"assets"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	if m.Namespace != "" || strings.Contains(m.ID, ":") {
		return true
	}
	for _, a := range m.Assets {
		if strings.Contains(a.ID, ":") {
			return true
		}
	}
	return false
}

type strList []string

// UnmarshalJSON 认字符串或字符串数组。
func (s *strList) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		if one = strings.TrimSpace(one); one != "" {
			*s = []string{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("要写字符串或字符串数组")
	}
	*s = many
	return nil
}

// previewRef 是 asset.json 里列出的一张预览图：路径字符串，或 {path, view}。
type previewRef struct {
	Path string `json:"path"`
	View string `json:"view"`
}

func (p *previewRef) UnmarshalJSON(b []byte) error {
	if json.Unmarshal(b, &p.Path) == nil {
		return nil
	}
	type plain previewRef
	return json.Unmarshal(b, (*plain)(p))
}

type multiFile struct {
	Namespace   string            `json:"namespace"`
	Batch       strList           `json:"batch"`
	Batches     strList           `json:"batches"`
	PreviewsDir strList           `json:"previews_dir"`
	Assets      []json.RawMessage `json:"assets"`
}

type idEntry struct {
	ID             string          `json:"id"`
	Category       string          `json:"category"`
	File           string          `json:"file"`
	Variants       json.RawMessage `json:"variants"`
	DefaultVariant string          `json:"default_variant"`
	Previews       []previewRef    `json:"previews"`
	Batch          strList         `json:"batch"`
	Batches        strList         `json:"batches"`
	Source         json.RawMessage `json:"source"`
	Sources        json.RawMessage `json:"sources"`
	License        json.RawMessage `json:"license"`
	Group          string          `json:"group"`
	Triangles      int             `json:"triangles"`
	Budget         json.RawMessage `json:"budget"`
	BudgetMax      int             `json:"budget_max"`
	Size           json.RawMessage `json:"size"`
	Bounds         *bounds         `json:"bounds_m"`
	BoundsLoc      *bounds         `json:"bounds_local_m"`
	Dimensions     []float64       `json:"dimensions_m"`
	Pixels         []int           `json:"pixels"`
	PixelsOld      []int           `json:"dimensions_px"`
	Audio          json.RawMessage `json:"audio"`
	Display        json.RawMessage `json:"display"`
}

// winBad 是 Windows 文件名不允许的字符。
const winBad = `<>:"|?*\`

var winReserved = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\..*)?$`)

// winNameProblem 检查一段文件名在 Windows 上能不能用，能用返回空。
func winNameProblem(name string) string {
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(winBad, r) {
			return fmt.Sprintf("含 Windows 文件名不允许的字符「%c」", r)
		}
	}
	if winReserved.MatchString(name) {
		return "是 Windows 的保留名"
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return "以点或空格结尾，Windows 上会被改名"
	}
	return ""
}

// winPathProblem 逐段检查 asset.json 里写的相对路径（/ 分隔）。
func winPathProblem(p string) string {
	if filepath.IsAbs(p) {
		return ""
	}
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		if msg := winNameProblem(seg); msg != "" {
			return "「" + seg + "」" + msg
		}
	}
	return ""
}

// checkCategory 校验类别的格式，合格返回空。
func checkCategory(c string, allowed []string) string {
	switch {
	case c == "":
		return "没写类别（category）"
	case c != strings.TrimSpace(c) || len([]rune(c)) > 64:
		return "类别「" + c + "」前后不能有空格，最多 64 个字"
	case strings.ContainsAny(c, winBad) || strings.ContainsFunc(c, func(r rune) bool { return r < 0x20 }):
		return "类别「" + c + "」含不允许的字符（< > : \" | ? * \\）"
	case len(allowed) > 0 && !contains(allowed, c):
		return "类别「" + c + "」不在项目的类别表里（" + strings.Join(allowed, "、") + "）"
	}
	return ""
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// projectConfig 是 ConfigFile 的内容。
type projectConfig struct {
	Categories   []string `json:"categories"`
	PreviewsBase string   `json:"previews_base"`
	root         string   // 配置所在的仓库目录（.kitestring 的上一级）
	path         string
}

// findConfig 从 dir 往上（到仓库根为止）找 ConfigFile。
func findConfig(dir, root string) (projectConfig, error) {
	var c projectConfig
	for d := dir; ; d = filepath.Dir(d) {
		p := filepath.Join(d, ConfigFile)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			raw, err := os.ReadFile(p)
			if err != nil {
				return c, err
			}
			if err := json.Unmarshal(raw, &c); err != nil {
				return c, fmt.Errorf("项目素材配置 %s 不是有效的 JSON：%w", p, err)
			}
			c.root, c.path = d, p
			c.Categories = cleanList(c.Categories)
			return c, nil
		}
		// 到仓库根为止；不在仓库里时一直往上，但不进家目录（~/.kitestring 是纸鸢自己的数据目录）
		if d == root || filepath.Dir(d) == d || filepath.Dir(d) == home() {
			return c, nil
		}
	}
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// loadCategories 读 --categories 指定的类别表：字符串数组，或 {"categories": [...]}。
func loadCategories(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读不到类别表：%w", err)
	}
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		var obj struct {
			Categories []string `json:"categories"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("类别表 %s 要写成字符串数组或 {\"categories\": [...]}：%w", path, err)
		}
		list = obj.Categories
	}
	return cleanList(list), nil
}

// splitView 把预览图文件名拆成名字和机位：「<名字>__<机位>.png」；没有 __ 时 ok = false。
func splitView(file string) (name, view string, ok bool) {
	stem := strings.TrimSuffix(file, filepath.Ext(file))
	i := strings.LastIndex(stem, "__")
	if i <= 0 || i+2 >= len(stem) {
		return stem, "", false
	}
	return stem[:i], stem[i+2:], true
}

// viewOf：预览图的机位，取 __ 后面的部分，没有 __ 时取整个文件名（去扩展名）。
func viewOf(file string) string {
	if _, v, ok := splitView(file); ok {
		return v
	}
	return strings.TrimSuffix(file, filepath.Ext(file))
}

// assetNames 是预览图文件名里可以用的名字：id 冒号后的部分、去掉类别前缀的部分、最后一段。
func assetNames(id, category string) []string {
	_, path, _ := strings.Cut(id, ":")
	names := []string{path}
	if category != "" {
		for _, sep := range []string{".", "/"} {
			if rest, ok := strings.CutPrefix(path, category+sep); ok {
				names = append(names, rest)
			}
		}
	}
	names = append(names, protocol.AssetName(id))
	return cleanList(names)
}

// expand 把 previews_base 模板里的 {category} {name} {path} {namespace} 换成这件素材的值。
func expand(tmpl, id, category string) string {
	ns, path, _ := strings.Cut(id, ":")
	names := assetNames(id, category)
	name := names[len(names)-1]
	if len(names) > 1 {
		name = names[1]
	}
	return strings.NewReplacer("{category}", category, "{name}", name, "{path}", path, "{namespace}", ns, "{id}", id).Replace(tmpl)
}

func listImages(dir string) ([]string, []string, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var files, dirs []string
	for _, de := range des {
		n := de.Name()
		if strings.HasPrefix(n, ".") {
			continue
		}
		if de.IsDir() {
			dirs = append(dirs, n)
		} else if imageExt[strings.ToLower(filepath.Ext(n))] {
			files = append(files, n)
		}
	}
	sort.Strings(files)
	sort.Strings(dirs)
	return files, dirs, nil
}

// findAssetJSON 在目录里逐个找 asset.json（跳过隐藏目录和 node_modules）。
func findAssetJSON(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "asset.json" {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// one 是读出来的一件素材（还没校验）。
type one struct {
	raw     json.RawMessage
	dir     string // asset.json 所在目录
	src     string // asset.json 路径（报告用）
	ns      string
	batches []string
	scan    []string // 按文件名归属预览图的目录
}

// textOf 把来源、许可写成一句：字符串原样；数组用「、」连起来；对象取 name（有 notes 时接在后面）。
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return strings.Join(cleanList(list), "、")
	}
	var obj struct {
		Name  string `json:"name"`
		Notes string `json:"notes"`
		URL   string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Name != "" {
		out := obj.Name
		if obj.URL != "" {
			out += "（" + obj.URL + "）"
		}
		if obj.Notes != "" {
			out += "：" + obj.Notes
		}
		return out
	}
	return string(raw)
}

// variantFile 在 variants 里找默认变体的主文件：先 default_variant，再 default，再第一个；文件按 pickMainFile 的顺序取。
func variantFile(raw json.RawMessage, def string) string {
	if len(raw) == 0 {
		return ""
	}
	type variant struct {
		Name  string            `json:"name"`
		File  string            `json:"file"`
		Files map[string]string `json:"files"`
	}
	pick := func(v variant) string {
		if v.File != "" {
			return v.File
		}
		return pickMainFile(v.Files)
	}
	var byName map[string]variant
	if json.Unmarshal(raw, &byName) == nil && len(byName) > 0 {
		for _, k := range []string{def, "default"} {
			if v, ok := byName[k]; ok && k != "" {
				return pick(v)
			}
		}
		keys := make([]string, 0, len(byName))
		for k := range byName {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return pick(byName[keys[0]])
	}
	var list []variant
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		for _, v := range list {
			if v.Name != "" && (v.Name == def || v.Name == "default") {
				return pick(v)
			}
		}
		return pick(list[0])
	}
	return ""
}

func readOnes(path string) ([]one, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读不到 asset.json：%w", err)
	}
	dir, _ := filepath.Abs(filepath.Dir(path))
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("%s 不是有效的 JSON：%w", path, err)
	}
	if _, multi := probe["assets"]; !multi {
		if _, ok := probe["id"]; !ok {
			return nil, fmt.Errorf("%s 里既没有 id 也没有 assets", path)
		}
		return []one{{raw: raw, dir: dir, src: path}}, nil
	}
	var m multiFile
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s 无法解析：%w", path, err)
	}
	if len(m.Assets) == 0 {
		return nil, fmt.Errorf("%s 里没有素材（assets）", path)
	}
	scan := []string{}
	for _, d := range m.PreviewsDir {
		if !filepath.IsAbs(d) {
			d = filepath.Join(dir, d)
		}
		scan = append(scan, d)
	}
	if len(scan) == 0 {
		if st, err := os.Stat(filepath.Join(dir, "previews")); err == nil && st.IsDir() {
			scan = append(scan, filepath.Join(dir, "previews"))
		}
	}
	out := make([]one, 0, len(m.Assets))
	for _, a := range m.Assets {
		out = append(out, one{raw: a, dir: dir, src: path, ns: strings.TrimSpace(m.Namespace), batches: append(append([]string{}, m.Batch...), m.Batches...), scan: scan})
	}
	return out, nil
}

// matchID：--id 过滤，末尾带 * 按前缀。
func matchID(filter, id string) bool {
	if filter == "" {
		return true
	}
	if pre, ok := strings.CutSuffix(filter, "*"); ok {
		return strings.HasPrefix(id, pre)
	}
	return id == filter
}

// BuildByID 读 asset.json（文件或目录），做推送前的本地校验，返回推送内容（只含合格的素材）和逐件报告。
// 有错的素材：SkipInvalid 时跳过，否则调用方应不推送（报告照样给出）。读不了文件、JSON 无效时返回 error。
func BuildByID(o IDOptions) (protocol.AssetPush, Report, error) {
	p := protocol.AssetPush{ByID: true, Status: o.Status, Project: o.Project, Requirement: o.Requirement, SessionKey: o.SessionKey, Branch: o.Branch, Commit: o.Commit}
	rep := Report{Paths: o.Paths, Items: []Check{}}
	if len(o.Paths) == 0 {
		return p, rep, errors.New("要给 asset.json 或放着 asset.json 的目录")
	}
	var files []string
	for _, path := range o.Paths {
		st, err := os.Stat(path)
		if err != nil {
			return p, rep, fmt.Errorf("读不到 %s：%w", path, err)
		}
		if !st.IsDir() {
			files = append(files, path)
			continue
		}
		found, err := findAssetJSON(path)
		if err != nil {
			return p, rep, err
		}
		if len(found) == 0 {
			return p, rep, fmt.Errorf("%s 下面没有 asset.json", path)
		}
		files = append(files, found...)
	}
	var ones []one
	for _, f := range files {
		got, err := readOnes(f)
		if err != nil {
			return p, rep, err
		}
		ones = append(ones, got...)
	}
	first, _ := filepath.Abs(filepath.Dir(files[0]))
	root := gitRoot(first)
	cfg, err := findConfig(first, root)
	if err != nil {
		return p, rep, err
	}
	rep.Config = cfg.path
	rep.Categories, rep.CatSource = cfg.Categories, cfg.path
	if o.Categories != "" {
		if rep.Categories, err = loadCategories(o.Categories); err != nil {
			return p, rep, err
		}
		rep.CatSource = o.Categories
	}
	extra := cleanList([]string{o.Batch})

	type built struct {
		it    protocol.AssetItem
		check Check
		one   one
	}
	var all []*built
	byLower := map[string]*built{}
	for i, x := range ones {
		b := &built{one: x}
		c := &b.check
		c.Source = x.src
		var e idEntry
		if err := json.Unmarshal(x.raw, &e); err != nil {
			c.ID = fmt.Sprintf("%s 第 %d 项", x.src, i+1)
			c.Errors = append(c.Errors, "无法解析："+err.Error())
			all = append(all, b)
			continue
		}
		id := strings.TrimSpace(e.ID)
		if id != "" && x.ns != "" && !strings.Contains(id, ":") {
			id = x.ns + ":" + id
		}
		c.ID = id
		c.Category = strings.TrimSpace(e.Category)
		if !matchID(o.OnlyID, id) || o.OnlyCat != "" && c.Category != o.OnlyCat {
			rep.Filtered++
			continue
		}
		all = append(all, b)
		if id == "" {
			c.ID = fmt.Sprintf("%s 第 %d 项", x.src, i+1)
			c.Errors = append(c.Errors, "没有 id")
		} else if msg := protocol.CheckAssetID(id); msg != "" {
			c.Errors = append(c.Errors, "id "+msg)
		} else if prev, ok := byLower[strings.ToLower(id)]; ok {
			if prev.check.ID == id {
				c.Errors = append(c.Errors, "id 重复了（另一处在 "+prev.check.Source+"）")
			} else {
				c.Errors = append(c.Errors, "和 "+prev.check.ID+" 只差大小写，Windows 上会同名")
			}
		} else {
			byLower[strings.ToLower(id)] = b
			for _, n := range assetNames(id, c.Category) {
				for _, seg := range strings.Split(n, "/") {
					if msg := winNameProblem(seg); msg != "" {
						c.Errors = append(c.Errors, "名字「"+seg+"」"+msg+"（预览图文件名要用它）")
					}
				}
			}
		}
		if msg := checkCategory(e.Category, rep.Categories); msg != "" {
			c.Errors = append(c.Errors, msg)
		}
		it := protocol.AssetItem{Key: id, Category: c.Category, Group: strings.TrimSpace(e.Group), Source: textOf(e.Source), License: textOf(e.License)}
		if it.Source == "" {
			it.Source = textOf(e.Sources)
		}
		var nameErr string
		if it.Name, nameErr = displayName(e.Display); nameErr != "" {
			c.Errors = append(c.Errors, nameErr)
		}
		c.Name = it.Name
		c.Batches = cleanList(append(append(append(append([]string{}, x.batches...), e.Batch...), e.Batches...), extra...))
		it.Batches = c.Batches
		if len(e.Variants) > 0 && string(e.Variants) != "null" {
			it.Variants = e.Variants
		}
		// 主文件：file，或默认变体的文件
		fileRef := strings.TrimSpace(e.File)
		if fileRef == "" {
			fileRef = variantFile(e.Variants, e.DefaultVariant)
		}
		switch {
		case fileRef == "":
			c.Errors = append(c.Errors, "没写主文件（file，或 variants 里的文件）")
		case winPathProblem(fileRef) != "":
			c.Errors = append(c.Errors, "文件路径"+winPathProblem(fileRef))
		default:
			c.File = fileRef
			file := resolve(x.dir, root, fileRef)
			if file == "" {
				c.Errors = append(c.Errors, "文件找不到："+fileRef)
				break
			}
			it.File = &protocol.AssetFile{Path: file, Name: filepath.Base(file)}
			switch it.Kind = fileKind(file); it.Kind {
			case protocol.AssetImage:
				it.Pixels = e.Pixels
				if len(it.Pixels) != 2 {
					it.Pixels = e.PixelsOld
				}
				if px, svg := checkFontSVG(file, c); svg && len(it.Pixels) != 2 {
					it.Pixels = px
				}
				if len(it.Pixels) != 2 {
					it.Pixels = imageSize(file)
				}
			case protocol.AssetFont:
				checkFontSVG(file, c)
			case protocol.AssetAudio:
				var errs, warns []string
				it.Audio, errs, warns = audioInfo(file, e.Audio, !o.NoFFmpeg)
				c.Errors, c.Warnings, c.Audio = append(c.Errors, errs...), append(c.Warnings, warns...), AudioText(it.Audio)
			case "":
				c.Errors = append(c.Errors, "文件类型不支持："+filepath.Base(file)+unsupportedType)
			}
			c.Kind = it.Kind
		}
		// 面数、预算、尺寸：数字 / 数组，或 rollcall 式的对象（budget.triangles / max_triangles，size.dimensions_m）
		it.Triangles, it.Budget = e.Triangles, e.BudgetMax
		var bud struct {
			Triangles    int `json:"triangles"`
			MaxTriangles int `json:"max_triangles"`
			Max          int `json:"max"`
		}
		if len(e.Budget) > 0 && json.Unmarshal(e.Budget, &it.Budget) != nil && json.Unmarshal(e.Budget, &bud) == nil {
			it.Budget = max(bud.MaxTriangles, bud.Max)
			if it.Triangles == 0 {
				it.Triangles = bud.Triangles
			}
		}
		var size struct {
			Dimensions []float64 `json:"dimensions_m"`
		}
		switch {
		case len(e.Size) > 0 && json.Unmarshal(e.Size, &it.Size) == nil && len(it.Size) == 3:
		case len(e.Size) > 0 && json.Unmarshal(e.Size, &size) == nil && len(size.Dimensions) == 3:
			it.Size = size.Dimensions
		case e.Bounds != nil && len(e.Bounds.Size) == 3:
			it.Size = e.Bounds.Size
		case e.BoundsLoc != nil && len(e.BoundsLoc.Size) == 3:
			it.Size = e.BoundsLoc.Size
		case len(e.Dimensions) == 3:
			it.Size = e.Dimensions
		default:
			it.Size = nil
		}
		it.Meta = projectMeta(x.raw)
		// 明确列出的预览图（文件或目录）：先相对 asset.json，再相对项目配置的 previews_base，最后相对仓库根
		base := ""
		if cfg.PreviewsBase != "" && id != "" {
			base = filepath.Join(cfg.root, filepath.FromSlash(expand(cfg.PreviewsBase, id, c.Category)))
		}
		names := assetNames(id, c.Category)
		for _, pv := range e.Previews {
			ref := strings.TrimSpace(pv.Path)
			if ref == "" {
				c.Errors = append(c.Errors, "previews 里有一项没写路径")
				continue
			}
			if msg := winPathProblem(ref); msg != "" {
				c.Errors = append(c.Errors, "预览图路径"+msg)
				continue
			}
			path := ""
			for _, d := range []string{x.dir, base, root} {
				if d == "" && !filepath.IsAbs(ref) {
					continue
				}
				cand := ref
				if !filepath.IsAbs(ref) {
					cand = filepath.Join(d, ref)
				}
				if _, err := os.Stat(cand); err == nil {
					path = cand
					break
				}
			}
			st, err := os.Stat(path)
			switch {
			case path == "" || err != nil:
				c.Errors = append(c.Errors, "预览图找不到："+ref)
			case st.IsDir():
				imgs, _, _ := listImages(path)
				if len(imgs) == 0 {
					c.Warnings = append(c.Warnings, "预览图目录里没有图："+ref)
				}
				for _, n := range imgs {
					addPreview(c, &it, filepath.Join(path, n), n, "", names)
				}
			case !imageExt[strings.ToLower(filepath.Ext(path))]:
				c.Errors = append(c.Errors, "预览图要是图片："+ref)
			default:
				addPreview(c, &it, path, filepath.Base(path), strings.TrimSpace(pv.View), names)
			}
		}
		// 按所在目录归属：previews_base 下 previews 目录里没列出的图也挂上，并提示补进 asset.json
		if base != "" {
			dir := filepath.Join(base, "previews")
			if imgs, _, err := listImages(dir); err == nil {
				for _, n := range imgs {
					path := filepath.Join(dir, n)
					if !hasPreview(it, path) {
						addPreview(c, &it, path, n, "", names)
						c.Warnings = append(c.Warnings, "预览图 "+n+" 没在 previews 里列出，按所在目录挂上")
					}
				}
			}
		}
		// 具名变体（R-380）：默认变体就是素材本身，其余变体各推一条（排在素材后面）
		if len(it.Variants) > 0 && id != "" && protocol.CheckAssetID(id) == "" {
			def, vs, errs := variantItems(it, x.src, e.Variants, e.DefaultVariant, []string{x.dir, base, root}, root, !o.NoFFmpeg)
			it.Variant = def
			c.Errors = append(c.Errors, errs...)
			for _, v := range vs {
				all = append(all, &built{it: v.it, check: v.check, one: x})
			}
		}
		b.it = it
	}

	// 按文件名「<名字>__<机位>」或以名字命名的子目录归属的预览图（多件写法的 previews_dir、--previews）
	byName := map[string][]*built{}
	for _, b := range all {
		if b.it.Key != "" && (protocol.CheckAssetID(b.it.Key) == "" || b.it.Parent != "") { // 变体的名字是「<名字>@<变体名>」
			for _, n := range assetNames(b.it.Key, b.it.Category) {
				byName[n] = append(byName[n], b)
			}
		}
	}
	scanDirs := map[string]bool{}
	var scans []string
	for _, b := range all {
		for _, d := range b.one.scan {
			if !scanDirs[d] {
				scanDirs[d] = true
				scans = append(scans, d)
			}
		}
	}
	for _, d := range o.Previews {
		if abs, _ := filepath.Abs(d); !scanDirs[abs] {
			scanDirs[abs] = true
			scans = append(scans, abs)
		}
	}
	rel := func(p string) string {
		if r, err := filepath.Rel(first, p); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
		return p
	}
	attach := func(path, file, name string) {
		idx := byName[name]
		switch {
		case len(idx) == 0:
			rep.Strays = append(rep.Strays, Stray{rel(path), "没有名字是「" + name + "」的素材"})
		case len(idx) > 1 && idx[0] != idx[1]:
			rep.Strays = append(rep.Strays, Stray{rel(path), "名字「" + name + "」对应多件素材，请在 asset.json 的 previews 里写明"})
		case idx[0].it.Kind == protocol.AssetImage:
			rep.Strays = append(rep.Strays, Stray{rel(path), idx[0].it.Key + " 是图片素材，不挂预览图"})
		case hasPreview(idx[0].it, path):
		default:
			addPreview(&idx[0].check, &idx[0].it, path, file, "", nil)
		}
	}
	for _, d := range scans {
		names, subs, err := listImages(d)
		if err != nil {
			return p, rep, fmt.Errorf("读不到预览图目录：%w", err)
		}
		lower := map[string]string{}
		for _, n := range names {
			path := filepath.Join(d, n)
			if prev, ok := lower[strings.ToLower(n)]; ok {
				rep.Strays = append(rep.Strays, Stray{rel(path), "和 " + prev + " 只差大小写，Windows 上会同名"})
				continue
			}
			lower[strings.ToLower(n)] = n
			if msg := winNameProblem(n); msg != "" {
				rep.Strays = append(rep.Strays, Stray{rel(path), "文件名" + msg})
				continue
			}
			name, _, ok := splitView(n)
			if !ok {
				listed := false
				for _, b := range all {
					listed = listed || hasPreview(b.it, path)
				}
				if !listed {
					rep.Strays = append(rep.Strays, Stray{rel(path), "文件名不合「<名字>__<机位>.png」"})
				}
				continue
			}
			attach(path, n, name)
		}
		for _, s := range subs {
			if _, ok := byName[s]; !ok {
				continue
			}
			imgs, _, _ := listImages(filepath.Join(d, s))
			for _, n := range imgs {
				attach(filepath.Join(d, s, n), n, s)
			}
		}
	}

	badMain := map[string]bool{}
	for _, b := range all {
		c, it := &b.check, &b.it
		if it.Parent != "" && badMain[it.Parent] {
			c.Errors = append(c.Errors, "所属的素材 "+it.Parent+" 有错，变体不能单独推送")
		}
		if it.Kind == protocol.AssetImage && len(it.Previews) > 0 {
			c.Errors = append(c.Errors, "图片素材不挂预览图（预览图只给 GLB）")
		}
		if it.Kind == protocol.AssetPart && len(it.Previews) == 0 {
			c.Warnings = append(c.Warnings, "没有预览图，资源库会用模型自动生成缩略图；要用自己的图，按「<名字>__<机位>.png」放预览图")
		}
		seen := map[string]bool{}
		for _, pv := range it.Previews {
			if seen[strings.ToLower(pv.View)] {
				c.Warnings = append(c.Warnings, "机位「"+pv.View+"」有不止一张图")
			}
			seen[strings.ToLower(pv.View)] = true
		}
		if len(c.Errors) > 0 && it.Parent == "" {
			badMain[it.Key] = true
		}
		rep.Items = append(rep.Items, *c)
		if len(c.Errors) == 0 {
			p.Items = append(p.Items, *it)
		}
	}
	if p.Branch == "" || p.Commit == "" {
		br, cm := gitHead(first)
		if p.Branch == "" {
			p.Branch = br
		}
		if p.Commit == "" {
			p.Commit = cm
		}
	}
	return p, rep, nil
}

func hasPreview(it protocol.AssetItem, path string) bool {
	for _, x := range it.Previews {
		if x.Path == path {
			return true
		}
	}
	return false
}

// addPreview 挂一张预览图；view 为空时从文件名取。names 不为空时检查文件名里的名字和素材对不对得上（只提示）。
func addPreview(c *Check, it *protocol.AssetItem, path, file, view string, names []string) {
	if hasPreview(*it, path) {
		return
	}
	if msg := winNameProblem(file); msg != "" {
		c.Errors = append(c.Errors, "预览图 "+file+" 的文件名"+msg)
		return
	}
	if name, _, ok := splitView(file); !ok {
		c.Warnings = append(c.Warnings, "预览图 "+file+" 的文件名不合「<名字>__<机位>.png」")
	} else if len(names) > 0 && !contains(names, name) {
		c.Warnings = append(c.Warnings, "预览图 "+file+" 文件名里的名字「"+name+"」和素材（"+strings.Join(names, " / ")+"）对不上")
	}
	if view == "" {
		view = viewOf(file)
	}
	it.Previews = append(it.Previews, protocol.AssetFile{Path: path, Name: file, View: view})
	if !contains(c.Views, view) {
		c.Views = append(c.Views, view)
	}
}

func cleanList(xs []string) []string {
	var out []string
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" && !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// MaxDisplayName 是中文名的最长字数。
const MaxDisplayName = 64

// displayName 取 asset.json 的 display.name（R-400）：可选；写了要是不含换行的字符串，最多 64 个字。返回名字和不合格的原因。
func displayName(raw json.RawMessage) (string, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	var d struct {
		Name json.RawMessage `json:"name"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return "", "display 要写成对象，如 {\"name\": \"卡宾枪\"}"
	}
	if len(d.Name) == 0 || string(d.Name) == "null" {
		return "", ""
	}
	var name string
	if json.Unmarshal(d.Name, &name) != nil {
		return "", "display.name 要是字符串"
	}
	name = strings.TrimSpace(name)
	switch {
	case strings.ContainsAny(name, "\r\n"):
		return "", "display.name 不能换行"
	case len([]rune(name)) > MaxDisplayName:
		return "", fmt.Sprintf("display.name 太长（最多 %d 个字）", MaxDisplayName)
	}
	return name, ""
}

// projectMeta 取出项目特有字段（纸鸢不认识的），原样存进 meta；没有时返回空。
func projectMeta(r json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(r, &m) != nil {
		return nil
	}
	for k, v := range m {
		if genericKeys[k] {
			delete(m, k)
			continue
		}
		// budget、size、bounds 写成数字 / 数组时纸鸢已单独存下；写成对象时整个留在 meta
		if (k == "budget" || k == "size" || k == "bounds_m" || k == "bounds_local_m" || k == "dimensions_m") && !strings.HasPrefix(strings.TrimSpace(string(v)), "{") {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	b, _ := json.Marshal(m)
	return b
}

var checkLabel = []string{"可推送", "有提示", "有错"}

// ReportText 是逐件的校验报告（ks asset push 推送前、--dry-run 时打印）。
func ReportText(rep Report) string {
	var b strings.Builder
	counts := [3]int{}
	for _, c := range rep.Items {
		counts[level(c)]++
	}
	fmt.Fprintf(&b, "校验 %s：%d 件素材，可推送 %d 件", strings.Join(rep.Paths, "、"), len(rep.Items), counts[0]+counts[1])
	if counts[1] > 0 {
		fmt.Fprintf(&b, "（其中 %d 件有提示）", counts[1])
	}
	if counts[2] > 0 {
		fmt.Fprintf(&b, "，有错 %d 件", counts[2])
	}
	if rep.Filtered > 0 {
		fmt.Fprintf(&b, "；不在 --category / --id 范围的 %d 件没有校验", rep.Filtered)
	}
	b.WriteString("\n")
	if rep.CatSource != "" {
		fmt.Fprintf(&b, "类别表：%s（%s）\n", rep.CatSource, strings.Join(rep.Categories, "、"))
	} else {
		fmt.Fprintf(&b, "类别表：没有找到 %s 里的 categories，类别只校验非空和格式\n", ConfigFile)
	}
	width := 0
	for _, c := range rep.Items {
		width = max(width, len(c.ID))
	}
	for _, c := range rep.Items {
		var parts []string
		if c.Name != "" {
			parts = append(parts, "「"+c.Name+"」")
		}
		if c.Category != "" {
			parts = append(parts, c.Category)
		}
		if k := kindLabel(c.Kind, c.File); k != "" {
			parts = append(parts, k)
		}
		if c.Kind == protocol.AssetAudio && c.Audio != "" {
			parts = append(parts, c.Audio)
		}
		if len(c.Views) > 0 {
			parts = append(parts, fmt.Sprintf("预览图 %d 个机位（%s）", len(c.Views), strings.Join(c.Views, "、")))
		}
		if len(c.Batches) > 0 {
			parts = append(parts, "批次 "+strings.Join(c.Batches, "、"))
		}
		fmt.Fprintf(&b, "  %s  %-*s  %s\n", checkLabel[level(c)], width, c.ID, strings.Join(parts, " · "))
		for _, e := range c.Errors {
			fmt.Fprintf(&b, "        错误：%s\n", e)
		}
		for _, w := range c.Warnings {
			fmt.Fprintf(&b, "        提示：%s\n", w)
		}
	}
	if len(rep.Strays) > 0 {
		fmt.Fprintf(&b, "挂不上素材的预览图 %d 张（不会推送）：\n", len(rep.Strays))
		for _, s := range rep.Strays {
			fmt.Fprintf(&b, "  %s  %s\n", s.Path, s.Reason)
		}
	}
	return b.String()
}

func level(c Check) int {
	switch {
	case len(c.Errors) > 0:
		return 2
	case len(c.Warnings) > 0:
		return 1
	}
	return 0
}
