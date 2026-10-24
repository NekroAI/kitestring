package blocks

// 截图的标题与改动框（R-183）：每张图带一句话标题，可以附若干矩形框标出这次改了哪里，网页画在图上、可一键隐藏。
// 框用图片原始尺寸的比例坐标（0～1，左上角为原点），截图怎么缩放都对得上。写错的框（格式不对、越界）丢掉并提示，证据照常保存。

import (
	"fmt"
	"math"
	"os"
	"strings"
)

// Box 是截图上的一个改动框。
type Box struct {
	X     float64 `json:"x" doc:"左边到图片左边的距离 / 图片宽（0～1）"`
	Y     float64 `json:"y" doc:"上边到图片顶部的距离 / 图片高（0～1）"`
	W     float64 `json:"w" doc:"框宽 / 图片宽（0～1）"`
	H     float64 `json:"h" doc:"框高 / 图片高（0～1）"`
	Label string  `json:"label,omitempty" doc:"一句标签，如：这里改成标题优先"`
}

const (
	MaxBoxes = 8
	MaxTitle = 40 // 截图标题
	MaxLabel = 40 // 改动框标签
	MaxShots = 40 // evidence 的 images 一次最多几张
	boxSlack = 0.01
)

// parseBoxes 宽松解析一组框：[{x, y, w, h, label}]，也接受 [x, y, w, h, label?]、left / top / width / height。
func (p *parser) boxes(v any, at string) []Box {
	if v == nil {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		if m, isMap := v.(map[string]any); isMap {
			raw = []any{m}
		} else {
			p.warn(at + "的 boxes 要是数组 [{x, y, w, h, label}]，已忽略")
			return nil
		}
	}
	if len(raw) > MaxBoxes {
		p.warn(fmt.Sprintf("%s的改动框最多 %d 个，后面 %d 个没有保存", at, MaxBoxes, len(raw)-MaxBoxes))
		raw = raw[:MaxBoxes]
	}
	var out []Box
	for i, x := range raw {
		where := fmt.Sprintf("%s的第 %d 个改动框", at, i+1)
		var vals [4]float64
		var label string
		okAll := true
		switch o := x.(type) {
		case map[string]any:
			keys := [4][]string{{"x", "left"}, {"y", "top"}, {"w", "width"}, {"h", "height"}}
			for k, names := range keys {
				var got bool
				for _, n := range names {
					if f, ok := float(o[n]); ok {
						vals[k], got = f, true
						break
					}
				}
				okAll = okAll && got
			}
			label = firstStr(o, "label", "text", "note")
		case []any:
			if len(o) < 4 {
				okAll = false
				break
			}
			for k := 0; k < 4; k++ {
				f, ok := float(o[k])
				vals[k] = f
				okAll = okAll && ok
			}
			if len(o) > 4 {
				label = str(o[4])
			}
		default:
			okAll = false
		}
		if !okAll {
			p.warn(where + "格式不对，要写成 {x, y, w, h, label}（0～1 的比例），没有保存")
			continue
		}
		b, msg := checkBox(vals)
		if msg != "" {
			p.warn(where + msg + "，没有保存")
			continue
		}
		b.Label = clip(strings.TrimSpace(label), MaxLabel)
		out = append(out, b)
	}
	return out
}

// checkBox 检查一个框的坐标：都要在 0～1 之内（右边、下边允许出界一点点，截到边上），宽高要大于 0。
func checkBox(v [4]float64) (Box, string) {
	x, y, w, h := v[0], v[1], v[2], v[3]
	if x > 1 || y > 1 || w > 1 || h > 1 {
		return Box{}, fmt.Sprintf("（%g, %g, %g, %g）像是像素坐标：要换算成图片尺寸的比例（x / 图片宽、y / 图片高，0～1）", x, y, w, h)
	}
	if w <= 0 || h <= 0 {
		return Box{}, "的宽或高不是正数"
	}
	if x < -boxSlack || y < -boxSlack || x+w > 1+boxSlack || y+h > 1+boxSlack {
		return Box{}, fmt.Sprintf("超出图片范围（x %g + w %g、y %g + h %g 要在 0～1 之内）", x, w, y, h)
	}
	x, y = math.Max(0, x), math.Max(0, y)
	w, h = math.Min(w, 1-x), math.Min(h, 1-y)
	return Box{X: round4(x), Y: round4(y), W: round4(w), H: round4(h)}, ""
}

