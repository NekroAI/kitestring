// Package blocks 是表达块（R-97）：Agent 交证据、提决策、交阶段小结时可以附的结构化图示。
// 块的种类是固定的一套（清单、对比、截图、流程、指标、改动范围、选项、小表格），由纸鸢统一渲染，
// 不接受任意 HTML 或脚本。解析是宽松的：不合法的块丢掉并给出清楚的提示，能用的块照常保存，
// 证据 / 决策本身不会因为某个块写错而交不上去。
package blocks

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 块类型。
const (
	Checklist = "checklist"
	Compare   = "compare"
	Figure    = "figure"
	Flow      = "flow"
	Metrics   = "metrics"
	Scope     = "scope"
	Options   = "options"
	Table     = "table"
)

// Types 是全部块类型，按说明里的顺序。
var Types = []string{Checklist, Compare, Figure, Flow, Metrics, Scope, Options, Table}

// 清单项的判定。
const (
	Pass = "pass" // 达标
	Fail = "fail" // 未达标
	Ask  = "ask"  // 要你看
)

// 上限：超出的部分截掉并提示，不整块丢弃。
const (
	MaxBlocks      = 12
	MaxChecks      = 12
	MaxMetrics     = 4
	MaxScope       = 10
	MaxOptions     = 4
	MaxColumns     = 6
	MaxRows        = 20
	MaxMermaid     = 4000
	MaxTrend       = 24
	MaxText        = 300 // 单个短文本字段（依据、说明、单元格）
	MaxListItems   = 8   // todo / aside
	MaxDetailsText = 4000
)

// File 是块里引用的附件（与 store.FileRef 同形）。
type File struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
	// Title / Boxes：截图的一句话标题与改动框（R-183），旧数据没有
	Title string `json:"title,omitempty" doc:"一句话标题"`
	Boxes []Box  `json:"boxes,omitempty" doc:"改动框：按图片原始尺寸的比例坐标（0～1）"`
}

// Item 是块里的一项，按块类型使用不同字段：
// checklist 用 criterion / text / result / why / see；metrics 用 label / value / unit / note / trend；
// scope 用 label（模块）/ count / note；options 用 label / desc / recommended / image。
type Item struct {
	Criterion   int       `json:"criterion,omitempty" doc:"checklist：对应第几条验收标准（从 1 开始）"`
	Text        string    `json:"text,omitempty" doc:"checklist：这一项验收标准"`
	Result      string    `json:"result,omitempty" enum:"pass,fail,ask" doc:"checklist：pass 达标 / fail 未达标 / ask 要你看"`
	Why         string    `json:"why,omitempty" doc:"checklist：一句依据"`
	See         string    `json:"see,omitempty" doc:"checklist：关联的块 id，点这一项会跳到那个块"`
	Label       string    `json:"label,omitempty" doc:"metrics：指标名；scope：模块名；options：选项名"`
	Value       string    `json:"value,omitempty" doc:"metrics：数值"`
	Unit        string    `json:"unit,omitempty" doc:"metrics：单位"`
	Note        string    `json:"note,omitempty" doc:"metrics / scope：一句补充"`
	Trend       []float64 `json:"trend,omitempty" doc:"metrics：小趋势线的数据点"`
	Count       int       `json:"count,omitempty" doc:"scope：改动数"`
	Desc        string    `json:"desc,omitempty" doc:"options：一句说明"`
	Recommended bool      `json:"recommended,omitempty" doc:"options：是否推荐"`
	Image       *File     `json:"image,omitempty" doc:"options：效果图"`
}

