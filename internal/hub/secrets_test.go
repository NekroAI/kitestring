package hub

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/fsperm"
	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/store"
)

// 凭据（R-295）：允许的命令按词匹配；「*」单独放最后 = 后面任意参数，放中间 = 一个参数，词里的「*」匹配任意字符；空表 = 不限命令。
// 程序（D-1450 审查第 3 条）：规则写裸程序名时只认按 PATH 找到的那一个，写了路径的（./wrangler、/tmp/x/wrangler）不算；
// 规则写绝对路径时和解析出的路径精确比对。
func TestCommandAllowed(t *testing.T) {
	allowed := []string{"wrangler pages deploy *", "wrangler whoami", "/usr/bin/gh release * --repo=NekroAI/*"}
	viaPath := func(p string) program { return program{path: p, viaPath: true} }
	given := func(p string) program { return program{path: p} }
	wr, gh := viaPath("/opt/homebrew/bin/wrangler"), viaPath("/usr/bin/gh")
	cases := []struct {
		prog program
		argv []string
		want bool
	}{
		{wr, []string{"wrangler", "pages", "deploy", "dist", "--project-name", "kite"}, true},
		{wr, []string{"wrangler", "pages", "deploy"}, true},
		{wr, []string{"wrangler", "whoami"}, true},
		{given("/tmp/x/wrangler"), []string{"/tmp/x/wrangler", "whoami"}, false},
		{given("/home/u/proj/wrangler"), []string{"./wrangler", "whoami"}, false},
		{viaPath("/tmp/x/wrangler-evil"), []string{"wrangler-evil", "whoami"}, false},
		{wr, []string{"wrangler", "whoami", "--json"}, false},
		{wr, []string{"wrangler", "pages", "project", "delete"}, false},
		{viaPath("/usr/bin/curl"), []string{"curl", "-H", "Authorization: Bearer x", "https://api.cloudflare.com"}, false},
		{viaPath("/bin/sh"), []string{"sh", "-c", "wrangler whoami"}, false},
		{gh, []string{"gh", "release", "create", "--repo=NekroAI/kitestring"}, true},
		{given("/usr/bin/gh"), []string{"/usr/bin/gh", "release", "create", "--repo=NekroAI/kitestring"}, true},
		{viaPath("/opt/gh/gh"), []string{"gh", "release", "create", "--repo=NekroAI/kitestring"}, false},
		{gh, []string{"gh", "release", "create", "--repo=other/kitestring"}, false},
		{gh, []string{"gh", "release", "--repo=NekroAI/kitestring"}, false},
	}
	for _, c := range cases {
		if got := commandAllowed(allowed, c.argv, c.prog); got != c.want {
			t.Errorf("%v（%+v）→ %v，应为 %v", c.argv, c.prog, got, c.want)
		}
	}
	if !commandAllowed(nil, []string{"anything"}, given("/x")) || commandAllowed(nil, nil, program{}) {
		t.Fatal("空表 = 不限命令（但要有命令）")
	}
}

// Windows 设备解析出的路径（Hub 可能在别的系统上判定）：裸程序名不分大小写、可省 .exe / .cmd；
// 绝对路径的规则 / 与 \ 等同、不分大小写；写了路径的仍不算按 PATH 找到。
func TestCommandAllowedWindowsPaths(t *testing.T) {
	allowed := []string{"wrangler whoami", `C:/Tools/gh.exe release *`}
	cases := []struct {
		prog program
		argv []string
		want bool
	}{
		{program{path: `C:\Users\u\AppData\Roaming\npm\wrangler.cmd`, viaPath: true}, []string{"wrangler", "whoami"}, true},
		{program{path: `C:\Users\u\AppData\Roaming\npm\WRANGLER.CMD`, viaPath: true}, []string{"wrangler", "whoami"}, true},
		{program{path: `D:\proj\wrangler.cmd`}, []string{`.\wrangler`, "whoami"}, false},
		{program{path: `C:\x\wrangler-evil.exe`, viaPath: true}, []string{"wrangler-evil", "whoami"}, false},
		{program{path: `c:\tools\GH.exe`}, []string{`c:\tools\GH.exe`, "release", "list"}, true},
		{program{path: `C:\Tools2\gh.exe`}, []string{`C:\Tools2\gh.exe`, "release", "list"}, false},
	}
	for i, c := range cases {
		if got := commandAllowed(allowed, c.argv, c.prog); got != c.want {
			t.Errorf("第 %d 条 %v（%s）：得到 %v", i, c.argv, c.prog.path, got)
		}
	}
}

