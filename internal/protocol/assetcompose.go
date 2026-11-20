package protocol

// 3D 查看器的组合预览（R-407）：asset.json 的 display 里可选写挂件（attach）和比例参照（refs）。
// 推送端（internal/assetpush）按这里校验格式，Hub 按这里解析版本 meta 里的配置、找到对应素材的 GLB，网页（web/src/lib/compose.ts）照同样的规则画。
//
//	"display": {
//	  "attach": [{"socket": "crate", "asset": "core:prop.supply_crate", "variant": "empty", "clips": ["carry_idle", "carry_walk"], "name": "cargo"}],
//	  "refs":   [{"asset": "core:char.soldier", "offset": [1.2, 0, 0]}]
//	}
//
// 挂件的 name 是它在这件素材里的名字（不写时取 socket），动作事件（R-408）按它引用挂件，所以同一件素材里不能重名。
//
// 动作事件（R-408）：display.events 按动作名列出这个动作里某一时刻发生的事——挂上 / 摘下挂件、播音效素材、在挂点闪一个特效标记：
//
//	"events": {"load": [{"t": 1.8, "do": "attach", "name": "cargo"}, {"t": 2.6, "do": "sound", "asset": "core:sfx.latch"},
//	                    {"t": 2.6, "do": "mark", "socket": "crate", "label": "卡扣"}]}

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// DisplayFields 是 asset.json 的 display 下 Hub / 网页会读的全部字段。新增字段先加在这里，
// 一致性测试（assetpush 的 helpcover_test）会要求 ks asset help（含 compose 子页）里写明它，并扫描代码里有没有没登记的 display.<字段>。
var DisplayFields = []string{"name", "animations", "attach", "refs", "events"}

// ComposeAttach 是一个挂件：挂到 GLB 里名为 Socket 的节点（骨骼或空节点）上。
type ComposeAttach struct {
	Name    string   `json:"name,omitempty" doc:"挂件名（不写时取 socket）；动作事件按它引用"`
	Socket  string   `json:"socket" doc:"挂点：GLB 里的骨骼或空节点名"`
	Asset   string   `json:"asset" doc:"挂上去的素材 id"`
	Variant string   `json:"variant,omitempty" doc:"素材的变体名，不写用素材本身"`
	Clips   []string `json:"clips,omitempty" doc:"只在这些动作里显示；不写时一直显示"`
}

// ComposeRef 是放在旁边的比例参照。
type ComposeRef struct {
	Asset   string    `json:"asset" doc:"参照素材 id"`
	Variant string    `json:"variant,omitempty"`
	Offset  []float64 `json:"offset,omitempty" doc:"相对本素材原点的位置（米，x / y / z）；不写时并排放在右侧、底面对齐"`
}

// 动作事件的种类（R-408）：只做这四种，不支持自定义。
const (
	EventAttach = "attach" // 挂上挂件
	EventDetach = "detach" // 摘下挂件（放到挂点正下方的地面上）
	EventSound  = "sound"  // 播音效素材
	EventMark   = "mark"   // 在挂点闪一个特效标记
)

// ComposeEvent 是动作里某一时刻的一个事件。
type ComposeEvent struct {
	T      float64 `json:"t" doc:"第几秒（从动作开头算）"`
	Do     string  `json:"do" enum:"attach,detach,sound,mark" doc:"attach 挂上挂件 / detach 摘下挂件 / sound 播音效 / mark 特效位置"`
	Name   string  `json:"name,omitempty" doc:"attach / detach：挂件名（display.attach 的 name，不写 name 的挂件用 socket）"`
	Asset  string  `json:"asset,omitempty" doc:"sound：音效素材 id"`
	Socket string  `json:"socket,omitempty" doc:"mark：在哪个挂点闪标记"`
	Label  string  `json:"label,omitempty" doc:"mark：标签，一句话（如 枪口火光）"`
}

// MaxEventLabel 是特效标记标签的字数上限。
const MaxEventLabel = 24

// Compose 是一件素材的组合预览配置。
type Compose struct {
	Attach []ComposeAttach           `json:"attach,omitempty"`
	Refs   []ComposeRef              `json:"refs,omitempty"`
	Events map[string][]ComposeEvent `json:"events,omitempty"` // 动作名 → 事件（按时间排好）
}

// Empty：没有挂件、参照，也没有动作事件。
func (c Compose) Empty() bool { return len(c.Attach) == 0 && len(c.Refs) == 0 && len(c.Events) == 0 }

// Sounds 是事件里引用到的音效素材（「素材 id」或「素材 id@变体名」，去重，按出现顺序）。
func (c Compose) Sounds() []string {
	var out []string
	seen := map[string]bool{}
	for _, clip := range c.EventClips() {
		for _, e := range c.Events[clip] {
			if e.Do == EventSound && !seen[e.Asset] {
				seen[e.Asset] = true
				out = append(out, e.Asset)
			}
		}
	}
	return out
}