// Block 是一个表达块。
type Block struct {
	ID      string     `json:"id"`
	Type    string     `json:"type" enum:"checklist,compare,figure,flow,metrics,scope,options,table"`
	Title   string     `json:"title,omitempty" doc:"块的小标题"`
	Caption string     `json:"caption,omitempty" doc:"图注 / 说明"`
	Items   []Item     `json:"items,omitempty"`
	Image   *File      `json:"image,omitempty" doc:"figure：截图或录屏"`
	Before  *File      `json:"before,omitempty" doc:"compare：改之前"`
	After   *File      `json:"after,omitempty" doc:"compare：改之后"`
	Mermaid string     `json:"mermaid,omitempty" doc:"flow：mermaid 源码"`
	Columns []string   `json:"columns,omitempty" doc:"table：表头（最多 6 列）"`
	Rows    [][]string `json:"rows,omitempty" doc:"table：行"`
}

// Payload 是证据的结构化部分（原型 ⑤）：一句结论、要你做的、表达块、默认折叠的实现细节与顺带改动。
type Payload struct {
	Verdict string   `json:"verdict,omitempty" doc:"一句结论（40 字以内）"`
	Todo    []string `json:"todo,omitempty" doc:"要你做的：只写机器验证不了的事"`
	Blocks  []Block  `json:"blocks,omitempty"`
	Details string   `json:"details,omitempty" doc:"实现细节（给工程师，默认折叠）"`
	Aside   []string `json:"aside,omitempty" doc:"顺带改动（默认折叠）"`
}

// Empty 判断是否没有任何结构化内容。
func (p *Payload) Empty() bool {
	return p == nil || p.Verdict == "" && len(p.Todo) == 0 && len(p.Blocks) == 0 && p.Details == "" && len(p.Aside) == 0
}

// Checklist 返回第一个清单块（没有时为 nil）。
func (p *Payload) Checklist() *Block { return FindChecklist(p.blockList()) }

// BlockList 返回全部块（nil 时为空）。
func (p *Payload) BlockList() []Block { return p.blockList() }

func (p *Payload) blockList() []Block {
	if p == nil {
		return nil
	}
	return p.Blocks
}

// FindChecklist 返回第一个清单块。
func FindChecklist(bs []Block) *Block {
	for i := range bs {
		if bs[i].Type == Checklist {
			return &bs[i]
		}
	}
	return nil
}

// Files 返回块里引用的全部附件（去重，按出现顺序）。
func Files(bs []Block) []File {
	var out []File
	seen := map[string]bool{}
	add := func(f *File) {
		if f != nil && f.ID != "" && !seen[f.ID] {
			seen[f.ID] = true
			out = append(out, *f)
		}
	}
	for _, b := range bs {
		add(b.Image)
		add(b.Before)
		add(b.After)
		for _, it := range b.Items {
			add(it.Image)
		}
	}
	return out
}

// Resolver 把块里写的图片引用（附件 ID）解析成附件；找不到时 ok = false。
type Resolver func(ref string) (f File, ok bool)

// Parse 宽松解析 Agent 交来的块列表（JSON 解出的 []any）。不合法的块丢掉，并在 warnings 里写清楚哪一个、为什么。
func Parse(raw []any, resolve Resolver) (out []Block, warnings []string) {
	p := parser{resolve: resolve}
	if len(raw) > MaxBlocks {
		p.warn(fmt.Sprintf("块最多 %d 个，后面 %d 个没有保存", MaxBlocks, len(raw)-MaxBlocks))
		raw = raw[:MaxBlocks]
	}
	ids := map[string]bool{}
	for i, r := range raw {
		p.at = fmt.Sprintf("第 %d 个块", i+1)
		m, ok := r.(map[string]any)
		if !ok {
			p.warn(p.at + "不是对象，没有保存")
			continue
		}
		b, ok := p.block(m)
		if !ok {
			continue
		}
		if b.ID == "" || ids[b.ID] {
			if b.ID != "" {
				p.warn(fmt.Sprintf("%s的 id「%s」重复，改成了自动编号", p.at, b.ID))
			}
			b.ID = fmt.Sprintf("b%d", i+1)
			for ids[b.ID] {
				b.ID += "x"
			}
		}
		ids[b.ID] = true
		out = append(out, b)
	}
	// 清单项的 see 要指向存在的块
	for bi := range out {
		for ii := range out[bi].Items {
			it := &out[bi].Items[ii]
			if it.See != "" && !ids[it.See] {
				p.warn(fmt.Sprintf("清单第 %d 项的 see「%s」找不到对应的块，已去掉", ii+1, it.See))
				it.See = ""
			}
		}
	}
	return out, p.warnings
}

