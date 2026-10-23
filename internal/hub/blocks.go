package hub

import (
	"context"
	"fmt"
	"strings"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/store"
)

// blockFile 把表达块里写的附件 ID（也接受 file:ID、/api/files/ID 这类写法）解析成附件（R-97）。
func (h *Hub) blockFile(ctx context.Context) blocks.Resolver {
	return func(ref string) (blocks.File, bool) {
		id := ref
		if i := strings.LastIndexAny(id, ":/"); i >= 0 {
			id = id[i+1:]
		}
		f, err := h.store.GetFile(ctx, id)
		if err != nil {
			return blocks.File{}, false
		}
		return blocks.File(f), true
	}
}

// mergeFiles 把块里引用的图并进证据附件（去重），旧界面、截图类证据的检查和 req_get 都能看到它们。
func mergeFiles(files []store.FileRef, extra []blocks.File) []store.FileRef {
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.ID] = true
	}
	for _, f := range extra {
		if !seen[f.ID] {
			seen[f.ID] = true
			files = append(files, store.FileRef(f))
		}
	}
	return files
}

// pointsAnswer 把多点决策的逐点选择拼成答复文本（R-112），逐点注入 Agent 的上下文：
//
//	批准，按以下选择：
//	1. 旧 PM 被接管后变成什么？ → 无身份
//	2. 每轮拦几次就放行？ → 2 次
//	附言：……
func pointsAnswer(d store.Decision, choices []int, note string, reject bool) (string, error) {
	note = strings.TrimSpace(note)
	if reject {
		if note != "" {
			return "拒绝：" + note, nil
		}
		return "拒绝", nil
	}
	if len(choices) != len(d.Points) {
		return "", fmt.Errorf("%s 有 %d 个要拍板的点，收到 %d 个选择", d.Code, len(d.Points), len(choices))
	}
	var b strings.Builder
	if d.Approval {
		b.WriteString("批准，按以下选择：")
	} else {
		b.WriteString("按以下选择：")
	}
	for i, p := range d.Points {
		c := choices[i]
		if c < 0 || c >= len(p.Items) {
			return "", fmt.Errorf("第 %d 个点「%s」还没选", i+1, p.Title)
		}
		fmt.Fprintf(&b, "\n%d. %s → %s", i+1, p.Title, p.Items[c].Label)
	}
	if note != "" {
		b.WriteString("\n附言：" + note)
	}
	return b.String(), nil
}
