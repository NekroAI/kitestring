package e2e

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// 凭据按稳定编号取用、名称可改（R-326）：
//   - 每条凭据有编号 S-x，网页接口、ks secret list 都给出；取用认编号、内部 ID 和名称；
//   - 凭据页改名：编号不变，按编号取用不受影响，时间线记改名；不能撞名、不能写成编号的样子；
//   - 修改授权申请里提出改名：标题写「改名」，回执给出编号；抽屉里可再调整新名称后保存并授权，时间线记改名和申请的会话，答复里有改名和 ks secret run S-x；
//   - 申请结果、被拒提示都给编号写法；名称对上多条时拒绝并列出编号。
func TestSecretCodeRename(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	const key = "claude:scode"
	hook(t, "claude", map[string]any{"session_id": "scode", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "发版", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session(key).ProjectID != "" })
	projectID := e.session(key).ProjectID

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
	use := func(ref string, argv ...string) error {
		return c.Do(ctx, "POST", "/agent/secrets/use", map[string]any{"session_key": key, "name": ref, "argv": argv, "program": "/usr/local/bin/" + argv[0], "via_path": true}, nil)
	}
	mk := func(name string) store.Secret {
		var d detail
		e.call("POST", "/api/secrets", map[string]any{"name": name, "fields": []map[string]any{{"env": "TOKEN", "value": "tok-" + name + "-0123456789"}}, "projects": []string{projectID}, "days": 30, "allowed": []string{"gh *"}}, &d)
		return d.Secret
	}
	a := mk("官网 GitHub Token（一年期）")
	b := mk("Cloudflare 部署")
	if a.Code != "S-1" || b.Code != "S-2" {
		t.Fatalf("编号：%s %s", a.Code, b.Code)
	}

	// 1. 取用认编号、内部 ID、名称
	for _, ref := range []string{"S-1", a.ID, a.Name} {
		if err := use(ref, "gh", "release", "list"); err != nil {
			t.Fatalf("按 %q 取用：%v", ref, err)
		}
	}
	err := use("S-1", "curl", "x")
	if err == nil || !strings.Contains(err.Error(), "ks secret amend S-1") {
		t.Fatalf("被拒提示写编号：%v", err)
	}
	var list struct {
		Secrets []struct {
			Code string `json:"code"`
			Run  string `json:"run"`
		} `json:"secrets"`
	}
	if err := c.Do(ctx, "GET", "/agent/secrets?session_key="+key, nil, &list); err != nil || len(list.Secrets) != 2 || list.Secrets[0].Run != "ks secret run "+list.Secrets[0].Code+" -- <命令>" {
		t.Fatalf("ks secret list 给出编号和写法：%+v %v", list, err)
	}

	// 2. 凭据页改名：编号不变，按编号照常取用，旧名称不再认
	if code, body := e.try("POST", "/api/secrets/"+a.ID+"/rename", map[string]any{"name": "cloudflare 部署"}); code != http.StatusBadRequest || !strings.Contains(body, "已有名为") {
		t.Fatalf("改名不能撞名：%d %s", code, body)
	}
	if code, _ := e.try("POST", "/api/secrets/"+a.ID+"/rename", map[string]any{"name": "S-7"}); code != http.StatusBadRequest {
		t.Fatalf("名称不能写成编号：%d", code)
	}
	var d detail
	e.call("POST", "/api/secrets/"+a.ID+"/rename", map[string]any{"name": "官网 GitHub Token"}, &d)
	if d.Secret.Code != "S-1" || d.Secret.Name != "官网 GitHub Token" || d.Logs[0].Kind != store.SecretLogRename || d.Logs[0].Reason != "「官网 GitHub Token（一年期）」→「官网 GitHub Token」" {
		t.Fatalf("改名后编号不变，时间线记改名：%+v %+v", d.Secret, d.Logs[0])
	}
	if err := use("S-1", "gh", "release", "list"); err != nil {
		t.Fatalf("改名后按编号取用不受影响：%v", err)
	}
	if err := use("官网 GitHub Token（一年期）", "gh", "x"); err == nil || !strings.Contains(err.Error(), "没有编号或名称") {
		t.Fatalf("旧名称不再认：%v", err)
	}

	// 3. 修改授权申请里提出改名
	var rq struct {
		Decision store.Decision `json:"decision"`
		Message  string         `json:"message"`
	}
	if err := c.Do(ctx, "POST", "/agent/secrets/amend", map[string]any{"session_key": key, "name": "S-1", "reason": "名称里有全角括号，命令里不好写", "rename": "Cloudflare 部署"}, nil); err == nil || !strings.Contains(err.Error(), "已有名为") {
		t.Fatalf("申请改名不能撞名：%v", err)
	}
	if err := c.Do(ctx, "POST", "/agent/secrets/amend", map[string]any{"session_key": key, "name": "S-1", "reason": "统一命名", "rename": "官网发版 Token"}, &rq); err != nil {
		t.Fatal(err)
	}
	if rq.Decision.Question != "官网 GitHub Token：改名" || !strings.Contains(rq.Message, "S-1") || !strings.Contains(rq.Message, "名称：「官网 GitHub Token」→「官网发版 Token」") {
		t.Fatalf("改名申请：%s / %s", rq.Decision.Question, rq.Message)
	}
	x := get(rq.Decision.Code).Secret
	if x.Ask.Name != "官网发版 Token" || x.Name != "官网 GitHub Token" {
		t.Fatalf("批准前名称不变，申请里记新名称：%+v", x)
	}
	done := parkWait(t, e, c, key)
	e.call("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{"projects": x.Projects, "days": -1, "allowed": x.Allowed, "name": "官网发版 GitHub Token", "note": "按我调整后的名称"}, nil)
	got := woke(t, done, "批准", "名称：「官网 GitHub Token」→「官网发版 GitHub Token」", "ks secret run S-1 --", "按我调整后的名称")
	_ = got
	d = get("S-1")
	if d.Secret.Name != "官网发版 GitHub Token" || d.Logs[0].Kind != store.SecretLogRename || d.Logs[0].SessionKey != key || d.Logs[0].Note != "按我调整后的名称" {
		t.Fatalf("批准后改名生效，时间线记申请的会话和附言：%+v %+v", d.Secret.Name, d.Logs[0])
	}
	for _, l := range d.Logs[:2] {
		if l.Kind == store.SecretLogEdit {
			t.Fatal("只改名不另记修改授权")
		}
	}
	if err := use("S-1", "gh", "release", "list"); err != nil {
		t.Fatalf("改名后按编号取用：%v", err)
	}

	// 4. 新申请的结果里给编号写法
	var nr struct {
		Decision store.Decision `json:"decision"`
	}
	if err := c.Do(ctx, "POST", "/agent/secrets/request", map[string]any{"session_key": key, "name": "Sentry", "purpose": "上传 sourcemap", "fields": []map[string]any{{"env": "SENTRY_TOKEN", "how": "Sentry → Settings"}}}, &nr); err != nil {
		t.Fatal(err)
	}
	sx := get(nr.Decision.Code).Secret
	done = parkWait(t, e, c, key)
	e.call("POST", "/api/secrets/"+sx.ID+"/authorize", map[string]any{"projects": []string{projectID}, "days": 7, "values": map[string]string{"SENTRY_TOKEN": "sntrys-0123456789"}}, nil)
	woke(t, done, "批准", "凭据编号 "+sx.Code, "ks secret run "+sx.Code+" --")

	// 5. 名称对上多条（老数据兜底）：拒绝并列出编号
	if _, err := e.store.DB().Exec(`UPDATE secrets SET name = 'Sentry' WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := use("Sentry", "gh", "x"); err == nil || !strings.Contains(err.Error(), b.Code+"、"+sx.Code) {
		t.Fatalf("名称对上多条应拒绝并列出编号：%v", err)
	}
}