// ParsePayload 解析证据的结构化部分。
func ParsePayload(verdict string, todo []string, details string, aside []string, raw []any, resolve Resolver) (*Payload, []string) {
	bs, warns := Parse(raw, resolve)
	p := &Payload{
		Verdict: clip(strings.TrimSpace(verdict), 200),
		Todo:    cleanList(todo),
		Blocks:  bs,
		Details: clip(strings.TrimSpace(details), MaxDetailsText),
		Aside:   cleanList(aside),
	}
	if p.Empty() {
		return nil, warns
	}
	return p, warns
}

// Decode 从数据库里存的 JSON 读回；空串或坏数据返回 nil。
func Decode[T any](s string) *T {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var v T
	if json.Unmarshal([]byte(s), &v) != nil {
		return nil
	}
	return &v
}

// Encode 存成 JSON；nil / 空返回空串。
func Encode(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" || string(b) == "[]" {
		return ""
	}
	return string(b)
}

type parser struct {
	resolve  Resolver
	at       string
	warnings []string
}

func (p *parser) warn(s string) { p.warnings = append(p.warnings, s) }

func (p *parser) block(m map[string]any) (Block, bool) {
	typ := strings.ToLower(str(m["type"]))
	b := Block{ID: str(m["id"]), Type: typ, Title: clip(str(m["title"]), 60), Caption: clip(str(m["caption"]), MaxText)}
	at := p.at
	if typ != "" {
		at += "（" + typ + "）"
	}
	switch typ {
	case "":
		p.warn(at + "缺少 type，没有保存。可用的类型：" + strings.Join(Types, "、"))
		return b, false
	case Checklist:
		items := list(m, "items", "checks")
		if len(items) > MaxChecks {
			p.warn(fmt.Sprintf("%s最多 %d 项，后面 %d 项没有保存", at, MaxChecks, len(items)-MaxChecks))
			items = items[:MaxChecks]
		}
		for j, x := range items {
			o, _ := x.(map[string]any)
			it := Item{Criterion: num(o["criterion"]), Text: clip(firstStr(o, "text", "title", "label"), 120), Why: clip(str(o["why"]), MaxText), See: str(o["see"])}
			if it.Text == "" && it.Criterion == 0 {
				p.warn(fmt.Sprintf("%s第 %d 项既没有 text 也没有 criterion，没有保存", at, j+1))
				continue
			}
			res, ok := result(str(o["result"]))
			if !ok {
				p.warn(fmt.Sprintf("%s第 %d 项的 result「%s」不认识（可用 pass / fail / ask），按 ask（要你看）保存", at, j+1, str(o["result"])))
			}
			it.Result = res
			b.Items = append(b.Items, it)
		}
		if len(b.Items) == 0 {
			p.warn(at + "没有有效的清单项，没有保存")
			return b, false
		}
	case Compare:
		b.Before = p.file(m["before"], at+"的 before")
		b.After = p.file(m["after"], at+"的 after")
		if b.Before == nil || b.After == nil {
			p.warn(at + "需要 before 和 after 两张图，没有保存")
			return b, false
		}
		// 改动框画在改后的图上（默认显示改后 + 框，点一下切到改前）
		b.After.Boxes = p.boxes(m["boxes"], at)
		b.After.Title = shotTitle(b)
		b.Before.Title = b.After.Title
	case Figure:
		v := m["image"]
		if v == nil {
			v = m["file"]
		}
		if v == nil {
			v = m["video"]
		}
		if b.Image = p.file(v, at+"的 image"); b.Image == nil {
			p.warn(at + "需要 image（截图或录屏），没有保存")
			return b, false
		}
		b.Image.Boxes = p.boxes(m["boxes"], at)
		b.Image.Title = shotTitle(b)
	case Flow:
		b.Mermaid = strings.TrimSpace(firstStr(m, "mermaid", "source", "code"))
		if b.Mermaid == "" {
			p.warn(at + "需要 mermaid 源码，没有保存")
			return b, false
		}
		if utf8.RuneCountInString(b.Mermaid) > MaxMermaid {
			p.warn(fmt.Sprintf("%s的 mermaid 超过 %d 字，没有保存：拆小或只画关键路径", at, MaxMermaid))
			return b, false
		}
	case Metrics:
		items := list(m, "items", "metrics")
		for j, x := range items {
			o, _ := x.(map[string]any)
			it := Item{Label: clip(firstStr(o, "label", "name"), 40), Value: clip(str(o["value"]), 24), Unit: clip(str(o["unit"]), 16), Note: clip(firstStr(o, "note", "desc"), 80)}
			if it.Label == "" || it.Value == "" {
				p.warn(fmt.Sprintf("%s第 %d 项需要 label 和 value，没有保存", at, j+1))
				continue
			}
			for _, t := range anyList(o["trend"]) {
				if f, ok := float(t); ok && len(it.Trend) < MaxTrend {
					it.Trend = append(it.Trend, f)
				}
			}
			b.Items = append(b.Items, it)
		}
		if len(b.Items) > MaxMetrics {
			p.warn(fmt.Sprintf("%s最多 %d 个指标，后面 %d 个没有保存", at, MaxMetrics, len(b.Items)-MaxMetrics))
			b.Items = b.Items[:MaxMetrics]
		}
		if len(b.Items) == 0 {
			p.warn(at + "没有有效的指标，没有保存")
			return b, false
		}
	case Scope:
		items := list(m, "items", "modules")
		for j, x := range items {
			o, _ := x.(map[string]any)
			it := Item{Label: clip(firstStr(o, "module", "label", "name"), 30), Count: num(o["count"]), Note: clip(str(o["note"]), 80)}
			if it.Label == "" {
				p.warn(fmt.Sprintf("%s第 %d 项缺少 module，没有保存", at, j+1))
				continue
			}
			if it.Count < 0 {
				it.Count = 0
			}
			b.Items = append(b.Items, it)
		}
		if len(b.Items) > MaxScope {
			p.warn(fmt.Sprintf("%s最多 %d 个模块，后面 %d 个没有保存", at, MaxScope, len(b.Items)-MaxScope))
			b.Items = b.Items[:MaxScope]
		}
		if len(b.Items) == 0 {
			p.warn(at + "没有有效的模块，没有保存")
			return b, false
		}
	case Options:
		items := list(m, "items", "options")
		for j, x := range items {
			o, _ := x.(map[string]any)
			it := Item{Label: clip(firstStr(o, "label", "title", "name"), 40), Desc: clip(firstStr(o, "desc", "description"), 120), Recommended: truthy(o["recommended"])}
			if it.Label == "" {
				p.warn(fmt.Sprintf("%s第 %d 项缺少 label，没有保存", at, j+1))
				continue
			}
			if v := o["image"]; v != nil && str(v) != "" {
				it.Image = p.file(v, fmt.Sprintf("%s第 %d 项的 image", at, j+1))
			}
			b.Items = append(b.Items, it)
		}
		if len(b.Items) > MaxOptions {
			p.warn(fmt.Sprintf("%s最多 %d 个选项，后面 %d 个没有保存", at, MaxOptions, len(b.Items)-MaxOptions))
			b.Items = b.Items[:MaxOptions]
		}
		if len(b.Items) < 2 {
			p.warn(at + "至少要 2 个有效选项，没有保存")
			return b, false
		}
	case Table:
		for _, c := range anyList(m["columns"]) {
			b.Columns = append(b.Columns, clip(str(c), 40))
		}
		if len(b.Columns) == 0 {
			p.warn(at + "需要 columns（表头），没有保存")
			return b, false
		}
		if len(b.Columns) > MaxColumns {
			p.warn(fmt.Sprintf("%s最多 %d 列，后面 %d 列没有保存", at, MaxColumns, len(b.Columns)-MaxColumns))
			b.Columns = b.Columns[:MaxColumns]
		}
		rows := anyList(m["rows"])
		if len(rows) > MaxRows {
			p.warn(fmt.Sprintf("%s最多 %d 行，后面 %d 行没有保存", at, MaxRows, len(rows)-MaxRows))
			rows = rows[:MaxRows]
		}
		for _, r := range rows {
			cells := anyList(r)
			row := make([]string, len(b.Columns))
			for k := range row {
				if k < len(cells) {
					row[k] = clip(str(cells[k]), MaxText)
				}
			}
			b.Rows = append(b.Rows, row)
		}
		if len(b.Rows) == 0 {
			p.warn(at + "没有行，没有保存")
			return b, false
		}
	default:
		p.warn(fmt.Sprintf("%s的类型「%s」不认识，没有保存。可用的类型：%s", p.at, typ, strings.Join(Types, "、")))
		return b, false
	}
	return b, true
}

