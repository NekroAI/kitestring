package hub

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

// 需求留言（R-249）：署名是调用的会话（身份 + 当时的标题）；空留言拒绝；--session 自报的已结束会话不能署名；
// 超过 600 字照常保存并提示压缩。
func TestPostComment(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	d, err := st.CreateDevice(ctx, "mac", "h1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, x := range [][4]string{{"claude:dev", "dev", "R-249 留言", "idle"}, {"claude:gone", "", "旧会话", "ended"}} {
		if _, err := st.DB().Exec(`INSERT INTO sessions(key, tool, session_id, device_id, role, title, state, started_at, last_active_at) VALUES(?, 'claude', ?, ?, ?, ?, ?, ?, ?)`, x[0], x[0], d.ID, x[1], x[2], x[3], now, now); err != nil {
			t.Fatal(err)
		}
	}
	r, err := st.CreateRequirement(ctx, store.Requirement{Title: "会话通信", Status: store.ReqInProgress, Priority: "P1"})
	if err != nil {
		t.Fatal(err)
	}
	post := func(key, text string, claimed bool) (*commentOut, error) {
		in := &commentIn{Ref: r.Code}
		in.Body.SessionKey, in.Body.Text, in.Body.SessionClaimed = key, text, claimed
		return h.postComment(ctx, in)
	}
	if _, err := post("claude:dev", "  ", false); err == nil {
		t.Fatal("空留言应拒绝")
	}
	if _, err := post("claude:gone", "冒用", true); err == nil || !strings.Contains(err.Error(), "没结束") {
		t.Fatalf("自报已结束的会话不能署名：%v", err)
	}
	if _, err := post("claude:nobody", "x", false); err == nil {
		t.Fatal("不存在的会话应拒绝")
	}
	out, err := post("claude:dev", "接口改成分页：每页 50 条。", false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Body.Hint != "" || out.Body.Requirement != r.Code || !strings.Contains(out.Body.Message, "署名：开发 claude:dev") {
		t.Fatalf("返回不对：%+v", out.Body)
	}
	c, err := st.GetDecision(ctx, out.Body.Code)
	if err != nil || c.Kind != store.KindComment || c.AssignedBy != "claude:dev" || c.FromRole != "dev" || c.ByTitle != "R-249 留言" || c.Status != store.DecClosed {
		t.Fatalf("留言记录不对：%+v %v", c, err)
	}
	long, err := post("claude:dev", strings.Repeat("长", 601), false)
	if err != nil || !strings.Contains(long.Body.Hint, "601 字") {
		t.Fatalf("超长留言应照常保存并提示：%+v %v", long, err)
	}
}
