package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// webTry 调网页接口，返回状态码与响应正文（不因失败中断测试）。
func (e *env) webTry(method, path string, in any) (int, string) {
	e.t.Helper()
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.web.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// 参谋授权窗口（R-163）：你开窗口（到期时间 + 范围）后，被授权的 主助理 能代答范围内的决策；
// 窗口外、到期后、撤销后、超出范围、硬规则、非 主助理、别的 主助理 都被拒；代答留痕；你推翻后更正送回提问方。
func TestProxyAnswerGrant(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "adv", "adv2", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	for k, r := range map[string]string{"claude:pm": "pm", "claude:adv": "aide", "claude:adv2": "advisor", "claude:dev": "dev"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	project := e.session("claude:dev").ProjectID
	raise := func(key string, body map[string]any) store.Decision {
		t.Helper()
		body["session_key"] = key
		var d store.Decision
		if err := c.Do(ctx, "POST", "/agent/decisions", body, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	answer := func(ref, key, ans, reason, risk string) (store.Decision, error) {
		var d store.Decision
		err := c.Do(ctx, "POST", "/agent/decisions/"+ref+"/answer", map[string]any{"session_key": key, "answer": ans, "reason": reason, "risk": risk}, &d)
		return d, err
	}
	mustFail := func(err error, want, what string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s：应报「%s」，实际 %v", what, want, err)
		}
	}
	getDec := func(ref string) store.Decision {
		var d store.Decision
		if err := c.Do(ctx, "GET", "/agent/decisions/"+ref, nil, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	openGrant := func(key, risk string, span time.Duration) store.Grant {
		t.Helper()
		var g store.Grant
		e.call("POST", "/api/grants", map[string]any{"project_id": project, "session_key": key, "max_risk": risk, "expires_at": time.Now().Add(span).Format(time.RFC3339Nano)}, &g)
		return g
	}

	d1 := raise("claude:dev", map[string]any{"question": "列表默认按创建时间倒序？", "risk": "low", "options": []string{"倒序", "正序"}})

	// 1. 窗口外：没开窗口时主助理代答被拒
	_, err := answer(d1.Code, "claude:adv", "倒序", "和看板一致", "low")
	mustFail(err, "没有开授权窗口", "没开窗口时代答")

	// 2. 开窗口：只能授权 主助理会话；到期时间要在将来、最长 24 小时
	if code, body := e.webTry("POST", "/api/grants", map[string]any{"project_id": project, "session_key": "claude:dev", "max_risk": "low", "expires_at": time.Now().Add(time.Hour)}); code != 400 || !strings.Contains(body, "主助理") {
		t.Fatalf("不能授权 Dev：%d %s", code, body)
	}
	if code, body := e.webTry("POST", "/api/grants", map[string]any{"project_id": project, "session_key": "claude:adv", "max_risk": "low", "expires_at": time.Now().Add(-time.Minute)}); code != 400 || !strings.Contains(body, "晚于现在") {
		t.Fatalf("到期时间在过去应被拒：%d %s", code, body)
	}
	if code, body := e.webTry("POST", "/api/grants", map[string]any{"project_id": project, "session_key": "claude:adv", "max_risk": "low", "expires_at": time.Now().Add(30 * time.Hour)}); code != 400 || !strings.Contains(body, "24 小时") {
		t.Fatalf("超过 24 小时应被拒：%d %s", code, body)
	}
	g := openGrant("claude:adv", "low", time.Hour)
	if g.Status != store.GrantActive || g.SessionKey != "claude:adv" {
		t.Fatalf("窗口应生效：%+v", g)
	}
	if code, body := e.webTry("POST", "/api/grants", map[string]any{"project_id": project, "session_key": "claude:adv2", "max_risk": "low", "expires_at": time.Now().Add(time.Hour)}); code != 400 || !strings.Contains(body, "只能授权本项目的主助理") {
		t.Fatalf("不能授权顾问（R-190）：%d %s", code, body)
	}
	if code, body := e.webTry("POST", "/api/grants", map[string]any{"project_id": project, "session_key": "claude:adv", "max_risk": "low", "expires_at": time.Now().Add(time.Hour)}); code != 400 || !strings.Contains(body, "先撤销") {
		t.Fatalf("同一项目同时只能有一个窗口：%d %s", code, body)
	}

	// 3. 非主助理、顾问 被拒；理由、风险判断必填
	_, err = answer(d1.Code, "claude:pm", "倒序", "和看板一致", "low")
	mustFail(err, "只有本项目的主助理", "PM 代答")
	_, err = answer(d1.Code, "claude:dev", "倒序", "和看板一致", "low")
	mustFail(err, "只有本项目的主助理", "Dev 代答")
	_, err = answer(d1.Code, "claude:adv2", "倒序", "和看板一致", "low")
	mustFail(err, "代答决策是主助理的职责", "顾问代答（R-190）")
	_, err = answer(d1.Code, "claude:adv", "倒序", "", "low")
	mustFail(err, "必须写理由", "不写理由")
	_, err = answer(d1.Code, "claude:adv", "倒序", "和看板一致", "")
	mustFail(err, "风险判断", "不写风险判断")

	// 4. 范围外：参谋自己判断为中风险（只升不降）、提问方没标风险档（按高）、中风险，都超出 low 窗口
	_, err = answer(d1.Code, "claude:adv", "倒序", "和看板一致", "medium")
	mustFail(err, "超出授权范围", "参谋判断升到中风险")
	dNone := raise("claude:dev", map[string]any{"question": "要不要加导出按钮？"})
	_, err = answer(dNone.Code, "claude:adv", "要", "用户提过", "low")
	mustFail(err, "按高风险算", "没标风险档")
	dMid := raise("claude:dev", map[string]any{"question": "分页大小 20 还是 50？", "risk": "medium"})
	_, err = answer(dMid.Code, "claude:adv", "20", "首屏更快", "low")
	mustFail(err, "超出授权范围", "中风险超出 low 窗口")

	// 5. 硬规则：批准方案、带效果图的设计选型、逐点决策里任一点带图、参谋自己提的
	dAppr := raise("claude:dev", map[string]any{"question": "批准这个方案？", "approval": true, "risk": "low"})
	_, err = answer(dAppr.Code, "claude:adv", "批准", "方案清楚", "low")
	mustFail(err, "批准方案", "代批准方案")
	shot := t.TempDir() + "/a.png"
	os.WriteFile(shot, tinyPNG, 0o600)
	var img store.FileRef
	if err := c.Upload(ctx, "/agent/files", shot, &img); err != nil {
		t.Fatal(err)
	}
	dImg := raise("claude:dev", map[string]any{"question": "选哪套配色？", "risk": "low", "options": []string{"A", "B"}, "option_images": []string{img.ID, ""}})
	_, err = answer(dImg.Code, "claude:adv", "A", "更清楚", "low")
	mustFail(err, "设计选型", "代选设计方案")
	dPts := raise("claude:dev", map[string]any{"question": "两处细节怎么定？", "risk": "low", "points": []map[string]any{
		{"question": "按钮文案", "options": []map[string]any{{"label": "保存"}, {"label": "提交"}}},
		{"question": "图标", "options": []map[string]any{{"label": "线框", "image": img.ID}, {"label": "实心"}}},
	}})
	_, err = answer(dPts.Code, "claude:adv", "", "都按推荐", "low")
	mustFail(err, "设计选型", "逐点决策任一点带图整体拒绝")
	dOwn := raise("claude:adv", map[string]any{"question": "参谋自己问的？", "risk": "low"})
	_, err = answer(dOwn.Code, "claude:adv", "好", "我自己定", "low")
	mustFail(err, "自己提的", "参谋代答自己提的")

	// 6. 窗口内、范围内：代答成功并留痕；提问方收到注明代答的答复
	done := parkWait(t, e, c, "claude:dev")
	got, err := answer(d1.Code, "claude:adv", "倒序", "和看板的默认排序一致", "low")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DecAnswered || got.ProxyBy != "claude:adv" || got.ProxyReason != "和看板的默认排序一致" || got.ProxyRisk != "low" || got.ProxyAnswer != "倒序" || got.ProxyAt == nil {
		t.Fatalf("代答应留痕：%+v", got)
	}
	woke(t, done, "（参谋代答）倒序", "和看板的默认排序一致")
	_, err = answer(d1.Code, "claude:adv", "正序", "改主意", "low")
	mustFail(err, "已经答复过", "已答复的不能再代答")
	var list struct {
		Grants  []store.Grant    `json:"grants"`
		Proxied []store.Decision `json:"proxied"`
	}
	e.call("GET", "/api/grants?project_id="+project, nil, &list)
	if len(list.Proxied) != 1 || list.Proxied[0].Code != d1.Code || len(list.Grants) != 1 || list.Grants[0].Proxied != 1 {
		t.Fatalf("代答记录应能查到：%+v", list)
	}

	// 7. 推翻：以你的为准，更正送回提问方；参谋也收到一句说明
	if code, body := e.webTry("POST", "/api/decisions/"+dMid.Code+"/override", map[string]any{"answer": "50"}); code != 400 || !strings.Contains(body, "不是参谋代答") {
		t.Fatalf("不是代答的不能推翻：%d %s", code, body)
	}
	woke(t, parkWait(t, e, c, "claude:adv"), "用户开了参谋授权窗口", "只告知") // 先收掉开窗口时送来的说明：只告知不叫醒（R-227），随之前提决策的事件一并送达
	done = parkWait(t, e, c, "claude:dev")
	advDone := parkWait(t, e, c, "claude:adv")
	e.call("POST", "/api/decisions/"+d1.Code+"/override", map[string]any{"answer": "正序"}, nil)
	woke(t, done, "更正：用户推翻了参谋的代答（原代答：倒序），以此为准：正序")
	drainQuiet(t, advDone, "参谋（被推翻只告知）", "adv", "被用户推翻", d1.Code)
	if d := getDec(d1.Code); d.OverriddenAt == nil || d.Answer != "正序" || d.ProxyAnswer != "倒序" || d.ProxyBy != "claude:adv" {
		t.Fatalf("推翻后应保留代答原文并记下推翻：%+v", d)
	}

	// 8. 撤销后被拒；撤销留痕
	var rg store.Grant
	e.call("POST", "/api/grants/"+g.Code+"/revoke", nil, &rg)
	if rg.Status != store.GrantRevoked || rg.RevokedAt == nil {
		t.Fatalf("应已撤销：%+v", rg)
	}
	d2 := raise("claude:dev", map[string]any{"question": "空状态写「暂无数据」？", "risk": "low"})
	_, err = answer(d2.Code, "claude:adv", "好", "通用写法", "low")
	mustFail(err, "没有开授权窗口", "撤销后代答")

	// 9. 到期自动失效：中风险窗口内能代答中风险；到期后被拒，状态为已到期
	openGrant("claude:adv", "medium", 1500*time.Millisecond)
	if _, err := answer(dMid.Code, "claude:adv", "20", "首屏更快", "medium"); err != nil {
		t.Fatalf("medium 窗口内应能代答中风险：%v", err)
	}
	time.Sleep(1600 * time.Millisecond)
	_, err = answer(d2.Code, "claude:adv", "好", "通用写法", "low")
	mustFail(err, "已到期", "到期后代答")
	e.call("GET", "/api/grants?project_id="+project, nil, &list)
	if len(list.Grants) != 2 || list.Grants[0].Status != store.GrantExpired || list.Grants[1].Status != store.GrantRevoked {
		t.Fatalf("开启、撤销、到期都应留在记录里：%+v", list.Grants)
	}
	if d := getDec(d2.Code); d.Status != store.DecPending {
		t.Fatalf("被拒的决策应仍等用户答复：%s", d.Status)
	}
}