// shotTitle：图片自己的标题（看大图时显示），取块的 title，没有时取 caption。
func shotTitle(b Block) string {
	if b.Title != "" {
		return b.Title
	}
	return clip(b.Caption, MaxTitle)
}

// file 解析一个图片引用：字符串（附件 ID）或 {id: ...}。
func (p *parser) file(v any, what string) *File {
	ref := str(v)
	if o, ok := v.(map[string]any); ok {
		ref = str(o["id"])
	}
	if ref == "" {
		return nil
	}
	if p.resolve != nil {
		if f, ok := p.resolve(ref); ok {
			return &f
		}
	}
	p.warn(fmt.Sprintf("%s「%s」找不到（MCP / ks 命令会自动上传本机路径；直接调接口要先上传到 /api/agent/files，填附件 ID）", what, ref))
	return nil
}

// ImageRefs 列出块里所有图片字段的当前值（MCP / ks 命令用来找出要上传的本机路径）。
// set 把某个字段改成新值（上传后的附件 ID）。
func ImageRefs(raw []any, visit func(ref string, set func(string))) {
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		for _, k := range []string{"image", "file", "video", "before", "after"} {
			if s, ok := m[k].(string); ok && s != "" {
				visit(s, func(v string) { m[k] = v })
			}
		}
		for _, x := range list(m, "items", "options") {
			if o, ok := x.(map[string]any); ok {
				if s, ok := o["image"].(string); ok && s != "" {
					visit(s, func(v string) { o["image"] = v })
				}
			}
		}
	}
}