// EventClips 是写了事件的动作名（排好序，输出稳定）。
func (c Compose) EventClips() []string {
	out := make([]string, 0, len(c.Events))
	for k := range c.Events {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Key 是挂件名：写了 name 用 name，否则用 socket。
func (a ComposeAttach) Key() string {
	if a.Name != "" {
		return a.Name
	}
	return a.Socket
}

// splitVariant 认「素材 id@变体名」的写法。
func splitVariant(asset, variant string) (string, string) {
	if id, v, ok := strings.Cut(asset, VariantSep); ok && variant == "" {
		return id, v
	}
	return asset, variant
}

// ComposeTarget 是挂件 / 参照指向的素材 id（有变体时是「素材 id@变体名」）。
func ComposeTarget(asset, variant string) string {
	id, v := splitVariant(strings.TrimSpace(asset), strings.TrimSpace(variant))
	if v == "" {
		return id
	}
	return VariantKey(id, v)
}

// ParseCompose 读 display 对象里的 attach、refs；errs 是格式问题（推送端当作错误）。display 为空或没写这两项时返回空配置。
func ParseCompose(display json.RawMessage) (Compose, []string) {
	var c Compose
	if len(display) == 0 || string(display) == "null" {
		return c, nil
	}
	var d struct {
		Attach json.RawMessage `json:"attach"`
		Refs   json.RawMessage `json:"refs"`
		Events json.RawMessage `json:"events"`
	}
	if json.Unmarshal(display, &d) != nil {
		return c, nil // display 本身不是对象由 display.name 的校验报
	}
	var errs []string
	seen := map[string]bool{}
	if len(d.Attach) > 0 && string(d.Attach) != "null" {
		var raw []json.RawMessage
		if json.Unmarshal(d.Attach, &raw) != nil {
			errs = append(errs, `display.attach 要写成数组，如 [{"socket": "crate", "asset": "core:prop.crate"}]`)
		}
		for i, r := range raw {
			at := fmt.Sprintf("display.attach 第 %d 项", i+1)
			var a ComposeAttach
			if err := strictObj(r, &a, "name", "socket", "asset", "variant", "clips"); err != "" {
				errs = append(errs, at+err)
				continue
			}
			a.Name, a.Socket = strings.TrimSpace(a.Name), strings.TrimSpace(a.Socket)
			a.Asset, a.Variant = splitVariant(strings.TrimSpace(a.Asset), strings.TrimSpace(a.Variant))
			bad := false
			if a.Socket == "" {
				errs, bad = append(errs, at+"没写 socket（挂点名）"), true
			}
			if msg := checkTarget(a.Asset, a.Variant); msg != "" {
				errs, bad = append(errs, at+msg), true
			}
			clips := a.Clips[:0]
			for _, c := range a.Clips {
				if c = strings.TrimSpace(c); c != "" {
					clips = append(clips, c)
				}
			}
			a.Clips = clips
			if !bad && seen[a.Key()] {
				errs, bad = append(errs, at+"的挂件名「"+a.Key()+"」重复了，同一挂点挂多件时用 name 区分"), true
			}
			if bad {
				continue
			}
			seen[a.Key()] = true
			c.Attach = append(c.Attach, a)
		}
	}
	if len(d.Refs) > 0 && string(d.Refs) != "null" {
		var raw []json.RawMessage
		if json.Unmarshal(d.Refs, &raw) != nil {
			errs = append(errs, `display.refs 要写成数组，如 [{"asset": "core:char.soldier"}]`)
		}
		for i, r := range raw {
			at := fmt.Sprintf("display.refs 第 %d 项", i+1)
			var x ComposeRef
			if err := strictObj(r, &x, "asset", "variant", "offset"); err != "" {
				errs = append(errs, at+err)
				continue
			}
			x.Asset, x.Variant = splitVariant(strings.TrimSpace(x.Asset), strings.TrimSpace(x.Variant))
			if msg := checkTarget(x.Asset, x.Variant); msg != "" {
				errs = append(errs, at+msg)
				continue
			}
			if x.Offset != nil && len(x.Offset) != 3 {
				errs = append(errs, at+"的 offset 要写成 [x, y, z]（米）")
				continue
			}
			c.Refs = append(c.Refs, x)
		}
	}
	if len(d.Events) > 0 && string(d.Events) != "null" {
		c.Events, errs = parseEvents(d.Events, errs)
	}
	return c, errs
}

// parseEvents 读 display.events：对象，键是动作名，值是事件数组。格式不对的项记错误并跳过；
// 挂件名、挂点、音效素材在不在由调用方核对（只提示）。
func parseEvents(raw json.RawMessage, errs []string) (map[string][]ComposeEvent, []string) {
	const eg = `display.events 要写成对象：键是动作名，值是事件数组，如 {"load": [{"t": 1.8, "do": "attach", "name": "cargo"}]}`
	var byClip map[string]json.RawMessage
	if json.Unmarshal(raw, &byClip) != nil {
		return nil, append(errs, eg)
	}
	out := map[string][]ComposeEvent{}
	clips := make([]string, 0, len(byClip))
	for k := range byClip {
		clips = append(clips, k)
	}
	sort.Strings(clips)
	for _, clip := range clips {
		name := strings.TrimSpace(clip)
		if name == "" {
			errs = append(errs, "display.events 里有一个动作名是空的")
			continue
		}
		var list []json.RawMessage
		if json.Unmarshal(byClip[clip], &list) != nil {
			errs = append(errs, "display.events 的「"+name+"」要写成事件数组")
			continue
		}
		for i, r := range list {
			at := fmt.Sprintf("display.events 的「%s」第 %d 项", name, i+1)
			var e ComposeEvent
			if err := strictObj(r, &e, "t", "do", "name", "asset", "socket", "label"); err != "" {
				errs = append(errs, at+err)
				continue
			}
			var has struct {
				T *float64 `json:"t"`
			}
			_ = json.Unmarshal(r, &has)
			e.Do, e.Name, e.Socket, e.Label = strings.TrimSpace(e.Do), strings.TrimSpace(e.Name), strings.TrimSpace(e.Socket), strings.TrimSpace(e.Label)
			var msg string
			switch {
			case has.T == nil:
				msg = "没写 t（第几秒）"
			case e.T < 0 || math.IsNaN(e.T) || math.IsInf(e.T, 0):
				msg = "的 t 不能是负数"
			}
			if msg == "" {
				switch e.Do {
				case EventAttach, EventDetach:
					if e.Name == "" {
						msg = "没写 name（挂件名）"
					}
				case EventSound:
					id, v := splitVariant(strings.TrimSpace(e.Asset), "")
					if m := checkTarget(id, v); m != "" {
						msg = m
					}
					e.Asset = ComposeTarget(id, v)
				case EventMark:
					if e.Socket == "" {
						msg = "没写 socket（在哪个挂点闪标记）"
					} else if strings.ContainsAny(e.Label, "\r\n") || utf8.RuneCountInString(e.Label) > MaxEventLabel {
						msg = fmt.Sprintf("的 label 要写成一行、最多 %d 字", MaxEventLabel)
					}
				case "":
					msg = "没写 do（attach / detach / sound / mark）"
				default:
					msg = "的 do「" + e.Do + "」不认识（可写 attach / detach / sound / mark）"
				}
			}
			if msg != "" {
				errs = append(errs, at+msg)
				continue
			}
			out[name] = append(out[name], e)
		}
		sort.SliceStable(out[name], func(i, j int) bool { return out[name][i].T < out[name][j].T })
	}
	if len(out) == 0 {
		return nil, errs
	}
	return out, errs
}

// MetaCompose 从版本的 meta（项目字段，含 display）里读组合预览配置，格式不对的项跳过。
func MetaCompose(meta json.RawMessage) Compose {
	var m struct {
		Display json.RawMessage `json:"display"`
	}
	if len(meta) == 0 || json.Unmarshal(meta, &m) != nil {
		return Compose{}
	}
	c, _ := ParseCompose(m.Display)
	return c
}

func checkTarget(asset, variant string) string {
	if asset == "" {
		return "没写 asset（素材 id）"
	}
	if msg := CheckAssetID(asset); msg != "" {
		return "的 asset「" + asset + "」" + msg
	}
	if variant != "" {
		if msg := CheckVariantName(variant); msg != "" {
			return "的变体名「" + variant + "」" + msg
		}
	}
	return ""
}

// strictObj 按字段解析一项：要是对象，字段类型要对，不认识的字段报出来（多半是拼错了）。
func strictObj(raw json.RawMessage, dst any, keys ...string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return "要写成对象"
	}
	for k := range m {
		ok := false
		for _, x := range keys {
			ok = ok || k == x
		}
		if !ok {
			return "有不认识的字段「" + k + "」（可写 " + strings.Join(keys, "、") + "）"
		}
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		var te *json.UnmarshalTypeError
		if ok := asTypeErr(err, &te); ok && te.Field != "" {
			return "的 " + te.Field + " 类型不对"
		}
		return "格式不对"
	}
	return ""
}

func asTypeErr(err error, te **json.UnmarshalTypeError) bool {
	t, ok := err.(*json.UnmarshalTypeError)
	if ok {
		*te = t
	}
	return ok
}

// socketRe：名字里带挂点通用词的节点（与 web/src/lib/compose.ts 的 SOCKET_RE 一致）。不按 crate、weapon 这类具体名字猜，各引擎命名不一。
var socketRe = regexp.MustCompile(`(?i)(socket|attach|mount|slot|hardpoint|anchor|hand|grip|muzzle|holster|^s_|^sk_)`)

// IsSocketName：节点名带不带挂点通用词。
func IsSocketName(name string) bool { return socketRe.MatchString(name) }

// GLBNode 是 GLB 里一个不带网格的节点（骨骼或空节点）。
type GLBNode struct {
	Name  string
	Bone  bool // 是蒙皮的骨骼
	Empty bool // 空节点：不是骨骼，自己和下面都没有网格
}

// SocketCandidate：默认列出的挂点——空节点、名字带通用词的节点，或 asset.json 里引用到的（refs）。其余要在查看器里切「全部节点」才列出。
func SocketCandidate(n GLBNode, refs map[string]bool) bool {
	return n.Empty || IsSocketName(n.Name) || refs[n.Name]
}