func TestCommandLine(t *testing.T) {
	got := commandLine([]string{"curl", "-H", `Authorization: Bearer "x"`, "https://a.b/c?d=1"})
	want := `curl -H "Authorization: Bearer \"x\"" "https://a.b/c?d=1"`
	if got != want {
		t.Fatalf("%s\n应为 %s", got, want)
	}
}

// 已知值：原文和 JSON 转义写法（含 HTML 转义）都换成「[已隐去]」；太短的值不参与。
func TestRedactor(t *testing.T) {
	r := newRedactor([]string{"tok_<secret>&1", "abc", "acct-1234567"})
	in := `原文 tok_<secret>&1；JSON "tok_<secret>&1"；另一个 acct-1234567；短的 abc 不隐去`
	got := r.Replace(in)
	if strings.Contains(got, "secret") || strings.Contains(got, "acct-1234567") || !strings.Contains(got, "abc") {
		t.Fatalf("脱敏不对：%s", got)
	}
	if strings.Count(got, SecretRedacted) != 3 {
		t.Fatalf("应隐去 3 处：%s", got)
	}
	if newRedactor([]string{"abc"}) != nil {
		t.Fatal("没有够长的值时不建脱敏器")
	}
}

// 加密：同一份值往返一致；密文换到另一条凭据上解不开；主密钥文件权限 0600、重开 Hub 后能解开；主密钥换了解不开。
func TestSecretSealing(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")
	h := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.SetSecretKeyFile(keyPath)
	vals := map[string]string{"CLOUDFLARE_API_TOKEN": "tok-abcdef-123", "CLOUDFLARE_ACCOUNT_ID": "acct-987654"}
	sealed, err := h.sealSecret("S1", vals)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "tok-abcdef") || !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("密文里不应有明文：%s", sealed)
	}
	if private, err := fsperm.IsPrivate(keyPath); err != nil || !private {
		t.Fatalf("主密钥文件应只有本人能读写：%v %v", private, err)
	}
	got, err := h.openSecret("S1", sealed)
	if err != nil || got["CLOUDFLARE_API_TOKEN"] != "tok-abcdef-123" || got["CLOUDFLARE_ACCOUNT_ID"] != "acct-987654" {
		t.Fatalf("解密不对：%v %v", got, err)
	}
	if _, err := h.openSecret("S2", sealed); err != errSecretUnreadable {
		t.Fatalf("密文挪到别的凭据上应解不开：%v", err)
	}
	// 重开（新的 Hub 读同一个主密钥文件）；Windows 上 Chmod 不放宽访问控制列表，只验证仍是本人专用
	_ = os.Chmod(keyPath, 0o644)
	h2 := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h2.SetSecretKeyFile(keyPath)
	if got, err := h2.openSecret("S1", sealed); err != nil || got["CLOUDFLARE_API_TOKEN"] != "tok-abcdef-123" {
		t.Fatalf("重开后应能解开：%v %v", got, err)
	}
	if private, _ := fsperm.IsPrivate(keyPath); !private {
		t.Fatal("权限宽了要收紧回只有本人能读写")
	}
	// 主密钥换了
	h3 := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h3.SetSecretKeyFile(filepath.Join(dir, "other.key"))
	if _, err := h3.openSecret("S1", sealed); err != errSecretUnreadable {
		t.Fatalf("主密钥换了应解不开：%v", err)
	}
}

