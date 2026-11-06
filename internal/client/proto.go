package client

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/NekroAI/kitestring/internal/proto"
)

// ProtoSpec 是 Agent 交的一个可交互原型（R-197）：path 是本机目录、单个 .html 或 .zip。
type ProtoSpec struct {
	Path      string `json:"path"`
	Title     string `json:"title"`
	Cover     string `json:"cover,omitempty"`      // 封面截图（本机路径）
	Entry     string `json:"entry,omitempty"`      // 入口页面在包里的路径；不写时找 index.html
	VersionOf string `json:"version_of,omitempty"` // 指定是哪个原型的新版本（原型 ID）
}

// ParseProtoSpec 认三种写法：路径字符串、{path, title, …} 对象、对象的 JSON 文本。空的返回 ok = false。
func ParseProtoSpec(v any) (ProtoSpec, bool) {
	var s ProtoSpec
	switch x := v.(type) {
	case nil:
		return s, false
	case string:
		x = strings.TrimSpace(x)
		if x == "" {
			return s, false
		}
		if strings.HasPrefix(x, "{") && json.Unmarshal([]byte(x), &s) == nil {
			break
		}
		s.Path = x
	case map[string]any:
		b, _ := json.Marshal(x)
		_ = json.Unmarshal(b, &s)
	default:
		return s, false
	}
	if s.Path == "" {
		return s, false
	}
	if s.Title == "" {
		base := filepath.Base(strings.TrimRight(s.Path, "/"))
		if base == "index.html" {
			base = filepath.Base(filepath.Dir(s.Path))
		}
		s.Title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return s, true
}

// UploadedProto 是交上去的原型。
type UploadedProto struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Version  int      `json:"version"`
	Files    int      `json:"files"`
	Size     int64    `json:"size"`
	External []string `json:"external"`
}

// Note 是交原型后给 Agent 的一句话。
func (u UploadedProto) Note() string {
	s := fmt.Sprintf("原型「%s」第 %d 版（%d 个文件，%.1f MB）", u.Title, u.Version, u.Files, float64(u.Size)/(1<<20))
	if len(u.External) > 0 {
		s += fmt.Sprintf("，引用了 %d 个外部地址（离线时打不开，如 %s）", len(u.External), u.External[0])
	}
	return s
}

// UploadPrototype 打包并交一个原型；requirement 是挂在哪条需求上（可空）。
func (c *Client) UploadPrototype(ctx context.Context, s ProtoSpec, requirement, sessionKey string) (UploadedProto, error) {
	var out UploadedProto
	z, cleanup, err := proto.Pack(s.Path)
	if err != nil {
		return out, err
	}
	defer cleanup()
	fields := map[string]string{"title": s.Title, "entry": s.Entry, "version_of": s.VersionOf, "requirement": requirement, "session_key": sessionKey}
	if s.Cover != "" {
		var ref struct {
			ID string `json:"id"`
		}
		if err := c.Upload(ctx, "/agent/files", s.Cover, &ref); err != nil {
			return out, fmt.Errorf("上传原型封面 %s 失败：%w", s.Cover, err)
		}
		fields["cover"] = ref.ID
	}
	for k, v := range fields {
		if v == "" {
			delete(fields, k)
		}
	}
	if err := c.UploadFields(ctx, "/agent/prototypes", z, fields, &out); err != nil {
		return out, fmt.Errorf("交原型 %s 失败：%w", s.Path, err)
	}
	return out, nil
}

// PointPrototypes 把要决策的点里选项的 prototype（本机路径或对象）交上去，换成原型 ID。
func (c *Client) PointPrototypes(ctx context.Context, points []any, requirement, sessionKey string, notes *[]string) error {
	for _, p := range points {
		m, _ := p.(map[string]any)
		if m == nil {
			continue
		}
		opts, _ := m["options"].([]any)
		if opts == nil {
			opts, _ = m["items"].([]any)
		}
		for _, o := range opts {
			om, _ := o.(map[string]any)
			if om == nil {
				continue
			}
			spec, ok := ParseProtoSpec(om["prototype"])
			if !ok {
				continue
			}
			if _, plain := om["prototype"].(string); plain {
				if label, _ := om["label"].(string); label != "" {
					spec.Title = label // 只给了路径：用选项名当原型标题
				}
			}
			up, err := c.UploadPrototype(ctx, spec, requirement, sessionKey)
			if err != nil {
				return err
			}
			om["prototype"] = up.ID
			if notes != nil {
				*notes = append(*notes, up.Note())
			}
		}
	}
	return nil
}
