package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

func (e *env) post(t *testing.T, name string, data []byte) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", name)
	part.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := e.web.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (e *env) download(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := e.web.Get(e.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// 附件不限类型（R-221）：任意文件都能上传；图片 / 录屏在页面里显示，其它文件（含 HTML、SVG）一律按下载返回、禁止脚本。
func TestAnyFileAttachment(t *testing.T) {
	e := setup(t)
	gesture := []byte(`{"events":[{"t":0,"dx":12}]}`)
	cases := []struct {
		name, mime string
		data       []byte
	}{
		{"gesture.json", "application/json", gesture},
		{"run.log", "text/plain; charset=utf-8", []byte("line 1\nline 2\n")},
		{"page.html", "text/html; charset=utf-8", []byte("<html><script>alert(document.cookie)</script></html>")},
		{"logo.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`)},
		{"bundle.zip", "application/zip", []byte("PK\x03\x04rest")},
		// 扩展名冒充图片的 HTML：不能按图片在页面里打开
		{"evil.png", "text/html; charset=utf-8", []byte("<html><script>alert(1)</script></html>")},
	}
	for _, c := range cases {
		f := e.upload(t, c.name, c.data)
		if f.Mime != c.mime || f.Name != c.name || f.Size != int64(len(c.data)) {
			t.Fatalf("%s 的附件信息不对：%+v", c.name, f)
		}
		resp, body := e.download(t, "/api/files/"+f.ID)
		cd := resp.Header.Get("Content-Disposition")
		if !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, c.name) {
			t.Fatalf("%s 应按下载返回并带原文件名：%q", c.name, cd)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
			t.Fatalf("%s 应禁止脚本：%q", c.name, csp)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Type") != c.mime || !bytes.Equal(body, c.data) {
			t.Fatalf("%s 下载内容或类型不对：%v %q", c.name, resp.Header, body)
		}
	}

	// 中文文件名：按 RFC 2231 编码，原名不丢
	f := e.upload(t, "手势日志.json", gesture)
	resp, _ := e.download(t, "/api/files/"+f.ID)
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "filename*=utf-8''%E6%89%8B") {
		t.Fatalf("中文文件名应编码在 Content-Disposition 里：%q", cd)
	}

	// 图片照旧在页面里显示；带 ?download 时按下载
	img := e.upload(t, "shot.png", tinyPNG)
	resp, _ = e.download(t, "/api/files/"+img.ID)
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "inline") || resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("Content-Security-Policy") != "" {
		t.Fatalf("图片应在页面里显示：%v", resp.Header)
	}
	resp, _ = e.download(t, "/api/files/"+img.ID+"?download=1")
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("?download 应按下载返回：%q", cd)
	}
}

// 其它文件的大小上限可在设置里调（默认 20 MB），超出给中文提示（R-221）。
func TestFileUploadLimit(t *testing.T) {
	e := setup(t)
	var cur map[string]any
	e.call("GET", "/api/settings", nil, &cur)
	if cur["upload_file_mb"] != float64(20) {
		t.Fatalf("其它文件默认上限应为 20 MB：%v", cur["upload_file_mb"])
	}
	delete(cur, "notify_events")
	delete(cur, "summary_api_key_set")
	cur["upload_file_mb"] = 1
	e.call("PUT", "/api/settings", cur, &cur)
	if cur["upload_file_mb"] != float64(1) {
		t.Fatalf("上限没保存：%v", cur["upload_file_mb"])
	}
	big := append([]byte("PK\x03\x04"), make([]byte, 2<<20)...)
	if code, body := e.post(t, "big.zip", big); code != http.StatusRequestEntityTooLarge || !strings.Contains(body, "文件不能超过 1 MB") {
		t.Fatalf("超过上限应被拒绝并给中文提示：%d %s", code, body)
	}
	// 1 MB 以内照常
	if code, body := e.post(t, "small.zip", big[:512<<10]); code != 200 {
		t.Fatalf("上限以内应能上传：%d %s", code, body)
	}
	// 请求本身超过所有上限：读到一半就拒绝，也给中文提示
	cur["upload_image_mb"], cur["upload_video_mb"] = 1, 1
	delete(cur, "notify_events")
	delete(cur, "summary_api_key_set")
	e.call("PUT", "/api/settings", cur, &cur)
	if code, body := e.post(t, "huge.zip", append(big, make([]byte, 2<<20)...)); code != http.StatusRequestEntityTooLarge || !strings.Contains(body, "文件太大") {
		t.Fatalf("超大请求应被拒绝并给中文提示：%d %s", code, body)
	}
}

// Agent 能拿到用户附的文件（R-221）：留言附 JSON → 送达时给出本机路径；需求详情（req_get / ks req get）里每个附件带本机路径。
func TestAgentGetsUserFile(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	prompt := func(turn string) string {
		return hook(t, "claude", map[string]any{"session_id": "af-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": turn})
	}
	prompt("p0")
	c := client.New(5 * time.Second)
	var r store.Requirement
	if err := c.Do(context.Background(), "POST", "/agent/requirements", map[string]any{"title": "手势返工", "status": "in_progress", "session_key": "claude:af-1"}, &r); err != nil {
		t.Fatal(err)
	}
	log := []byte(`{"gestures":[{"kind":"swipe","dx":-80}]}`)
	f := e.upload(t, "gesture-log.json", log)
	e.call("POST", "/api/requirements/"+r.Code+"/notes", map[string]any{"comment": "手势日志见附件", "files": []string{f.ID}}, nil)

	// 送达：注入里写明文件名和本机路径
	var out string
	e.waitFor("留言送达", func() bool { out = prompt("p1"); return strings.Contains(out, "手势日志见附件") })
	m := regexp.MustCompile(`文件「gesture-log\.json」：(\S+gesture-log\.json)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("注入应包含文件的本机路径：%q", out)
	}
	if b, err := os.ReadFile(m[1]); err != nil || !bytes.Equal(b, log) {
		t.Fatalf("文件应已下载到本机：%v", err)
	}

	// 需求详情：附件带本机路径（证据里 Agent 自己交的不下载）
	var doc any
	if err := c.Do(context.Background(), "GET", "/agent/requirements/"+r.Code, nil, &doc); err != nil {
		t.Fatal(err)
	}
	c.LocalizeFiles(context.Background(), doc)
	raw, _ := json.Marshal(doc)
	var got struct {
		Feedback []struct {
			AnswerFiles []struct {
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"answer_files"`
		} `json:"feedback"`
	}
	json.Unmarshal(raw, &got)
	var path string
	for _, fb := range got.Feedback {
		for _, af := range fb.AnswerFiles {
			if af.Name == "gesture-log.json" {
				path = af.Path
			}
		}
	}
	if path == "" {
		t.Fatalf("需求详情里的附件应带本机路径：%s", raw)
	}
	if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, log) {
		t.Fatalf("按路径应能读到用户附的文件：%v", err)
	}
}