func result(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pass", "ok", "passed", "yes", "true", "通过", "达标":
		return Pass, true
	case "fail", "failed", "no", "false", "未通过", "未达标", "不通过":
		return Fail, true
	case "ask", "manual", "check", "需你确认", "要你看", "待确认":
		return Ask, true
	}
	return Ask, false
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case bool:
		return strconv.FormatBool(x)
	case json.Number:
		return x.String()
	}
	return ""
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(m[k]); s != "" {
			return s
		}
	}
	return ""
}

func num(v any) int {
	f, ok := float(v)
	if !ok {
		return 0
	}
	return int(math.Round(f))
}

func float(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "yes" || x == "推荐"
	}
	return false
}

func anyList(v any) []any {
	l, _ := v.([]any)
	return l
}

func list(m map[string]any, keys ...string) []any {
	for _, k := range keys {
		if l := anyList(m[k]); len(l) > 0 {
			return l
		}
	}
	return nil
}

func cleanList(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && len(out) < MaxListItems {
			out = append(out, clip(s, MaxText))
		}
	}
	return out
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

var resultWord = map[string]string{Pass: "达标", Fail: "未达标", Ask: "要你看"}

// Text 把结构化证据拼成纯文本：没写 summary 时用作证据正文，旧界面、通知和 req_get 都读它。
func (p *Payload) Text() string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	line := func(s string) {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(s)
	}
	if p.Verdict != "" {
		line(p.Verdict)
	}
	if c := p.Checklist(); c != nil {
		for _, it := range c.Items {
			label := it.Text
			if label == "" {
				label = fmt.Sprintf("验收标准 %d", it.Criterion)
			}
			s := fmt.Sprintf("- %s：%s", label, resultWord[it.Result])
			if it.Why != "" {
				s += "，" + it.Why
			}
			line(s)
		}
	}
	for _, t := range p.Todo {
		line("要你做的：" + t)
	}
	for _, a := range p.Aside {
		line("顺带改动：" + a)
	}
	if p.Details != "" {
		line("实现细节：" + p.Details)
	}
	return b.String()
}

