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
	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/store"
)

// 修改授权申请里替换值（R-325）：
//   - 会话写明要替换哪几个值和获取方式，可以只替换值、不改授权；申请里没有值；
//   - 没填新值、填了和原来相同的值都不能保存并授权；
//   - 批准后新值生效、旧值作废，凭据 ID、名字、取用记录延续；时间线记「替换了哪些值」（只有显示名），附言和申请的会话记在这一条；
//   - 申请方立即收到结果，没有值；
//   - 换掉的敏感旧值仍参与脱敏（已存记录里的旧值继续隐去），新值也隐去；不敏感的值（R-318）换掉后照常不隐去；
//   - 凭据页手动替换值也一样留下旧值；替换值和改授权可以一起申请。
func TestSecretAmendReplace(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	const key = "claude:rpl"
	hook(t, "claude", map[string]any{"session_id": "rpl", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "发版", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session(key).ProjectID != "" })
	projectID := e.session(key).ProjectID

	const (
		name    = "官网 GitHub Token"
		oldTok  = "ghp_oldTokenAAAA1111"
		newTok  = "ghp_newTokenBBBB2222"
		user    = "kite-release-bot"
		newUser = "kite-deploy-bot"
	)
	type reqOut struct {
		Decision store.Decision `json:"decision"`
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
	use := func() map[string]string {
		t.Helper()
		var u struct {
			Env map[string]string `json:"env"`
		}
		if err := c.Do(ctx, "POST", "/agent/secrets/use", map[string]any{"session_key": key, "name": name, "argv": []string{"gh", "release", "list"}, "program": "/usr/local/bin/gh", "via_path": true}, &u); err != nil {
			t.Fatal(err)
		}
		return u.Env
	}
	amend := func(body map[string]any) (reqOut, error) {
		var out reqOut
		body["session_key"], body["name"] = key, name
		err := c.Do(ctx, "POST", "/agent/secrets/amend", body, &out)
		return out, err
	}
	prompt := func(id, text string) string {
		t.Helper()
		hook(t, "claude", map[string]any{"session_id": "rpl", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": text, "prompt_id": id})
		e.waitFor("消息入库 "+id, func() bool { return strings.HasPrefix(e.session(key).LastPrompt, id) })
		return e.session(key).LastPrompt
	}

	var det detail
	e.call("POST", "/api/secrets", map[string]any{"name": name, "purpose": "发版",
		"fields":   []map[string]any{{"env": "GITHUB_TOKEN", "label": "Token", "how": "GitHub → Settings → Tokens", "value": oldTok}, {"env": "GITHUB_USER", "label": "用户名", "value": user, "plain": true}},
		"projects": []string{projectID}, "days": 365, "allowed": []string{"gh *"}}, &det)
	x := det.Secret
	exp := *x.ExpiresAt
	if use()["GITHUB_TOKEN"] != oldTok {
		t.Fatal("旧值可取用")
	}
	// 已存的记录里出现过旧值
	if got := prompt("a1", "a1 旧 token "+oldTok); got != "a1 旧 token "+hub.SecretRedacted {
		t.Fatalf("旧值隐去：%q", got)
	}

	// 1. 申请：只替换值，不改授权；环境变量名要是已有的
	if _, err := amend(map[string]any{"reason": "x", "replace": []map[string]any{{"env": "NOPE"}}}); err == nil || !strings.Contains(err.Error(), "没有 NOPE 这个值") {
		t.Fatalf("替换不存在的值应报错：%v", err)
	}
	rq, err := amend(map[string]any{"reason": "Token 一年期快到期，已在 GitHub 新建", "replace": []map[string]any{{"env": "GITHUB_TOKEN", "how": "GitHub → Settings → Developer settings → Fine-grained tokens → Generate new token"}}})
	if err != nil {
		t.Fatal(err)
	}
	d := rq.Decision
	if d.Question != name+"：替换值" || d.RequestKind != store.RequestSecret || d.Status != store.DecPending {
		t.Fatalf("只替换值的申请：%+v", d)
	}
	if !strings.Contains(rq.Message, "替换值：Token") || !strings.Contains(rq.Message, "拿不到值") || !strings.Contains(rq.Message, "不要新建凭据") {
		t.Fatalf("回执：%s", rq.Message)
	}
	x = get(d.Code).Secret
	if !x.Ask.IsAmend() || len(x.Ask.Replace) != 1 || x.Ask.Replace[0].Env != "GITHUB_TOKEN" || !strings.Contains(x.Ask.Replace[0].How, "Fine-grained") {
		t.Fatalf("申请里记下要替换的值和获取方式：%+v", x.Ask)
	}
	if x.Fields[0].How != "GitHub → Settings → Tokens" || use()["GITHUB_TOKEN"] != oldTok {
		t.Fatal("批准前值和字段说明不变")
	}

	// 2. 没填新值、新值和原来相同：不能保存并授权
	auth := map[string]any{"projects": x.Projects, "days": -1, "allowed": x.Allowed}
	if code, body := e.try("POST", "/api/secrets/"+x.ID+"/authorize", auth); code != http.StatusBadRequest || !strings.Contains(body, "要填写新的值") {
		t.Fatalf("没填新值：%d %s", code, body)
	}
	auth["values"] = map[string]string{"GITHUB_TOKEN": oldTok}
	if code, body := e.try("POST", "/api/secrets/"+x.ID+"/authorize", auth); code != http.StatusBadRequest || !strings.Contains(body, "和原来的相同") {
		t.Fatalf("新值和原来相同：%d %s", code, body)
	}

	// 3. 填新值保存并授权 → 申请方立即收到，没有值
	done := parkWait(t, e, c, key)
	auth["values"] = map[string]string{"GITHUB_TOKEN": newTok}
	auth["note"] = "旧 Token 已在 GitHub 删除"
	e.call("POST", "/api/secrets/"+x.ID+"/authorize", auth, nil)
	got := woke(t, done, "批准", "替换", "Token", "旧值已作废", "授权没有改动", "旧 Token 已在 GitHub 删除")
	if strings.Contains(got, newTok) || strings.Contains(got, oldTok) {
		t.Fatalf("答复里不应有值：%s", got)
	}
	det = get(x.ID)
	y := det.Secret
	if y.ID != x.ID || y.Name != name || y.RequestPending || y.ExpiresAt == nil || !y.ExpiresAt.Equal(exp) || !slices.Equal(y.Allowed, []string{"gh *"}) {
		t.Fatalf("凭据和授权延续：%+v", y)
	}
	if !strings.Contains(y.Fields[0].How, "Fine-grained") {
		t.Fatalf("新的获取方式写进字段说明：%+v", y.Fields[0])
	}
	if env := use(); env["GITHUB_TOKEN"] != newTok || env["GITHUB_USER"] != user {
		t.Fatalf("新值生效、旧值作废：%v", env)
	}
	var replaceLog *store.SecretLog
	uses, edits := 0, 0
	for i, l := range det.Logs {
		switch l.Kind {
		case store.SecretLogReplace:
			replaceLog = &det.Logs[i]
		case store.SecretLogUse:
			uses++
		case store.SecretLogEdit:
			edits++
		}
	}
	if replaceLog == nil || replaceLog.Reason != "Token" || replaceLog.SessionKey != key || replaceLog.Note != "旧 Token 已在 GitHub 删除" {
		t.Fatalf("时间线记替换了哪些值、申请的会话和附言：%+v", replaceLog)
	}
	if edits != 0 || uses < 2 {
		t.Fatalf("只替换值不另记修改授权；取用记录延续（%d 条）", uses)
	}

	// 4. 脱敏：已存记录里的旧值继续隐去，新值也隐去；库里没有旧值明文
	if got := e.session(key).LastPrompt; strings.Contains(got, oldTok) {
		t.Fatalf("已存记录里的旧值继续隐去：%q", got)
	}
	if got := prompt("a2", "a2 "+oldTok+" / "+newTok); got != "a2 "+hub.SecretRedacted+" / "+hub.SecretRedacted {
		t.Fatalf("旧值和新值都隐去：%q", got)
	}
	var retired string
	if err := e.store.DB().QueryRow(`SELECT retired FROM secrets WHERE id = ?`, x.ID).Scan(&retired); err != nil {
		t.Fatal(err)
	}
	if retired == "" || strings.Contains(retired, oldTok) {
		t.Fatalf("旧值加密存放：%q", retired)
	}

	// 5. 凭据页手动替换值：敏感旧值同样留下；不敏感的旧值照常不隐去
	const third = "ghp_thirdTokenCCCC3333"
	e.call("POST", "/api/secrets/"+x.ID+"/values", map[string]any{"values": map[string]string{"GITHUB_TOKEN": third, "GITHUB_USER": newUser}}, &det)
	if det.Logs[0].Kind != store.SecretLogReplace || det.Logs[0].Reason != "Token、用户名" {
		t.Fatalf("手动替换值记替换了哪些值：%+v", det.Logs[0])
	}
	if got := prompt("a3", "a3 "+oldTok+" "+newTok+" "+third+" "+user); got != "a3 "+strings.Repeat(hub.SecretRedacted+" ", 3)+user {
		t.Fatalf("三个敏感值都隐去、不敏感的旧值不隐去：%q", got)
	}

	// 6. 替换值并修改授权
	rq, err = amend(map[string]any{"reason": "换 Token，顺带允许 git push", "add_allowed": []string{"git push *"}, "replace": []map[string]any{{"env": "GITHUB_TOKEN"}}})
	if err != nil {
		t.Fatal(err)
	}
	if rq.Decision.Question != name+"：替换值并修改授权" {
		t.Fatalf("标题：%s", rq.Decision.Question)
	}
	x = get(rq.Decision.Code).Secret
	if x.Ask.Replace[0].How != "" {
		t.Fatalf("没写获取方式 = 沿用原来的：%+v", x.Ask.Replace)
	}
	done = parkWait(t, e, c, key)
	e.call("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{"projects": x.Ask.Projects, "days": -1, "allowed": x.Ask.Allowed, "values": map[string]string{"GITHUB_TOKEN": "ghp_fourthTokenDDDD4444"}}, nil)
	woke(t, done, "替换", "加上「git push *」")
	det = get(x.ID)
	if det.Logs[0].Kind != store.SecretLogEdit || det.Logs[1].Kind != store.SecretLogReplace || !strings.Contains(det.Logs[0].Reason, "git push") {
		t.Fatalf("替换值和改授权各记一条：%+v %+v", det.Logs[0], det.Logs[1])
	}
	if !strings.Contains(det.Secret.Fields[0].How, "Fine-grained") {
		t.Fatal("没写新的获取方式时保留原来的")
	}
}
