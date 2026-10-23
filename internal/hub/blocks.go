package hub

import (
	"context"
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
