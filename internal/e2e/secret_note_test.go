package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// 凭据授权 / 拒绝的附言（R-321）：
//   - 凭据申请「保存并授权」带附言 → 申请方收到的结果里有附言原文；时间线记下附言；附言里写进的已知值（含这次新填的）隐去；
//   - 修改授权申请批准、拒绝都能带附言，同样送达、记时间线；
//   - 凭据页手动修改授权的附言只记在时间线上。
func TestSecretNote(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	const key = "claude:note"
	hook(t, "claude", map[string]any{"session_id": "note", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "部署", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session(key).ProjectID != "" })
	projectID := e.session(key).ProjectID
	const token = "note-token-Qw1234567890"
	type detail struct {
		Secret store.Secret      `json:"secret"`
		Logs   []store.SecretLog `json:"logs"`
	}
	get := func(ref string) detail {
		t.Helper()
		var d detail
		e.call("GET", "/api/secrets/"+ref, nil, &d)
		return d
	}
	var rq struct {
		Decision store.Decision `json:"decision"`
	}
	if err := c.Do(ctx, "POST", "/agent/secrets/request", map[string]any{"session_key": key, "name": "只读 Token", "purpose": "读统计", "days": 7,
		"fields": []map[string]string{{"env": "RO_TOKEN", "label": "Token"}}}, &rq); err != nil {
		t.Fatal(err)
	}
	x := get(rq.Decision.Code).Secret

	// 1. 保存并授权带附言：送达原文；附言里贴进的新值隐去
	done := parkWait(t, e, c, key)
	e.call("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{"values": map[string]string{"RO_TOKEN": token}, "projects": []string{projectID}, "days": 7,
		"note": "这个 Token 只读，别在生产库跑写操作。值是 " + token}, nil)
	got := woke(t, done, "批准", "附言：这个 Token 只读，别在生产库跑写操作。", "[已隐去]")
	if strings.Contains(got, token) {
		t.Fatalf("附言里的值应隐去：%s", got)
	}
	d := get(x.ID)
	if l := d.Logs[0]; l.Kind != store.SecretLogGrant || !strings.HasPrefix(l.Note, "这个 Token 只读") || strings.Contains(l.Note, token) {
		t.Fatalf("时间线应记下附言（值隐去）：%+v", l)
	}

	// 2. 修改授权申请：拒绝带附言 → 送达、记时间线
	amend := func() store.Decision {
		t.Helper()
		var out struct {
			Decision store.Decision `json:"decision"`
		}
		if err := c.Do(ctx, "POST", "/agent/secrets/amend", map[string]any{"session_key": key, "name": "只读 Token", "reason": "要写入磁盘", "disk": true}, &out); err != nil {
			t.Fatal(err)
		}
		return out.Decision
	}
	amend()
	done = parkWait(t, e, c, key)
	e.call("POST", "/api/secrets/"+x.ID+"/reject", map[string]any{"note": "改用 ks secret run，不要落盘"}, nil)
	woke(t, done, "拒绝", "附言：改用 ks secret run，不要落盘")
	if l := get(x.ID).Logs[0]; l.Kind != store.SecretLogReject || l.Note != "改用 ks secret run，不要落盘" {
		t.Fatalf("拒绝的附言记在时间线上：%+v", l)
	}

	// 3. 修改授权申请批准带附言
	amend()
	done = parkWait(t, e, c, key)
	e.call("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{"projects": []string{projectID}, "days": -1, "disk": true, "note": "写到 /tmp 下，用完删掉"}, nil)
	woke(t, done, "批准", "写入磁盘：不允许 → 允许", "附言：写到 /tmp 下，用完删掉")
	if l := get(x.ID).Logs[0]; l.Kind != store.SecretLogEdit || l.Note != "写到 /tmp 下，用完删掉" || l.SessionKey != key {
		t.Fatalf("批准修改授权的附言记在时间线上：%+v", l)
	}

	// 4. 凭据页手动修改授权：附言只记时间线
	e.call("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{"projects": []string{projectID}, "days": 30, "note": "续期一个月"}, nil)
	if l := get(x.ID).Logs[0]; l.Kind != store.SecretLogEdit || l.Note != "续期一个月" || l.SessionKey != "" {
		t.Fatalf("手动修改授权的附言记在时间线上：%+v", l)
	}
}
