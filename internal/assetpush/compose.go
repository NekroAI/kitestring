package assetpush

// 组合预览（R-407）的推送端：校验 display.attach / display.refs 的格式（不对算错），
// 读 GLB 的节点和动作，挂点名、动作名对不上只提示、不拦推送；推送返回里一行列出 GLB 里找到的挂点名。

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// glbInfo 是从 GLB 的 JSON 块里读出的节点和动作名。
type glbInfo struct {
	Nodes []protocol.GLBNode
	Clips []string
}

func (g glbInfo) has(name string) bool {
	for _, n := range g.Nodes {
		if n.Name == name {
			return true
		}
	}
	return false
}

// sockets 是默认列出的挂点名（与查看器默认列表一致）。
func (g glbInfo) sockets(refs map[string]bool) []string {
	var out []string
	for _, n := range g.Nodes {
		if n.Name != "" && protocol.SocketCandidate(n, refs) && !contains(out, n.Name) {
			out = append(out, n.Name)
		}
	}
	return out
}

// readGLB 读 GLB 的 JSON 块（只读文件头和 JSON，不读二进制块）。
func readGLB(path string) (glbInfo, error) {
	var g glbInfo
	f, err := os.Open(path)
	if err != nil {
		return g, err
	}
	defer f.Close()
	var head [20]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return g, err
	}
	if string(head[:4]) != "glTF" || string(head[16:20]) != "JSON" {
		return g, errors.New("不是 GLB")
	}
	n := binary.LittleEndian.Uint32(head[12:16])
	if n > 64<<20 {
		return g, errors.New("GLB 的 JSON 块太大")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return g, err
	}
	var doc struct {
		Nodes []struct {
			Name     string `json:"name"`
			Mesh     *int   `json:"mesh"`
			Children []int  `json:"children"`
		} `json:"nodes"`
		Skins []struct {
			Joints []int `json:"joints"`
		} `json:"skins"`
		Animations []struct {
			Name string `json:"name"`
		} `json:"animations"`
	}
	if err := json.Unmarshal(buf, &doc); err != nil {
		return g, err
	}
	joint := map[int]bool{}
	for _, s := range doc.Skins {
		for _, j := range s.Joints {
			joint[j] = true
		}
	}
	// 自己或下面有没有网格（防环：glTF 不允许，坏文件也不死循环）
	memo := map[int]int{} // 0 未算 1 算中 2 无 3 有
	var meshy func(i int) bool
	meshy = func(i int) bool {
		if i < 0 || i >= len(doc.Nodes) {
			return false
		}
		switch memo[i] {
		case 1, 2:
			return false
		case 3:
			return true
		}
		memo[i] = 1
		has := doc.Nodes[i].Mesh != nil
		for _, c := range doc.Nodes[i].Children {
			has = meshy(c) || has
		}
		memo[i] = 2
		if has {
			memo[i] = 3
		}
		return has
	}
	for i, nd := range doc.Nodes {
		if nd.Mesh != nil {
			continue
		}
		g.Nodes = append(g.Nodes, protocol.GLBNode{Name: nd.Name, Bone: joint[i], Empty: !joint[i] && !meshy(i)})
	}
	for _, a := range doc.Animations {
		g.Clips = append(g.Clips, a.Name)
	}
	return g, nil
}

// checkCompose 校验一件素材的组合预览配置：格式不对记错误；GLB 里没有的挂点、动作，以及挂到图片、音效上的，记提示。
// 返回的提示句子按素材情况追加（hints），挂点那一句只在 GLB 里有挂点或写了挂件时给。
func checkCompose(c *Check, display json.RawMessage, kind, file string) protocol.Compose {
	comp, errs := protocol.ParseCompose(display)
	c.Errors = append(c.Errors, errs...)
	if kind != protocol.AssetPart {
		if len(comp.Attach) > 0 {
			c.Warnings = append(c.Warnings, "display.attach 只对 GLB 零件生效，这件素材不是 GLB，挂件不会显示")
		}
		return comp
	}
	g, err := readGLB(file)
	c.clips = g.Clips
	var d struct {
		Animations json.RawMessage `json:"animations"`
	}
	_ = json.Unmarshal(display, &d)
	c.animsText = len(d.Animations) > 0 && string(d.Animations) != "null"
	if err != nil {
		if !comp.Empty() {
			c.Warnings = append(c.Warnings, "读不出 GLB 的节点，挂点名没有核对："+err.Error())
		}
		return comp
	}
	refs := map[string]bool{}
	for _, a := range comp.Attach {
		refs[a.Socket] = true
		if !g.has(a.Socket) {
			c.Warnings = append(c.Warnings, "挂件「"+a.Key()+"」的挂点「"+a.Socket+"」在 GLB 里找不到，查看器里会提示；照常推送")
		}
		for _, cl := range a.Clips {
			if !contains(g.Clips, cl) {
				c.Warnings = append(c.Warnings, "挂件「"+a.Key()+"」的 clips 里的动作「"+cl+"」在 GLB 里没有")
			}
		}
	}
	c.Sockets = g.sockets(refs)
	return comp
}

