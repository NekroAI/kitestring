package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 选项写在位置参数前面或后面都要生效，用不上的选项要报错（R-279）。
// 起一个假的 daemon（unix socket），记下收到的请求路径和 JSON 体。
type fakeDaemon struct {
	mu   sync.Mutex
	reqs []map[string]any
	path []string
}

func startFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ks279")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("KS_HOME", dir)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("DSH_SESSION_ID", "")
	ln, err := net.Listen("unix", filepath.Join(dir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		if strings.HasPrefix(r.URL.Path, "/agent/") {
			d.mu.Lock()
			d.reqs = append(d.reqs, m)
			d.path = append(d.path, r.URL.Path)
			d.mu.Unlock()
		}
		w.Write([]byte(`{}`))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return d
}

func (d *fakeDaemon) last(t *testing.T) (string, map[string]any) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.reqs) == 0 {
		t.Fatal("daemon 没收到请求")
	}
	return d.path[len(d.path)-1], d.reqs[len(d.reqs)-1]
}

func TestOptionsBeforeOrAfterPositional(t *testing.T) {
	d := startFakeDaemon(t)
	cases := []struct {
		name string
		run  func([]string) error
		args [][]string
		path string
		want map[string]any
	}{
		{"req status", runReq, [][]string{
			{"status", "R-1", "blocked", "--on", "R-2", "--reason", "等它"},
			{"status", "--on", "R-2", "--reason", "等它", "R-1", "blocked"},
			{"status", "R-1", "--on", "R-2", "blocked", "--reason", "等它"},
		}, "/agent/requirements/R-1/status", map[string]any{"status": "blocked", "waiting_on": "R-2", "reason": "等它"}},
		{"req comment", runReq, [][]string{
			{"comment", "R-1", "一句留言", "--session", "claude:t1"},
			{"comment", "--session", "claude:t1", "R-1", "一句留言"},
		}, "/agent/requirements/R-1/comments", map[string]any{"text": "一句留言", "session_key": "claude:t1"}},
		{"team tell", runTeam, [][]string{
			{"tell", "你好", "--to", "aide", "--req", "R-9", "--fyi", "--session", "claude:t1"},
			{"tell", "--to", "aide", "--req", "R-9", "--fyi", "--session", "claude:t1", "你好"},
		}, "/agent/team/tell", map[string]any{"text": "你好", "to": "aide", "requirement": "R-9", "fyi": true}},
	}
	for _, c := range cases {
		for i, args := range c.args {
			if err := c.run(args); err != nil {
				t.Fatalf("%s 写法 %d 出错：%v", c.name, i, err)
			}
			path, body := d.last(t)
			if path != c.path {
				t.Fatalf("%s 写法 %d 路径 %s", c.name, i, path)
			}
			for k, v := range c.want {
				if body[k] != v {
					t.Fatalf("%s 写法 %d：%s 应为 %v，实际 %v（整体 %v）", c.name, i, k, v, body[k], body)
				}
			}
		}
	}
}

func TestStrayOptionsRejected(t *testing.T) {
	d := startFakeDaemon(t)
	bad := []struct {
		run  func([]string) error
		args []string
	}{
		{runReq, []string{"comment", "R-1", "hi", "--on", "R-2", "--session", "claude:t1"}},
		{runReq, []string{"status", "R-1", "ready", "--verdict", "x"}},
		{runReq, []string{"status", "R-1", "ready", "--nope"}},
		{runTeam, []string{"tell", "hi", "--role", "dev", "--session", "claude:t1"}},
		{runTeam, []string{"sent", "--fyi", "--session", "claude:t1"}},
		{runPlan, []string{"get", "P-1", "--file", "x.json"}},
		{runCommit, []string{"list", "R-1", "--url", "x"}},
	}
	for _, c := range bad {
		if err := c.run(c.args); err == nil {
			t.Fatalf("%v 应报错", c.args)
		}
	}
	if len(d.reqs) != 0 {
		t.Fatalf("报错的命令不该发出请求：%v", d.path)
	}
}