// 数据库里只有密文：凭据存进库后，整个数据库文件里找不到明文。
func TestSecretNotInDatabase(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.SetFilesDir(filepath.Join(dir, "files"))
	ctx := t.Context()
	x, err := st.CreateSecret(ctx, store.Secret{Name: "甲", Fields: []store.SecretField{{Env: "TOKEN", Label: "Token"}}})
	if err != nil {
		t.Fatal(err)
	}
	if x.Cipher, err = h.sealSecret(x.ID, map[string]string{"TOKEN": "plain-value-0042"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSecret(ctx, x); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"hub.db", "hub.db-wal"} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		if strings.Contains(string(b), "plain-value-0042") {
			t.Fatalf("%s 里出现了明文", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.key")); err != nil {
		t.Fatalf("主密钥应在数据库旁的单独文件里：%v", err)
	}
}

// 脱敏出口：JSON 响应整段替换、事件流逐条替换；其它类型和取值接口不动。
func TestRedactWriter(t *testing.T) {
	rep := newRedactor([]string{"value-123456"})
	serve := func(ct, body string) string {
		rec := httptest.NewRecorder()
		rw := &redactWriter{ResponseWriter: rec, rep: rep}
		rw.Header().Set("Content-Type", ct)
		_, _ = rw.Write([]byte(body[:5]))
		_, _ = rw.Write([]byte(body[5:]))
		rw.finish()
		return rec.Body.String()
	}
	if got := serve("application/json", `{"a":"value-123456"}`); got != `{"a":"`+SecretRedacted+`"}` {
		t.Fatalf("JSON：%s", got)
	}
	if got := serve("text/event-stream", "data: x\n\ndata: value-123456\n\n"); strings.Contains(got, "value-123456") {
		t.Fatalf("事件流：%s", got)
	}
	if got := serve("image/png", "value-123456"); got != "value-123456" {
		t.Fatalf("其它类型应透传：%s", got)
	}
}

// 出口脱敏补漏（D-1450 审查第 2 条）：系统推送的内容、分享页 HTML 的标题与描述、设备长连接下行都隐去已知值。
func TestSecretRedactExits(t *testing.T) {
	e := newShareEnv(t)
	const val = "share-secret-0042abc"
	x, err := e.st.CreateSecret(e.ctx, store.Secret{Name: "甲", Fields: []store.SecretField{{Env: "TOKEN", Label: "Token", Filled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if x.Cipher, err = e.h.sealSecret(x.ID, map[string]string{"TOKEN": val}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SaveSecret(e.ctx, x); err != nil {
		t.Fatal(err)
	}
	e.h.refreshRedactor(e.ctx)
	// 系统推送
	if b := string(e.h.pushBody(notify.Message{Title: "新决策 D-1", Body: "token " + val}, "/")); strings.Contains(b, val) || !strings.Contains(b, SecretRedacted) {
		t.Fatalf("推送内容应隐去：%s", b)
	}
	// 设备长连接下行（JSON 帧）
	if b := string(e.h.redactBytes([]byte(`{"answer":"` + val + `"}`))); strings.Contains(b, val) {
		t.Fatalf("长连接下行应隐去：%s", b)
	}
	// 分享页 HTML 的标题与描述
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>纸鸢 Kitestring</title><meta name="description" content="x" /></head><body></body></html>`))
	})
	srv := httptest.NewServer(e.h.Handler(web))
	defer srv.Close()
	sh := e.createShare(map[string]any{"name": "部署 " + val, "links": []string{"甲"}, "items": []map[string]any{{"kind": "req", "ref": e.ra.Code}}})
	resp, err := http.Get(srv.URL + "/s/" + linkByName(sh, "甲").Token)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), val) || !strings.Contains(string(b), SecretRedacted) {
		t.Fatalf("分享页标题应隐去：%s", b)
	}
}

// 全量重拉时凭据页也要刷新（D-1450 审查第 5 条）。
func TestAllTopicsHasSecrets(t *testing.T) {
	if !strings.Contains(","+allTopics+",", ",secrets,") {
		t.Fatalf("allTopics 缺 secrets：%s", allTopics)
	}
}