// hint 是按素材情况追加的一句说明（校验报告里每件素材下面「说明：」一行）。
// 加新的说明时在 hints 里按条件追加，别写死在报告格式里（R-409 沿用）。
type hint func(c Check) string

var hints = []hint{
	// R-407：GLB 里找到的挂点名，方便照着写 display.attach
	func(c Check) string {
		if len(c.Sockets) == 0 {
			return ""
		}
		return "GLB 里的挂点：" + strings.Join(c.Sockets, "、") + "（挂件写法见 ks help asset compose）"
	},
	// R-409：GLB 里有动作但 asset.json 没写 display.animations（写了、或没有动作都不提示）
	func(c Check) string {
		if len(c.clips) == 0 || c.animsText {
			return ""
		}
		return fmt.Sprintf("网页会列出并分别播放这 %d 个动作；循环标记、显示名可写 display.animations（见 ks asset help）", len(c.clips))
	},
}

// hintsOf 是一件素材的全部说明。
func hintsOf(c Check) []string {
	var out []string
	for _, h := range hints {
		if s := h(c); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ComposeHelp 是 ks help asset compose（也是 ks asset help compose）：组合预览的写法（R-407）。
// R-408 的动作事件会在这一页接着写，按挂件名（name，不写时是 socket）引用挂件。整页控制在约 60 行内。
const ComposeHelp = `组合预览（3D 查看器）：asset.json 的 display 里可选写挂件和比例参照，用户打开素材就看到组合效果。
可选：没有运行时挂件、也不需要比例参照就别写。纸鸢不做编辑，配置只来自 asset.json。

display.attach：把别的素材挂到本素材 GLB 的挂点上（箱子、枪、盔甲部件这类运行时才挂的东西）。每项：
  socket   必填：挂点名，GLB 里的骨骼或空节点名。推送返回的「GLB 里的挂点」一行列出找到的节点，照着填
  asset    必填：挂上去的素材 id（同项目里已推送的 GLB 素材）；也可写「素材 id@变体名」
  variant  可选：变体名，不写用素材本身
  clips    可选：只在这些动作里显示（如驮箱动作），切到别的动作或静止姿势时隐藏；不写时一直显示
  name     可选：挂件名，不写时取 socket；同一挂点挂多件时必须写，动作事件按它引用
display.refs：放在旁边作比例参照的素材（人物、标准箱……），默认显示。每项：
  asset    必填：素材 id（也可写「素材 id@变体名」）
  variant  可选：变体名
  offset   可选：[x, y, z]（米），相对本素材原点；不写时并排放在右侧、底面对齐

完整例子（机械狗驮箱，旁边放一个士兵看比例）：
{
  "id": "core:vehicle.robodog_loader",
  "category": "vehicle",
  "file": "robodog_loader.glb",
  "display": {
    "name": "自装卸运输机械狗",
    "animations": [{"name": "idle", "loop": true}, {"name": "carry_idle", "loop": true}, {"name": "carry_walk", "loop": true}],
    "attach": [
      {"socket": "crate", "asset": "core:prop.supply_crate", "variant": "empty", "clips": ["carry_idle", "carry_walk"], "name": "cargo"}
    ],
    "refs": [
      {"asset": "core:char.soldier"},
      {"asset": "core:prop.standard_crate", "offset": [0, 0, 1.2]}
    ]
  }
}

挂件跟着挂点的位置、朝向、缩放随动作走；挂件素材的原点就是对齐点（箱子的原点放在箱底中心）。
网页上：查看器的「挂点」列出挂点并画坐标轴；「挂件」「参照」开关可临时全部摘掉再挂上。
格式不对（不是数组、缺 socket / asset、素材 id 不合格、挂件名重复、写了不认识的字段）算错误，整批不推；
挂点名、clips 里的动作在 GLB 里找不到只提示、照常推送，查看器里也会标出来；被挂的素材还没推送时查看器里提示找不到。
改了 attach / refs 重推记为新的一版。自动缩略图不带挂件。
`