func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

// ParseBoxes 解析一组框（hub 之外的地方用，例如测试）。
func ParseBoxes(v any, at string) ([]Box, []string) {
	p := parser{}
	bs := p.boxes(v, at)
	return bs, p.warnings
}

// ParseImages 解析证据的 images：每项是附件 ID，或 {id, title, boxes}。找不到的图丢掉，写错的框丢掉，都写进提示。
func ParseImages(raw []any, resolve Resolver) (out []File, warnings []string) {
	p := parser{resolve: resolve}
	if len(raw) > MaxShots {
		p.warn(fmt.Sprintf("images 最多 %d 张，后面 %d 张没有保存", MaxShots, len(raw)-MaxShots))
		raw = raw[:MaxShots]
	}
	for i, x := range raw {
		at := fmt.Sprintf("images 第 %d 张图", i+1)
		var ref any = x
		var m map[string]any
		if o, ok := x.(map[string]any); ok {
			m = o
			ref = firstStr(o, "id", "file", "path", "image")
		}
		f := p.file(ref, at)
		if f == nil {
			continue
		}
		if m != nil {
			f.Title = clip(firstStr(m, "title", "caption"), MaxTitle)
			f.Boxes = p.boxes(m["boxes"], at)
		}
		out = append(out, *f)
	}
	return out, p.warnings
}

// LocalizeImages 把 images 里的本机路径上传成附件 ID（MCP 与 ks 命令共用），每项都改成 {id, title?, boxes?}。
// 字符串一律当本机路径上传（与以前的 images 一致）；对象里写了 id 的原样留给 hub 解析。
func LocalizeImages(raw []any, cache map[string]string, upload func(path string) (string, error)) error {
	up := func(path string) (string, error) {
		if id, ok := cache[path]; ok {
			return id, nil
		}
		id, err := upload(path)
		if err != nil {
			return "", err
		}
		cache[path] = id
		return id, nil
	}
	for i, x := range raw {
		switch o := x.(type) {
		case string:
			id, err := up(strings.TrimSpace(o))
			if err != nil {
				return err
			}
			raw[i] = map[string]any{"id": id}
		case map[string]any:
			if str(o["id"]) != "" {
				continue
			}
			path := firstStr(o, "path", "file", "image")
			if path == "" {
				continue // 交给 hub 提示
			}
			if st, err := os.Stat(path); err != nil || st.IsDir() {
				return fmt.Errorf("images 第 %d 张图找不到本机文件：%s", i+1, path)
			}
			id, err := up(path)
			if err != nil {
				return err
			}
			o["id"] = id
			delete(o, "path")
			delete(o, "file")
			delete(o, "image")
		}
	}
	return nil
}

// TitleHints 找出没写标题的图（证据的附图和 figure / compare 块），返回一句提示；都有标题时返回空串。
// 网页上先用文件名兜底显示，提示 Agent 下次逐张写一句话标题。
func TitleHints(files []File, bs []Block) string {
	var names []string
	inBlock := map[string]bool{}
	for _, b := range bs {
		switch b.Type {
		case Figure:
			if b.Image != nil {
				inBlock[b.Image.ID] = true
				if b.Title == "" && b.Caption == "" {
					names = append(names, b.Image.Name)
				}
			}
		case Compare:
			for _, f := range []*File{b.Before, b.After} {
				if f != nil {
					inBlock[f.ID] = true
				}
			}
			if b.Title == "" && b.Caption == "" && b.After != nil {
				names = append(names, b.After.Name+"（对比）")
			}
		}
	}
	for _, f := range files {
		if inBlock[f.ID] || f.Title != "" || !(strings.HasPrefix(f.Mime, "image/") || strings.HasPrefix(f.Mime, "video/")) {
			continue
		}
		names = append(names, f.Name)
	}
	if len(names) == 0 {
		return ""
	}
	shown := names
	if len(shown) > 5 {
		shown = shown[:5]
	}
	more := ""
	if len(names) > len(shown) {
		more = fmt.Sprintf(" 等 %d 张", len(names))
	}
	return fmt.Sprintf("%s%s 没有标题，网页先用文件名显示。每张图写一句话标题（images 写成 {path, title, boxes}，figure / compare 块写 title），改动的地方用 boxes 框出来；可以重交一份带标题的证据", strings.Join(shown, "、"), more)
}
