package e2e

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/store"
)

// 纸鸢凭据库（R-295）：
//   - 会话申请凭据 → 请求类审批（request_kind = secret）进「待处理」；通用作答接口不能直接批准；
//   - 网页填值保存授权 → 申请方立即收到「批准」和用法，答复里没有值；
//   - ks secret run 取值：授权有效、项目匹配、命令在允许范围内才给值；过期、撤销、越界、别的项目、未授权写入磁盘一律拒绝并说明原因；
//   - 每次取用（含被拒）都有记录：会话、需求、命令、结果；命令里的已知值存成「[已隐去]」；越界被拒不进待处理；
//   - 会话记录（消息、留言）展示前脱敏；
//   - 凭据接口不对未登录、无来源页面（沙箱里的原型）、跨站请求和访客路由开放；
//   - 拒绝申请 → 申请方收到拒绝，从没填过值的凭据随之删除。
func TestSecretVault(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	const key = "claude:sec"
	hook(t, "claude", map[string]any{"session_id": "sec", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "部署官网", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session(key).ProjectID != "" })
	projectID := e.session(key).ProjectID
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "官网迁到 Cloudflare Pages", "status": "in_progress", "session_key": key}, &r); err != nil {
		t.Fatal(err)
	}

	const token, account = "cf-token-AbCdEf0123456789", "acct-5f3e2d1c0b"
	type reqOut struct {
		Decision store.Decision `json:"decision"`
		Existing bool           `json:"existing"`
		Message  string         `json:"message"`
	}
	type useOut struct {
		LogID string            `json:"log_id"`
		Env   map[string]string `json:"env"`
	}
	type detail struct {
		Secret store.Secret      `json:"secret"`
		Logs   []store.SecretLog `json:"logs"`
	}
	use := func(sess, name string, argv ...string) (useOut, error) {
		var out useOut
		err := c.Do(ctx, "POST", "/agent/secrets/use", map[string]any{"session_key": sess, "name": name, "argv": argv}, &out)
		return out, err
	}
	denied := func(err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("应拒绝并说明「%s」：%v", want, err)
		}
	}
	get := func(ref string) detail {
		t.Helper()
		var d detail
		e.call("GET", "/api/secrets/"+ref, nil, &d)
		return d
	}
	pendingCount := func() int {
		var out struct {
			Decisions []store.Decision `json:"decisions"`
		}
		e.call("GET", "/api/decisions?status=pending", nil, &out)
		return len(out.Decisions)
	}

	// 1. 申请：写明用途、关联需求、建议有效期、允许的命令、每个值的获取方式
	var rq reqOut
	if err := c.Do(ctx, "POST", "/agent/secrets/request", map[string]any{
		"session_key": key, "name": "Cloudflare 部署", "purpose": "用 wrangler 把官网部署到 Cloudflare Pages", "requirement": r.Code, "days": 7,
		"allowed": []string{"wrangler pages deploy *", "wrangler whoami"},
		"fields": []map[string]string{
			{"env": "CLOUDFLARE_API_TOKEN", "label": "API Token", "how": "Cloudflare 控制台 → 我的个人资料 → API 令牌，模板选「编辑 Cloudflare Workers」"},
			{"env": "CLOUDFLARE_ACCOUNT_ID", "label": "Account ID", "how": "控制台右侧栏「账户 ID」"},
		},
	}, &rq); err != nil {
		t.Fatal(err)
	}
	d := rq.Decision
	if d.RequestKind != store.RequestSecret || !d.Approval || d.Status != store.DecPending || d.RequirementCode != r.Code || !strings.Contains(d.Question, "API Token 和 Account ID") {
		t.Fatalf("申请应是一条进待处理的请求类审批：%+v", d)
	}
	if !strings.Contains(rq.Message, "ks secret run") {
		t.Fatalf("回执应写明之后怎么用：%s", rq.Message)
	}
	before := pendingCount()
	det := get(d.Code)
	x := det.Secret
	if x.Status != store.SecretPending || !x.RequestPending || x.RequestCode != d.Code || x.Ask == nil || x.Ask.Days != 7 || len(x.Ask.Allowed) != 2 || x.Fields[0].How == "" {
		t.Fatalf("申请的凭据应为待填写、带建议：%+v", x)
	}
	// 重复申请不重复建
	var again reqOut
	if err := c.Do(ctx, "POST", "/agent/secrets/request", map[string]any{"session_key": key, "name": "cloudflare 部署", "purpose": "x", "fields": []map[string]string{{"env": "CLOUDFLARE_API_TOKEN"}}}, &again); err != nil || !again.Existing {
		t.Fatalf("已有等待中的申请时不重复建：%+v %v", again, err)
	}
	// 还没填写 → 拒绝
	_, err := use(key, "Cloudflare 部署", "wrangler", "whoami")
	denied(err, "未填写值")
	// 通用作答接口不能直接批准（要填值）
	if code, body := e.try("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d.Code, "answer": "批准", "verdict": "approved"}}}); code != http.StatusBadRequest || !strings.Contains(body, "保存并授权") {
		t.Fatalf("凭据申请不能直接批准：%d %s", code, body)
	}
	// 没填全不能授权
	if code, body := e.try("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{"values": map[string]string{"CLOUDFLARE_API_TOKEN": token}, "projects": []string{projectID}, "days": 7}); code != http.StatusBadRequest || !strings.Contains(body, "Account ID") {
		t.Fatalf("每个值都要填：%d %s", code, body)
	}

	// 2. 网页上填值、保存并授权 → 申请方立即收到
	done := parkWait(t, e, c, key)
	e.call("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{
		"values": map[string]string{"CLOUDFLARE_API_TOKEN": token, "CLOUDFLARE_ACCOUNT_ID": account}, "projects": []string{projectID}, "days": 7,
		"allowed": []string{"wrangler pages deploy *", "wrangler whoami"},
	}, nil)
	got := woke(t, done, "批准", "Cloudflare 部署", "ks secret run", "CLOUDFLARE_API_TOKEN")
	if strings.Contains(got, token) || strings.Contains(got, account) {
		t.Fatalf("送达的答复里不应有值：%s", got)
	}
	x = get(x.ID).Secret
	if x.Status != store.SecretActive || x.RequestPending || x.ExpiresAt == nil || x.Disk || !x.Fields[0].Filled || !x.Fields[1].Filled {
		t.Fatalf("授权后应为生效中：%+v", x)
	}

	// 3. 允许的命令：拿到值、上报结果
	u, err := use(key, "Cloudflare 部署", "wrangler", "whoami")
	if err != nil || u.Env["CLOUDFLARE_API_TOKEN"] != token || u.Env["CLOUDFLARE_ACCOUNT_ID"] != account {
		t.Fatalf("允许的命令应拿到值：%+v %v", u, err)
	}
	if err := c.Do(ctx, "POST", "/agent/secrets/uses/"+u.LogID+"/finish", map[string]any{"exit_code": 0}, nil); err != nil {
		t.Fatal(err)
	}
	u2, err := use(key, "Cloudflare 部署", "/opt/bin/wrangler", "pages", "deploy", "dist", "--project-name", "kite")
	if err != nil {
		t.Fatalf("程序名按文件名比、* 跟任意参数：%v", err)
	}
	if err := c.Do(ctx, "POST", "/agent/secrets/uses/"+u2.LogID+"/finish", map[string]any{"exit_code": 1}, nil); err != nil {
		t.Fatal(err)
	}
	// 越界：命令不在允许范围内（命令里带着值）
	_, err = use(key, "Cloudflare 部署", "curl", "-H", "Authorization: Bearer "+token, "https://api.cloudflare.com/client/v4/user")
	denied(err, "不在允许范围内")
	if strings.Contains(err.Error(), token) {
		t.Fatalf("拒绝说明里不应有值：%v", err)
	}
	// 写入磁盘没授权
	err = c.Do(ctx, "POST", "/agent/secrets/use", map[string]any{"session_key": key, "name": "Cloudflare 部署", "path": "/tmp/ks-secret-x.env"}, nil)
	denied(err, "未授权写入磁盘")
	// 别的项目的会话
	other := t.TempDir()
	hook(t, "claude", map[string]any{"session_id": "sec2", "cwd": other, "hook_event_name": "UserPromptSubmit", "prompt": "x", "prompt_id": "p0"})
	e.waitFor("另一个项目的会话入库", func() bool { s := e.session("claude:sec2"); return s.ProjectID != "" && s.ProjectID != projectID })
	_, err = use("claude:sec2", "Cloudflare 部署", "wrangler", "whoami")
	denied(err, "项目不在授权范围内")
	if pendingCount() != before-1 {
		t.Fatalf("越界被拒只记录，不进待处理：%d → %d", before, pendingCount())
	}

	// 4. 取用记录：时间、会话、需求、命令、结果；命令里的值已隐去
	det = get(x.ID)
	var ok, failed, outOfRange, grant, request bool
	for _, l := range det.Logs {
		if strings.Contains(l.Command, token) {
			t.Fatalf("记录里出现了明文：%+v", l)
		}
		switch {
		case l.Kind == store.SecretLogUse && l.Result == store.SecretOK:
			ok = l.SessionKey == key && l.RequirementCode == r.Code && l.Command == "wrangler whoami"
		case l.Kind == store.SecretLogUse && l.Result == store.SecretFailed:
			failed = l.ExitCode == 1
		case l.Kind == store.SecretLogUse && l.Result == store.SecretDenied && l.Reason == "不在允许范围内":
			outOfRange = strings.Contains(l.Command, "Bearer "+hub.SecretRedacted)
		case l.Kind == store.SecretLogGrant:
			grant = l.SessionKey == key && l.Days == 7
		case l.Kind == store.SecretLogRequest:
			request = l.SessionKey == key && l.RequirementCode == r.Code
		}
	}
	if !ok || !failed || !outOfRange || !grant || !request {
		t.Fatalf("取用记录不全（ok %v failed %v 越界 %v 授权 %v 申请 %v）：%+v", ok, failed, outOfRange, grant, request, det.Logs)
	}
	if det.Secret.LastUse == nil || det.Secret.LastUse.Kind != store.SecretLogUse {
		t.Fatalf("应带最近一次取用：%+v", det.Secret.LastUse)
	}

	// 5. 会话记录展示前脱敏：会话把值写进了消息、留言
	hook(t, "claude", map[string]any{"session_id": "sec", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "token 是 " + token, "prompt_id": "p1"})
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/comments", map[string]any{"session_key": key, "text": "账户 " + account}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("消息入库", func() bool { return strings.Contains(e.session(key).LastPrompt, "token 是") })
	for _, path := range []string{"/api/sessions", "/api/sessions/" + key, "/api/requirements/" + r.Code} {
		code, body := e.try("GET", path, nil)
		if code != http.StatusOK || strings.Contains(body, token) || strings.Contains(body, account) {
			t.Fatalf("%s 展示前应脱敏：%d %s", path, code, body)
		}
	}
	if !strings.Contains(e.session(key).LastPrompt, "token 是 "+hub.SecretRedacted) {
		t.Fatalf("值应显示为已隐去：%q", e.session(key).LastPrompt)
	}

	// 6. 过期 → 拒绝；重新授权 → 恢复；撤销 → 拒绝；替换值 → 新值
	if _, err := e.store.DB().Exec(`UPDATE secrets SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), x.ID); err != nil {
		t.Fatal(err)
	}
	_, err = use(key, "Cloudflare 部署", "wrangler", "whoami")
	denied(err, "授权已到期")
	det = get(x.ID)
	if det.Secret.Status != store.SecretExpired || det.Logs[0].Kind == "" {
		t.Fatalf("应为已失效：%+v", det.Secret)
	}
	hasExpire := false
	for _, l := range det.Logs {
		hasExpire = hasExpire || l.Kind == "expire"
	}
	if !hasExpire {
		t.Fatal("时间线应记下到期")
	}
	e.call("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{"projects": []string{projectID}, "days": 0, "allowed": []string{"wrangler whoami"}, "disk": true}, nil)
	if x = get(x.ID).Secret; x.Status != store.SecretActive || x.ExpiresAt != nil || !x.Disk {
		t.Fatalf("重新授权（长期、可写入磁盘）：%+v", x)
	}
	_, err = use(key, "Cloudflare 部署", "wrangler", "pages", "deploy", "dist")
	denied(err, "不在允许范围内")
	if err := c.Do(ctx, "POST", "/agent/secrets/use", map[string]any{"session_key": key, "name": "Cloudflare 部署", "path": "/tmp/ks-secret-x.env"}, nil); err != nil {
		t.Fatalf("授权写入磁盘后可写：%v", err)
	}
	e.call("POST", "/api/secrets/"+x.ID+"/values", map[string]any{"values": map[string]string{"CLOUDFLARE_API_TOKEN": "cf-token-NEW-9876543210"}}, nil)
	if u, err := use(key, "Cloudflare 部署", "wrangler", "whoami"); err != nil || u.Env["CLOUDFLARE_API_TOKEN"] != "cf-token-NEW-9876543210" || u.Env["CLOUDFLARE_ACCOUNT_ID"] != account {
		t.Fatalf("替换值后取到新值、没替换的保留：%+v %v", u, err)
	}
	e.call("POST", "/api/secrets/"+x.ID+"/revoke", nil, nil)
	_, err = use(key, "Cloudflare 部署", "wrangler", "whoami")
	denied(err, "授权已撤回")
	kinds := map[string]bool{}
	for _, l := range get(x.ID).Logs {
		kinds[l.Kind] = true
		if l.Kind == store.SecretLogWrite && l.Path != "/tmp/ks-secret-x.env" {
			t.Fatalf("写入磁盘要记路径：%+v", l)
		}
	}
	for _, k := range []string{"request", "grant", "regrant", "replace", "revoke", "write", "use"} {
		if !kinds[k] {
			t.Fatalf("时间线缺少 %s：%v", k, kinds)
		}
	}
	// 列表：没有值
	code, body := e.try("GET", "/api/secrets", nil)
	if code != http.StatusOK || strings.Contains(body, account) || strings.Contains(body, "cf-token") || strings.Contains(body, "cipher") || !strings.Contains(body, "Cloudflare 部署") {
		t.Fatalf("列表不应带值：%d %s", code, body)
	}

	// 7. 凭据接口：未登录、无来源页面（沙箱里的原型）、跨站请求、访客路由一律拿不到
	anon := &http.Client{}
	for _, h := range []map[string]string{nil, {"Origin": "null"}, {"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "cors"}} {
		cl := e.web
		if h == nil {
			cl = anon
		}
		req, _ := http.NewRequest("GET", e.srv.URL+"/api/secrets", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%v 应拿不到凭据接口：%d", h, resp.StatusCode)
		}
	}
	for _, p := range []string{"/api/share/xxx/secrets", "/api/share/xxx/secrets/" + x.ID} {
		resp, err := anon.Get(e.srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			t.Fatalf("访客路由不应有凭据接口：%s → %d", p, resp.StatusCode)
		}
	}
	resp, err := anon.Post(e.srv.URL+"/api/agent/secrets/use", "application/json", strings.NewReader(`{"session_key":"claude:sec","name":"Cloudflare 部署","argv":["wrangler","whoami"]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("取值接口要设备 token：%d", resp.StatusCode)
	}

	// 8. 拒绝申请：申请方收到拒绝，从没填过值的凭据删除
	var rq2 reqOut
	if err := c.Do(ctx, "POST", "/agent/secrets/request", map[string]any{"session_key": key, "name": "Stripe 测试环境", "purpose": "支付回调联调",
		"fields": []map[string]string{{"env": "STRIPE_SECRET_KEY", "label": "Secret key", "how": "Stripe 控制台 → 开发者 → API 密钥（测试模式）"}}}, &rq2); err != nil {
		t.Fatal(err)
	}
	stripe := get(rq2.Decision.Code).Secret
	done = parkWait(t, e, c, key)
	e.call("POST", "/api/secrets/"+stripe.ID+"/reject", map[string]any{"note": "先用模拟数据"}, nil)
	woke(t, done, "拒绝", "先用模拟数据")
	if code, _ := e.try("GET", "/api/secrets/"+stripe.ID, nil); code != http.StatusNotFound {
		t.Fatalf("被拒绝、从没填过值的凭据应删除：%d", code)
	}
}