// Score 统计清单：达标数与总数。
func (p *Payload) Score() (pass, total int) {
	if c := p.Checklist(); c != nil {
		for _, it := range c.Items {
			total++
			if it.Result == Pass {
				pass++
			}
		}
	}
	return
}

// Localize 把块里写成本机路径的图片上传，换成附件 ID（MCP 与 ks 命令共用）。
// 不是本机文件的值（已经是附件 ID）原样留给 hub 解析；同一路径只上传一次，cache 可与 images 共用。
func Localize(raw []any, cache map[string]string, upload func(path string) (string, error)) error {
	var err error
	ImageRefs(raw, func(ref string, set func(string)) {
		if err != nil {
			return
		}
		if id, ok := cache[ref]; ok {
			set(id)
			return
		}
		if st, e := os.Stat(ref); e != nil || st.IsDir() {
			return
		}
		id, e := upload(ref)
		if e != nil {
			err = e
			return
		}
		cache[ref] = id
		set(id)
	})
	return err
}

// FromMaps 把 MCP 收到的 []map[string]any 转成 Parse / Localize 用的 []any。
func FromMaps(in []map[string]any) []any {
	out := make([]any, len(in))
	for i, m := range in {
		out[i] = m
	}
	return out
}

// PointsToRaw 把决策里“要拍板的点”（[{question, options:[{label, desc, recommended, image}]}]）转成 options 块（R-112），
// 之后与其它块一样走 Parse / Localize：每个点就是一个标题为问题的 options 块。
func PointsToRaw(points []any) []any {
	out := make([]any, 0, len(points))
	for _, p := range points {
		m, ok := p.(map[string]any)
		if !ok {
			out = append(out, p)
			continue
		}
		b := map[string]any{"type": Options, "title": firstStr(m, "question", "title")}
		if id := str(m["id"]); id != "" {
			b["id"] = id
		}
		b["items"] = list(m, "options", "items")
		out = append(out, b)
	}
	return out
}

// ParsePoints 解析要拍板的点：每个点要有问题和至少 2 个选项；不合法的点丢掉并说明。
func ParsePoints(points []any, resolve Resolver) (out []Block, warnings []string) {
	bs, warns := Parse(PointsToRaw(points), resolve)
	for _, b := range bs {
		if strings.TrimSpace(b.Title) == "" {
			var labels []string
			for _, it := range b.Items {
				labels = append(labels, it.Label)
			}
			warns = append(warns, fmt.Sprintf("选项为「%s」的要拍板的点缺少 question，没有保存", strings.Join(labels, " / ")))
			continue
		}
		out = append(out, b)
	}
	for i := range warns {
		warns[i] = strings.ReplaceAll(strings.ReplaceAll(warns[i], "个块（options）", "个要拍板的点"), "个块", "个要拍板的点")
	}
	return out, warns
}
