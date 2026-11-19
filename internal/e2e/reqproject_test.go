package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-358：ks req create --project / req_create 的 project 给别的项目记需求。
// 按项目名或 git remote 找；找不到、重名时报错并列出可选项目，不记进当前项目；
// 不指定时照旧记进会话所在项目并关联会话；记进别的项目时不关联会话，时间线写明由哪个会话、从哪个项目记的。
func TestReqCreateProject(t *testing.T) {
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
	roll2 := repo("https://github.com/other/rollcall.git") // 同名项目
	for sid, dir := range map[string]string{"rp-alpha": alpha, "rp-roll": roll, "rp-roll2": roll2} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": dir, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("三个会话都归了项目", func() bool {
		return e.session("claude:rp-alpha").ProjectID != "" && e.session("claude:rp-roll").ProjectID != "" && e.session("claude:rp-roll2").ProjectID != ""
	})
	pAlpha, pRoll, pRoll2 := e.session("claude:rp-alpha").ProjectID, e.session("claude:rp-roll").ProjectID, e.session("claude:rp-roll2").ProjectID
	if pRoll == pRoll2 || pAlpha == pRoll {
		t.Fatal("三个仓库应是三个项目")
	}

	c := client.New(30 * time.Second)
	ctx := context.Background()
	create := func(body map[string]any) (reqWithNotices, error) {
		var r reqWithNotices
		body["session_key"] = "claude:rp-alpha"
		err := c.Do(ctx, "POST", "/agent/requirements", body, &r)
		return r, err
	}
	count := func() int {
		var out struct {
			Requirements []store.Requirement `json:"requirements"`
		}
		e.call("GET", "/api/requirements", nil, &out)
		return len(out.Requirements)
	}

	// 1. 不指定：记进会话所在项目，关联会话，时间线不写来源项目
	r, err := create(map[string]any{"title": "本项目的需求"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ProjectID != pAlpha || !linked(r.Requirement, "claude:rp-alpha") {
		t.Fatalf("不指定时应记进会话所在项目并关联：%+v", r.Requirement)
	}
	if c := createdChange(t, e, r.Code); c.Reason != "" {
		t.Fatalf("同项目新建不该写来源：%q", c.Reason)
	}
	// 指定的就是会话所在项目：同不指定
	if r, err = create(map[string]any{"title": "写明本项目", "project": "alpha"}); err != nil || r.ProjectID != pAlpha || !linked(r.Requirement, "claude:rp-alpha") {
		t.Fatalf("指定本项目应同不指定：%v %+v", err, r.Requirement)
	}

	// 2. 按 git remote 指定别的项目：记进去、不关联会话，时间线写明由哪个会话、从哪个项目记的
	for _, q := range []string{"https://github.com/acme/rollcall.git", "acme/rollcall", "github.com/ACME/rollcall"} {
		r, err = create(map[string]any{"title": "给 rollcall 记的 " + q, "project": q})
		if err != nil {
			t.Fatalf("%s：%v", q, err)
		}
		if r.ProjectID != pRoll || r.ProjectName != "rollcall" {
			t.Fatalf("%s 应记进 acme/rollcall：%+v", q, r.Requirement)
		}
		if linked(r.Requirement, "claude:rp-alpha") {
			t.Fatalf("记进别的项目不该关联当前会话：%+v", r.Sessions)
		}
		if len(r.Notices) == 0 || !strings.Contains(r.Notices[len(r.Notices)-1], "记进了项目「rollcall」") {
			t.Fatalf("返回应提示记进了哪个项目：%v", r.Notices)
		}
		ch := createdChange(t, e, r.Code)
		if ch.Session != "claude:rp-alpha" || ch.Reason != "来自项目「alpha」" {
			t.Fatalf("时间线应写明会话和来源项目：%+v", ch)
		}
	}
	// 按项目 ID 也认
	if r, err = create(map[string]any{"title": "按 ID", "project": pRoll2}); err != nil || r.ProjectID != pRoll2 {
		t.Fatalf("按项目 ID 指定：%v %+v", err, r.Requirement)
	}

	// 3. 重名：报错并列出这些项目，不记下
	n := count()
	_, err = create(map[string]any{"title": "重名", "project": "rollcall"})
	if err == nil || !strings.Contains(err.Error(), "「rollcall」对应多个项目") || !strings.Contains(err.Error(), "github.com/acme/rollcall") || !strings.Contains(err.Error(), "github.com/other/rollcall") {
		t.Fatalf("重名应报错并列出两个项目：%v", err)
	}
	// 4. 找不到：报错并列出可选项目，不记下
	_, err = create(map[string]any{"title": "找不到", "project": "nope"})
	if err == nil || !strings.Contains(err.Error(), "找不到项目「nope」") || !strings.Contains(err.Error(), "「alpha」（github.com/acme/alpha）") {
		t.Fatalf("找不到应报错并列出可选项目：%v", err)
	}
	if got := count(); got != n {
		t.Fatalf("报错时不该记下需求：%d → %d", n, got)
	}
	// 重名里有隐藏的（误识别的目录）：排除它，剩一个就用它
	e.call("PUT", "/api/projects/"+pRoll2, map[string]any{"name": "rollcall", "doc_root": "", "hidden": true}, nil)
	if r, err = create(map[string]any{"title": "隐藏的不算重名", "project": "rollcall"}); err != nil || r.ProjectID != pRoll {
		t.Fatalf("排除隐藏项目后应唯一：%v %+v", err, r.Requirement)
	}

	// 5. 命令行：ks req create --project
	bin := filepath.Join(t.TempDir(), ksExe())
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/ks").CombinedOutput(); err != nil {
		t.Fatalf("编译 ks 失败：%v\n%s", err, out)
	}
	env := []string{"CLAUDE_CODE_SESSION_ID=rp-alpha"}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE_CODE_SESSION_ID=") && !strings.HasPrefix(kv, "DSH_") {
			env = append(env, kv)
		}
	}
	ks := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env, cmd.Dir = env, roll // cd 到别的项目目录也不改变会话所在项目
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := ks("req", "create", "--title", "命令行记的", "--project", "acme/rollcall")
	if err != nil || !strings.Contains(out, `"project_name": "rollcall"`) {
		t.Fatalf("ks req create --project：%v\n%s", err, out)
	}
	out, err = ks("req", "create", "--title", "命令行找不到", "--project", "nope")
	if err == nil || !strings.Contains(out, "找不到项目「nope」") {
		t.Fatalf("找不到应以非零状态退出并列出项目：%v\n%s", err, out)
	}
	out, err = ks("req", "create", "--title", "命令行不指定")
	if err != nil || !strings.Contains(out, `"project_name": "alpha"`) {
		t.Fatalf("不指定时记进会话所在项目（不看当前目录）：%v\n%s", err, out)
	}
}

type reqWithNotices struct {
	store.Requirement
	Notices []string `json:"notices"`
}

func linked(r store.Requirement, key string) bool {
	for _, s := range r.Sessions {
		if s == key {
			return true
		}
	}
	return false
}

func createdChange(t *testing.T, e *env, code string) store.StatusChange {
	t.Helper()
	var r store.Requirement
	e.call("GET", "/api/requirements/"+code, nil, &r)
	for _, c := range r.StatusChanges {
		if c.Via == "create" {
			return c
		}
	}
	t.Fatalf("%s 没有新建记录", code)
	return store.StatusChange{}
}
