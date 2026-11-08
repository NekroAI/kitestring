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

// 凭据的值可标「不敏感」（R-318）：
//   - 申请可声明某个值不敏感；默认敏感；同名凭据再申请不能把已有的值改成不敏感；
//   - 保存授权时、凭据页（/plain）可以改，改动记在时间线上；
//   - 不敏感的值在凭据接口里给出明文，不进全局脱敏；敏感的值照旧不返回、照旧隐去；
//   - 注入命令的规则不变：两个值都放进环境变量，命令里出现时只隐去敏感的；
//   - 库里 fields 列不存明文；老数据（没有 plain 字段）按敏感处理。
func TestSecretPlainField(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	const key = "claude:plain"
	hook(t, "claude", map[string]any{"session_id": "plain", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "建管理员账号", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session(key).ProjectID != "" })
	projectID := e.session(key).ProjectID

	const user, pass = "miose-administrator", "pw-9f8e7d6c5b4a"
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
	field := func(x store.Secret, env string) store.SecretField {
		t.Helper()
		for _, f := range x.Fields {
			if f.Env == env {
				return f
			}
		}
		t.Fatalf("没有 %s：%+v", env, x.Fields)
		return store.SecretField{}
	}

	// 1. 申请：用户名声明不敏感，密码不写（默认敏感）
	var rq struct {
		Decision store.Decision `json:"decision"`
	}
	if err := c.Do(ctx, "POST", "/agent/secrets/request", map[string]any{
		"session_key": key, "name": "官网管理员账号", "purpose": "建 /admin 后台的管理员", "days": 1, "allowed": []string{"node scripts/admin.mjs *"},
		"fields": []map[string]any{
			{"env": "ADMIN_USERNAME", "label": "用户名", "plain": true},
			{"env": "ADMIN_PASSWORD", "label": "密码"},
		},
	}, &rq); err != nil {
		t.Fatal(err)
	}
	x := get(rq.Decision.Code).Secret
	if !field(x, "ADMIN_USERNAME").Plain || field(x, "ADMIN_PASSWORD").Plain {
		t.Fatalf("申请声明的敏感标记应带进凭据：%+v", x.Fields)
	}

	// 2. 保存并授权：两个值都填
	e.call("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{
		"values": map[string]string{"ADMIN_USERNAME": user, "ADMIN_PASSWORD": pass}, "projects": []string{projectID}, "days": 1, "allowed": []string{"node scripts/admin.mjs *"},
	}, nil)
	d := get(x.ID)
	if f := field(d.Secret, "ADMIN_USERNAME"); f.Value != user {
		t.Fatalf("不敏感的值应给出明文：%+v", f)
	}
	if f := field(d.Secret, "ADMIN_PASSWORD"); f.Value != "" {
		t.Fatalf("敏感的值不应返回：%+v", f)
	}
	var list struct {
		Secrets []store.Secret `json:"secrets"`
	}
	e.call("GET", "/api/secrets", nil, &list)
	if len(list.Secrets) != 1 || field(list.Secrets[0], "ADMIN_USERNAME").Value != user || field(list.Secrets[0], "ADMIN_PASSWORD").Value != "" {
		t.Fatalf("凭据页列表也给出不敏感的明文：%+v", list.Secrets)
	}
	// 库里 fields 列不存明文
	var raw string
	if err := e.store.DB().QueryRow(`SELECT fields FROM secrets WHERE id = ?`, x.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, user) || strings.Contains(raw, pass) {
		t.Fatalf("fields 列不应有明文：%s", raw)
	}

	// 3. 不进全局脱敏：留言里出现用户名照常显示，密码照旧隐去
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "管理员账号", "status": "in_progress", "session_key": key}, nil); err != nil {
		t.Fatal(err)
	}
	hook(t, "claude", map[string]any{"session_id": "plain", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "用户 " + user + " 密码 " + pass, "prompt_id": "p1"})
	e.waitFor("消息入库", func() bool { return strings.Contains(e.session(key).LastPrompt, "密码") })
	if got := e.session(key).LastPrompt; got != "用户 "+user+" 密码 "+hub.SecretRedacted {
		t.Fatalf("只隐去敏感的值：%q", got)
	}

	// 4. 注入命令的规则不变：两个值都进环境；命令里只隐去敏感的；越界照旧拒绝
	var u struct {
		LogID string            `json:"log_id"`
		Env   map[string]string `json:"env"`
	}
	if err := c.Do(ctx, "POST", "/agent/secrets/use", map[string]any{"session_key": key, "name": "官网管理员账号", "argv": []string{"node", "scripts/admin.mjs", "create", user, pass}, "program": "/usr/local/bin/node", "via_path": true}, &u); err != nil {
		t.Fatal(err)
	}
	if u.Env["ADMIN_USERNAME"] != user || u.Env["ADMIN_PASSWORD"] != pass {
		t.Fatalf("两个值都应放进环境变量：%v", u.Env)
	}
	if err := c.Do(ctx, "POST", "/agent/secrets/use", map[string]any{"session_key": key, "name": "官网管理员账号", "argv": []string{"curl", "x"}, "program": "/usr/bin/curl", "via_path": true}, nil); err == nil || !strings.Contains(err.Error(), "不在允许范围内") {
		t.Fatalf("不敏感不放宽注入规则：%v", err)
	}
	d = get(x.ID)
	for _, l := range d.Logs {
		if l.Kind == store.SecretLogUse && l.Result == store.SecretRunning {
			if l.Command != "/usr/local/bin/node scripts/admin.mjs create "+user+" "+hub.SecretRedacted {
				t.Fatalf("命令里只隐去敏感的值：%q", l.Command)
			}
		}
	}

	// 5. 同名凭据再申请不能把已有的值改成不敏感；新加的值可以声明
	if err := c.Do(ctx, "POST", "/agent/secrets/request", map[string]any{
		"session_key": key, "name": "官网管理员账号", "purpose": "加邮箱",
		"fields": []map[string]any{{"env": "ADMIN_PASSWORD", "plain": true}, {"env": "ADMIN_EMAIL", "label": "邮箱", "plain": true}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	x = get(x.ID).Secret
	if field(x, "ADMIN_PASSWORD").Plain || !field(x, "ADMIN_EMAIL").Plain {
		t.Fatalf("再申请只能给新加的值声明不敏感：%+v", x.Fields)
	}
	// 授权时把邮箱改回敏感
	e.call("POST", "/api/secrets/"+x.ID+"/authorize", map[string]any{
		"values": map[string]string{"ADMIN_EMAIL": "admin@example.org"}, "projects": []string{projectID}, "days": 1, "allowed": []string{"node scripts/admin.mjs *"},
		"plain": map[string]bool{"ADMIN_EMAIL": false},
	}, nil)
	if f := field(get(x.ID).Secret, "ADMIN_EMAIL"); f.Plain || f.Value != "" {
		t.Fatalf("授权时改成敏感应生效：%+v", f)
	}

	// 6. 凭据页上改：密码标为不敏感 → 明文、不再隐去；改回去 → 又隐去；值和授权不变，时间线有记录
	e.call("POST", "/api/secrets/"+x.ID+"/plain", map[string]any{"plain": map[string]bool{"ADMIN_PASSWORD": true}}, nil)
	d = get(x.ID)
	if f := field(d.Secret, "ADMIN_PASSWORD"); !f.Plain || f.Value != pass || d.Secret.Status != store.SecretActive {
		t.Fatalf("标为不敏感后应给出明文、授权不变：%+v %s", f, d.Secret.Status)
	}
	if got := e.session(key).LastPrompt; strings.Contains(got, hub.SecretRedacted) {
		t.Fatalf("标为不敏感后不再隐去：%q", got)
	}
	e.call("POST", "/api/secrets/"+x.ID+"/plain", map[string]any{"plain": map[string]bool{"ADMIN_PASSWORD": false}}, nil)
	if got := e.session(key).LastPrompt; !strings.Contains(got, hub.SecretRedacted) {
		t.Fatalf("改回敏感后又隐去：%q", got)
	}
	var marks []string
	for _, l := range get(x.ID).Logs {
		if l.Kind == store.SecretLogPlain || l.Kind == store.SecretLogSensitive {
			marks = append(marks, l.Kind+":"+l.Reason)
		}
	}
	if strings.Join(marks, ",") != "sensitive:密码,plain:密码,sensitive:邮箱" {
		t.Fatalf("改敏感标记应记在时间线上（新的在前）：%v", marks)
	}
	if code, body := e.try("POST", "/api/secrets/"+x.ID+"/plain", map[string]any{"plain": map[string]bool{"NOPE": true}}); code != http.StatusBadRequest || !strings.Contains(body, "NOPE") {
		t.Fatalf("没有的值应报错：%d %s", code, body)
	}
	// 替换值对话框里只改标记也能保存，不记「替换了值」
	e.call("POST", "/api/secrets/"+x.ID+"/values", map[string]any{"values": map[string]string{}, "plain": map[string]bool{"ADMIN_USERNAME": false}}, nil)
	d = get(x.ID)
	if f := field(d.Secret, "ADMIN_USERNAME"); f.Plain || f.Value != "" || !f.Filled {
		t.Fatalf("只改标记：值保留、改为敏感：%+v", f)
	}
	if d.Logs[0].Kind != store.SecretLogSensitive {
		t.Fatalf("只改标记不记替换值：%+v", d.Logs[0])
	}

	// 7. 老数据：fields 里没有 plain 的按敏感处理
	if _, err := e.store.DB().Exec(`UPDATE secrets SET fields = ? WHERE id = ?`, `[{"env":"ADMIN_USERNAME","label":"用户名","filled":true},{"env":"ADMIN_PASSWORD","label":"密码","filled":true},{"env":"ADMIN_EMAIL","label":"邮箱","filled":true}]`, x.ID); err != nil {
		t.Fatal(err)
	}
	for _, f := range get(x.ID).Secret.Fields {
		if f.Plain || f.Value != "" {
			t.Fatalf("老数据应按敏感处理：%+v", f)
		}
	}
	// 重建脱敏器（改一次再改回）：用户名也参与隐去
	e.call("POST", "/api/secrets/"+x.ID+"/plain", map[string]any{"plain": map[string]bool{"ADMIN_EMAIL": true}}, nil)
	e.call("POST", "/api/secrets/"+x.ID+"/plain", map[string]any{"plain": map[string]bool{"ADMIN_EMAIL": false}}, nil)
	if got := e.session(key).LastPrompt; got != "用户 "+hub.SecretRedacted+" 密码 "+hub.SecretRedacted {
		t.Fatalf("老数据的值都参与隐去：%q", got)
	}
}
