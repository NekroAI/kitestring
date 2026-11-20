package assetpush

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 具名变体（R-380）：asset.json 的 variants 里每个变体各推一条素材，各有版本和审核。
// 默认变体（default_variant，没写时是 default，再没有取第一个）就是素材本身；其余变体的 key 是「素材 id@变体名」。
// 变体可写 file / files（主文件）、previews（预览图）、triangles、source / license；没写的来源、许可、项目字段沿用素材本身的。
// 预览图也能按文件名归属：「<名字>@<变体名>__<机位>.png」。

type variantSpec struct {
	Name      string            `json:"name"`
	File      string            `json:"file"`
	Files     map[string]string `json:"files"`
	Previews  []previewRef      `json:"previews"`
	Triangles int               `json:"triangles"`
	Source    json.RawMessage   `json:"source"`
	License   json.RawMessage   `json:"license"`
	Audio     json.RawMessage   `json:"audio"`
}

// mainFile 是变体的主文件：file，或 files 里按 pickMainFile 的顺序取。
func (v variantSpec) mainFile() string {
	if f := strings.TrimSpace(v.File); f != "" {
		return f
	}
	return pickMainFile(v.Files)
}

// parseVariants 读 variants：对象写法 {"名字": {...}}（按名字排序）或数组写法 [{"name": ...}]（保持顺序）。
func parseVariants(raw json.RawMessage) ([]variantSpec, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var byName map[string]variantSpec
	if json.Unmarshal(raw, &byName) == nil {
		names := make([]string, 0, len(byName))
		for k := range byName {
			names = append(names, k)
		}
		sort.Strings(names)
		out := make([]variantSpec, 0, len(names))
		for _, n := range names {
			v := byName[n]
			v.Name = n
			out = append(out, v)
		}
		return out, true
	}
	var list []variantSpec
	if json.Unmarshal(raw, &list) == nil {
		return list, true
	}
	return nil, false
}

// defaultVariantName 和 variantFile 选默认变体的规则一致：default_variant，再 default，再第一个。
func defaultVariantName(list []variantSpec, def string) string {
	for _, k := range []string{strings.TrimSpace(def), "default"} {
		for _, v := range list {
			if k != "" && v.Name == k {
				return k
			}
		}
	}
	if len(list) > 0 {
		return list[0].Name
	}
	return ""
}

type variantBuilt struct {
	it    protocol.AssetItem
	check Check
}

// variantItems 把素材的非默认变体做成单独的推送条目，返回默认变体名和这些条目（逐件带校验结果）。
// dirs 是找文件的目录（asset.json 所在目录、previews_base、仓库根）。
func variantItems(main protocol.AssetItem, src string, raw json.RawMessage, def string, dirs []string, root string, ffmpeg bool) (string, []variantBuilt, []string) {
	list, ok := parseVariants(raw)
	if !ok {
		return "", nil, []string{"variants 要写成对象 {\"名字\": {...}} 或数组 [{\"name\": ...}]"}
	}
	if len(list) == 0 {
		return "", nil, nil
	}
	var mainErrs []string
	defName := defaultVariantName(list, def)
	if def != "" && defName != strings.TrimSpace(def) {
		mainErrs = append(mainErrs, "default_variant「"+def+"」不在 variants 里")
	}
	if msg := protocol.CheckVariantName(defName); msg != "" {
		mainErrs = append(mainErrs, "默认变体名「"+defName+"」"+msg)
		return "", nil, mainErrs
	}
	var out []variantBuilt
	seen := map[string]bool{strings.ToLower(defName): true}
	for i, v := range list {
		name := strings.TrimSpace(v.Name)
		if name == defName && i == indexOf(list, defName) {
			continue
		}
		b := variantBuilt{}
		c := &b.check
		c.Source, c.Category, c.Batches = src, main.Category, main.Batches
		c.ID = protocol.VariantKey(main.Key, name)
		if msg := protocol.CheckVariantName(name); msg != "" {
			c.ID = main.Key + " 的第 " + strconv.Itoa(i+1) + " 个变体"
			c.Errors = append(c.Errors, "变体名「"+name+"」"+msg)
			out = append(out, b)
			continue
		}
		if seen[strings.ToLower(name)] {
			c.Errors = append(c.Errors, "变体名「"+name+"」重复了（或只差大小写）")
			out = append(out, b)
			continue
		}
		seen[strings.ToLower(name)] = true
		it := protocol.AssetItem{Key: c.ID, Parent: main.Key, Variant: name, Category: main.Category, Batches: main.Batches,
			Source: main.Source, License: main.License, Name: main.Name, Meta: main.Meta, Triangles: v.Triangles}
		if s := textOf(v.Source); s != "" {
			it.Source = s
		}
		if s := textOf(v.License); s != "" {
			it.License = s
		}
		ref := v.mainFile()
		switch {
		case ref == "":
			c.Errors = append(c.Errors, "变体没写主文件（file 或 files）")
		case winPathProblem(ref) != "":
			c.Errors = append(c.Errors, "文件路径"+winPathProblem(ref))
		default:
			c.File = ref
			file := ""
			for _, d := range dirs {
				if d != "" {
					if file = resolve(d, root, ref); file != "" {
						break
					}
				}
			}
			if file == "" {
				c.Errors = append(c.Errors, "文件找不到："+ref)
				break
			}
			it.File = &protocol.AssetFile{Path: file, Name: filepath.Base(file)}
			switch it.Kind = fileKind(file); it.Kind {
			case protocol.AssetImage:
				if px, svg := checkFontSVG(file, c); svg {
					it.Pixels = px
				} else {
					it.Pixels = imageSize(file)
				}
			case protocol.AssetFont:
				checkFontSVG(file, c)
			case protocol.AssetAudio:
				var errs, warns []string
				it.Audio, errs, warns = audioInfo(file, v.Audio, ffmpeg)
				c.Errors, c.Warnings, c.Audio = append(c.Errors, errs...), append(c.Warnings, warns...), AudioText(it.Audio)
			case "":
				c.Errors = append(c.Errors, "文件类型不支持："+filepath.Base(file)+unsupportedType)
			}
			c.Kind = it.Kind
		}
		for _, pv := range v.Previews {
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
			for _, d := range dirs {
				cand := ref
				if !filepath.IsAbs(ref) {
					if d == "" {
						continue
					}
					cand = filepath.Join(d, ref)
				}
				if st, err := os.Stat(cand); err == nil && !st.IsDir() {
					path = cand
					break
				}
			}
			switch {
			case path == "":
				c.Errors = append(c.Errors, "预览图找不到："+ref)
			case !imageExt[strings.ToLower(filepath.Ext(path))]:
				c.Errors = append(c.Errors, "预览图要是图片："+ref)
			default:
				addPreview(c, &it, path, filepath.Base(path), strings.TrimSpace(pv.View), nil)
			}
		}
		b.it = it
		out = append(out, b)
	}
	return defName, out, mainErrs
}

func indexOf(list []variantSpec, name string) int {
	for i, v := range list {
		if strings.TrimSpace(v.Name) == name {
			return i
		}
	}
	return -1
}
