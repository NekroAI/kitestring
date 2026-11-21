package e2e

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
)

// R-379：团队消息跨项目发。主调度 / 主助理用 project 给其他项目的会话发，to 按对方项目的身份（含轻接入）或会话键寻址；
// 对方看到来源项目和发送者；其他身份被拒；字数提示、只告知 / 要动手两档照旧；
// 回复送回原项目的发送方；ks team sent 查得到送达状态并标出发往哪个项目。
func TestTellCrossProject(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	e := setup(t)
	repo := func(remote string) string {
		t.Helper()
		dir, err := os.MkdirTemp(sockTmp(), "ks-repo-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		gitRun(t, dir, "init", "-q", "-b", "main")
		gitRun(t, dir, "remote", "add", "origin", remote)
		return dir
	}
	alpha := repo("git@github.com:acme/alpha.git")
	roll := repo("https://github.com/acme/rollcall.git")
	for sid, dir := range map[string]string{"xa-pm": alpha, "xa-aide": alpha, "xa-dev": alpha, "xr-lite": roll, "xr-none": roll} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": dir, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话都归了项目", func() bool {
		for _, k := range []string{"xa-pm", "xa-aide", "xa-dev", "xr-lite", "xr-none"} {
			if e.session("claude:"+k).ProjectID == "" {
				return false
			}
		}
		return true
	})
	pAlpha, pRoll := e.session("claude:xa-pm").ProjectID, e.session("claude:xr-lite").ProjectID
	if pAlpha == pRoll {
		t.Fatal("两个仓库应是两个项目")
	}
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for k, r := range map[string]string{"claude:xa-pm": "pm", "claude:xa-aide": "aide", "claude:xa-dev": "dev", "claude:xr-lite": "lite"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	tell := func(body map[string]any) (tellRes, error) {
		var out tellRes
		err := c.Do(ctx, "POST", "/agent/team/tell", body, &out)
		return out, err
	}
	wantErr := func(body map[string]any, want ...string) {
		t.Helper()
		_, err := tell(body)
		if err == nil {
			t.Fatalf("应报错：%v", body)
		}
		for _, w := range want {
			if !strings.Contains(err.Error(), w) {
				t.Fatalf("报错应含「%s」：%v", w, err)
			}
		}
	}

	// 其他身份跨项目发：被拒并说明原因；不填 project 点名其他项目的会话也被拒
	wantErr(map[string]any{"session_key": "claude:xa-dev", "text": "资源库命名不一致", "project": "rollcall", "to": "lite"}, "只有主调度、主助理")
	wantErr(map[string]any{"session_key": "claude:xa-dev", "text": "资源库命名不一致", "to": "claude:xr-lite"}, "不属于本项目", "只有主调度、主助理")
	// 主助理不填 project 点名其他项目的会话：提示加 --project
	wantErr(map[string]any{"session_key": "claude:xa-aide", "text": "资源库命名不一致", "to": "claude:xr-lite"}, "--project rollcall")
	// 找不到项目
	wantErr(map[string]any{"session_key": "claude:xa-aide", "text": "x", "project": "nosuch", "to": "lite"}, "找不到项目「nosuch」", "消息没有送出")
	// 对方项目没有该身份的在线会话：报错并列出可送达的会话键
	wantErr(map[string]any{"session_key": "claude:xa-aide", "text": "x", "project": "rollcall", "to": "pm"}, "项目「rollcall」现在没有在线的主调度", "claude:xr-lite", "claude:xr-none")

	// 1. 主助理按身份（轻接入）发给其他项目：对方挂着 ks wait 被唤醒，看到来源项目和发送者
	done := parkWait(t, e, c, "claude:xr-lite")
	out, err := tell(map[string]any{"session_key": "claude:xa-aide", "text": "资源库命名和本项目不一致，请对齐", "project": "rollcall", "to": "轻接入"})
	if err != nil || !out.Delivered || out.To != "claude:xr-lite" || !strings.Contains(out.Message, "项目「rollcall」的轻接入") {
		t.Fatalf("应跨项目送给轻接入：%+v %v", out, err)
	}
	d, err := e.store.GetDecision(ctx, out.Code)
	if err != nil {
		t.Fatal(err)
	}
	if d.ProjectID != pRoll || d.AssignedBy != "claude:xa-aide" || !strings.Contains(d.Answer, "项目「alpha」的「") || !strings.Contains(d.Answer, "（主助理）claude:xa-aide") || !strings.Contains(d.Answer, "claude:xa-aide") {
		t.Fatalf("消息应记在对方项目、写明来源项目和发送者：%+v", d)
	}
	woke(t, done, "项目「alpha」", "claude:xa-aide", "请对齐", "ks team tell --reply "+out.Code)

	// 2. 回复送回原项目的发送方，注明来源项目；回复时填 project 报错
	wantErr(map[string]any{"session_key": "claude:xr-lite", "text": "好", "reply_to": out.Code, "project": "alpha"}, "回复不用填项目")
	var rep tellRes
	if rep, err = tell(map[string]any{"session_key": "claude:xr-lite", "text": "已对齐成 assets", "reply_to": out.Code}); err != nil || rep.To != "claude:xa-aide" {
		t.Fatalf("回复应送回原项目的主助理：%+v %v", rep, err)
	}
	rd, _ := e.store.GetDecision(ctx, rep.Code)
	if rd.ProjectID != pAlpha || rd.ReplyTo != d.ID || !strings.Contains(rd.Answer, "项目「rollcall」的") || !strings.Contains(rd.Answer, "回复 "+out.Code) {
		t.Fatalf("回复应记在原项目并写明来源：%+v", rd)
	}

	// 3. ks team sent 查得到跨项目消息的送达状态，标出发往哪个项目
	var sent struct {
		Text     string `json:"text"`
		Messages []struct {
			Code       string `json:"code"`
			Project    string `json:"project"`
			Recipients []struct {
				To     string `json:"to"`
				Status string `json:"status"`
			} `json:"recipients"`
		} `json:"messages"`
	}
	if err := c.Do(ctx, "GET", "/agent/team/sent?"+url.Values{"session_key": {"claude:xa-aide"}, "ref": {out.Code}}.Encode(), nil, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Messages) != 1 || sent.Messages[0].Project != "rollcall" || sent.Messages[0].Recipients[0].To != "claude:xr-lite" ||
		sent.Messages[0].Recipients[0].Status != "delivered" || !strings.Contains(sent.Text, "发往项目「rollcall」") {
		t.Fatalf("team sent 应查到跨项目消息已送达：%+v", sent)
	}

	// 4. 主调度按会话键发给其他项目没身份的会话；只告知不唤醒；超字数照常送出并提示
	long := strings.Repeat("资源库命名", 30)
	out, err = tell(map[string]any{"session_key": "claude:xa-pm", "text": long, "project": "rollcall", "to": "claude:xr-none", "fyi": true})
	if err != nil || out.To != "claude:xr-none" || !strings.Contains(out.Message, "只告知") || !strings.Contains(out.Message, "字数提示") {
		t.Fatalf("主调度应能按会话键跨项目发只告知的消息，并提示字数：%+v %v", out, err)
	}
	if d, _ = e.store.GetDecision(ctx, out.Code); !d.FYI || d.ProjectID != pRoll || !strings.Contains(d.Answer, "项目「alpha」的「") || !strings.Contains(d.Answer, "（主调度）claude:xa-pm") {
		t.Fatalf("只告知的跨项目消息：%+v", d)
	}
	// project 填的就是本项目：同不填
	if out, err = tell(map[string]any{"session_key": "claude:xa-aide", "text": "R-1 看一下", "project": "alpha", "to": "pm"}); err != nil || out.To != "claude:xa-pm" || strings.Contains(out.Message, "项目「") {
		t.Fatalf("project 填本项目应同不填：%+v %v", out, err)
	}
}
