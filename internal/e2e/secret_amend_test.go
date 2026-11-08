package e2e

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// 修改授权申请（R-320）：
//   - 取用因命令不在允许范围内被拒时，提示指向 ks secret amend；
//   - 会话对生效中的凭据申请改允许的命令：进「待处理」（凭据申请那一类），批准前原授权照常生效、新命令仍被拒；
//   - 不能重复提、理由必填、没有要改的 / 去掉不存在的命令报错；别的项目的会话不能申请；
//   - 保存并授权（到期时间不变）→ 申请方立即收到改了什么，没有值；时间线记下谁申请的、改了什么；
//   - 理由里写进的已知值隐去；
//   - 已撤回的凭据也能申请，拒绝 → 申请方收到拒绝、授权仍是已撤回；批准 → 重新授权。
func TestSecretAmend(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	const key = "claude:amd"
	hook(t, "claude", map[string]any{"session_id": "amd", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "部署", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session(key).ProjectID != "" })
	projectID := e.session(key).ProjectID

	const token = "portal-token-Zx9876543210"
	type reqOut struct {
		Decision store.Decision `json:"decision"`
		Existing bool           `json:"existing"`
		Message  string         `json:"message"`
	}
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
	use := func(sess string, argv ...string) error {
		return c.Do(ctx, "POST", "/agent/secrets/use", map[string]any{"session_key": sess, "name": "Portal 部署", "argv": argv, "program": "/bin/" + argv[0], "via_path": true}, nil)
	}
	amend := func(sess string, body map[string]any) (reqOut, error) {
		var out reqOut
		body["session_key"], body["name"] = sess, "Portal 部署"
		err := c.Do(ctx, "POST", "/agent/secrets/amend", body, &out)
		return out, err
	}
	fails := func(err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("应报错「%s」：%v", want, err)
		}
	}

	// 用户在网页上新建的凭据，允许的命令写错了格式：sh -c 后面的脚本被拆成了多段
	var det detail
	e.call("POST", "/api/secrets", map[string]any{"name": "Portal 部署", "purpose": "部署门户",
		"fields": []map[string]any{{"env": "PORTAL_TOKEN", "label": "Token", "value": token}}, "projects": []string{projectID}, "days": 7,
		"allowed": []string{"sh -c ./deploy.sh --prod", "portal whoami"}}, &det)
	x := det.Secret
	exp := *x.ExpiresAt

	// 1. 被拒时提示用修改授权申请
	err := use(key, "sh", "-c", "./deploy.sh --prod")
	fails(err, "ks secret amend")
	fails(err, "不要让用户去凭据页手动改")

	// 2. 申请的各种错误
	_, err = amend(key, map[string]any{"add_allowed": []string{"sh -c *"}})
	fails(err, "理由")
	_, err = amend(key, map[string]any{"reason": "x", "remove_allowed": []string{"sh -c nothing"}})
	fails(err, "允许的命令里没有「sh -c nothing」")
	_, err = amend(key, map[string]any{"reason": "x", "add_allowed": []string{"portal  whoami"}})
	fails(err, "没有要改的")
	_, err = amend(key, map[string]any{"reason": "x", "remove_allowed": []string{"sh -c ./deploy.sh --prod", "portal whoami"}})
	fails(err, "any_command")
	other := t.TempDir()
	hook(t, "claude", map[string]any{"session_id": "amd2", "cwd": other, "hook_event_name": "UserPromptSubmit", "prompt": "x", "prompt_id": "p0"})
	e.waitFor("另一个项目的会话入库", func() bool { s := e.session("claude:amd2"); return s.ProjectID != "" && s.ProjectID != projectID })
	_, err = amend("claude:amd2", map[string]any{"reason": "x", "add_allowed": []string{"*"}})
	fails(err, "没有授权给本项目")

	// 3. 申请：去掉写错的，加上对的（脚本是一个整体参数）；理由里贴进了值
	rq, err := amend(key, map[string]any{"reason": "sh -c 的脚本被拆成了多段，取用被拒；token 是 " + token,
		"remove_allowed": []string{"sh -c ./deploy.sh --prod"}, "add_allowed": []string{"sh -c *"}})
	if err != nil {
		t.Fatal(err)
	}
	d := rq.Decision
	if d.RequestKind != store.RequestSecret || !d.Approval || d.Status != store.DecPending || !strings.Contains(d.Question, "修改授权") {
		t.Fatalf("修改授权申请应是一条进待处理的凭据申请：%+v", d)
	}
	if strings.Contains(d.Context, token) || !strings.Contains(d.Context, "[已隐去]") {
		t.Fatalf("理由里的已知值应隐去：%s", d.Context)
	}
	if !strings.Contains(rq.Message, "批准前原来的授权照常生效") || !strings.Contains(rq.Message, "加上「sh -c *」") || strings.Contains(rq.Message, token) {
		t.Fatalf("回执应写明改了什么、批准前旧授权照常生效，没有值：%s", rq.Message)
	}
	x = get(d.Code).Secret
	if !x.RequestPending || !x.Ask.IsAmend() || !slices.Equal(x.Ask.Allowed, []string{"portal whoami", "sh -c *"}) || x.Ask.Days != -1 {
		t.Fatalf("申请里应是改后的完整授权、到期时间不变：%+v", x.Ask)
	}
	// 批准前：授权不变，旧命令照常可用，新命令仍被拒
	if !slices.Equal(x.Allowed, []string{"sh -c ./deploy.sh --prod", "portal whoami"}) || x.Status != store.SecretActive {
		t.Fatalf("批准前授权不应变：%+v", x)
	}
	if err := use(key, "portal", "whoami"); err != nil {
		t.Fatalf("批准前旧授权照常生效：%v", err)
	}
	fails(use(key, "sh", "-c", "./deploy.sh --prod"), "不在允许范围内")
	if again, err := amend(key, map[string]any{"reason": "x", "add_allowed": []string{"a"}}); err != nil || !again.Existing {
		t.Fatalf("已有等待中的申请时不重复提：%+v %v", again, err)
	}
	if code, _ := e.try("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d.Code, "answer": "批准", "verdict": "approved"}}}); code != http.StatusBadRequest {
		t.Fatalf("修改授权申请不能在通用作答接口里直接批准：%d", code)
	}

	// 4. 保存并授权（用户可再调整：这里顺带允许写入磁盘），到期时间不变 → 申请方立即收到
	done := parkWait(t, e, c, key)
	e.call("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{"projects": x.Ask.Projects, "days": -1, "allowed": x.Ask.Allowed, "disk": true}, nil)
	got := woke(t, done, "批准", "修改", "加上「sh -c *」", "去掉「sh -c ./deploy.sh --prod」", "写入磁盘：不允许 → 允许", "ks secret run")
	if strings.Contains(got, token) {
		t.Fatalf("答复里不应有值：%s", got)
	}
	det = get(x.ID)
	x = det.Secret
	if x.RequestPending || !x.Disk || x.ExpiresAt == nil || !x.ExpiresAt.Equal(exp) || !slices.Equal(x.Allowed, []string{"portal whoami", "sh -c *"}) {
		t.Fatalf("批准后应按调整后的授权生效、到期时间不变：%+v", x)
	}
	if err := use(key, "sh", "-c", "./deploy.sh --prod"); err != nil {
		t.Fatalf("改后的命令可用：%v", err)
	}
	var amendLog, editLog *store.SecretLog
	for i, l := range det.Logs {
		switch l.Kind {
		case store.SecretLogAmend:
			amendLog = &det.Logs[i]
		case store.SecretLogEdit:
			editLog = &det.Logs[i]
		}
	}
	if amendLog == nil || amendLog.SessionKey != key || !strings.Contains(amendLog.Reason, "拆成了多段") || strings.Contains(amendLog.Reason, token) {
		t.Fatalf("时间线应记下会话申请修改授权和理由（值隐去）：%+v", amendLog)
	}
	if editLog == nil || editLog.SessionKey != key || !strings.Contains(editLog.Reason, "加上「sh -c *」") || editLog.Days != -1 {
		t.Fatalf("时间线应记下按哪个会话的申请改了什么：%+v", editLog)
	}

	// 5. 已撤回的凭据：申请重新授权，拒绝 → 仍是已撤回；再申请、批准 → 重新授权
	e.call("POST", "/api/secrets/"+x.ID+"/revoke", nil, nil)
	err = use(key, "portal", "whoami")
	fails(err, "ks secret amend")
	rq, err = amend(key, map[string]any{"reason": "继续部署", "days": 30})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := e.try("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{"projects": []string{projectID}, "days": -1}); code != http.StatusBadRequest {
		t.Fatalf("已撤回的凭据不能保留原到期时间：%d", code)
	}
	done = parkWait(t, e, c, key)
	e.call("POST", "/api/secrets/"+x.ID+"/reject", map[string]any{"note": "先别部署"}, nil)
	woke(t, done, "拒绝", "先别部署")
	if x = get(x.ID).Secret; x.Status != store.SecretRevoked || x.RequestPending {
		t.Fatalf("拒绝后仍是已撤回：%+v", x)
	}
	rq, err = amend(key, map[string]any{"reason": "继续部署", "days": 30, "projects": []string{x.ProjectNames[0]}})
	if err != nil {
		t.Fatal(err)
	}
	if x = get(rq.Decision.Code).Secret; x.Ask.Days != 30 {
		t.Fatalf("申请的有效期：%+v", x.Ask)
	}
	e.call("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{"projects": x.Ask.Projects, "days": 30, "allowed": x.Ask.Allowed, "disk": x.Ask.Disk}, nil)
	det = get(x.ID)
	if det.Secret.Status != store.SecretActive || det.Logs[0].Kind != store.SecretLogRegrant || det.Logs[0].SessionKey != key {
		t.Fatalf("批准已撤回凭据的修改授权申请 = 重新授权，记下申请的会话：%+v %+v", det.Secret, det.Logs[0])
	}
}
