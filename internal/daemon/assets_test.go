package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

func spoolTestQueue(t *testing.T) *queue {
	t.Helper()
	t.Setenv("KS_HOME", t.TempDir())
	q, err := openQueue(filepath.Join(Home(), "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.db.Close() })
	return q
}

func spoolTestJob(t *testing.T, q *queue, id string, contents ...string) []string {
	t.Helper()
	p := protocol.AssetPush{PushID: id, Batch: "S1"}
	var shas []string
	for i, c := range contents {
		src := filepath.Join(t.TempDir(), "f")
		os.WriteFile(src, []byte(c), 0o600)
		sha, size, err := spoolFile(src)
		if err != nil {
			t.Fatal(err)
		}
		shas = append(shas, sha)
		p.Items = append(p.Items, protocol.AssetItem{Key: string(rune('a' + i)), File: &protocol.AssetFile{Name: "f", SHA256: sha, Size: size}})
	}
	if err := q.assetJobAdd(p); err != nil {
		t.Fatal(err)
	}
	return shas
}

func spoolNames(t *testing.T) map[string]bool {
	t.Helper()
	entries, _ := os.ReadDir(spoolDir())
	out := map[string]bool{}
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}

// R-418：批次推完先删暂存文件再删记录；别的批次还在用的文件留着。
func TestAssetJobDoneRemovesSpool(t *testing.T) {
	q := spoolTestQueue(t)
	a := spoolTestJob(t, q, "A", "only-a", "shared")
	b := spoolTestJob(t, q, "B", "shared", "only-b")
	if err := q.assetJobDone("A"); err != nil {
		t.Fatal(err)
	}
	got := spoolNames(t)
	if got[a[0]] || !got[a[1]] || !got[b[1]] || len(got) != 2 {
		t.Fatalf("A 推完只删 A 独有的文件：%v", got)
	}
	if ids := q.assetJobsQueued(); len(ids) != 1 || ids[0] != "B" {
		t.Fatalf("队列：%v", ids)
	}
	if err := q.assetJobDone("B"); err != nil {
		t.Fatal(err)
	}
	if got := spoolNames(t); len(got) != 0 {
		t.Fatalf("都推完后暂存区应清空：%v", got)
	}
}

// 删不掉的暂存文件要报出来，批次照样从队列删掉。
func TestAssetJobDoneReportsRemoveFailure(t *testing.T) {
	q := spoolTestQueue(t)
	shas := spoolTestJob(t, q, "A", "x")
	p := filepath.Join(spoolDir(), shas[0])
	os.Remove(p)
	os.MkdirAll(filepath.Join(p, "busy"), 0o700) // 非空目录：os.Remove 在各平台都删不掉
	if err := q.assetJobDone("A"); err == nil {
		t.Fatal("删不掉应返回错误")
	}
	if ids := q.assetJobsQueued(); len(ids) != 0 {
		t.Fatalf("批次应已删掉：%v", ids)
	}
}

// sweepSpool 清掉没有批次引用、放了一阵的残留文件；刚复制进来的和还在用的不动。
func TestSweepSpool(t *testing.T) {
	q := spoolTestQueue(t)
	used := spoolTestJob(t, q, "A", "used")[0]
	dir := spoolDir()
	for _, name := range []string{"orphan-old", "orphan-new", "in-123"} {
		os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600)
	}
	old := time.Now().Add(-time.Hour)
	for _, name := range []string{used, "orphan-old", "in-123"} {
		os.Chtimes(filepath.Join(dir, name), old, old)
	}
	if err := q.sweepSpool(time.Now()); err != nil {
		t.Fatal(err)
	}
	got := spoolNames(t)
	if !got[used] || !got["orphan-new"] || got["orphan-old"] || got["in-123"] || len(got) != 2 {
		t.Fatalf("清理后：%v", got)
	}
}
