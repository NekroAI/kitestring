package hub

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

// 凭据（R-295）：允许的命令按词匹配；「*」单独放最后 = 后面任意参数，放中间 = 一个参数，词里的「*」匹配任意字符；
// 程序名按文件名比；空表 = 不限命令。
func TestCommandAllowed(t *testing.T) {
	allowed := []string{"wrangler pages deploy *", "wrangler whoami", "gh release * --repo=NekroAI/*"}
	cases := []struct {
		argv []string
		want bool
	}{
		{[]string{"wrangler", "pages", "deploy", "dist", "--project-name", "kite"}, true},
		{[]string{"wrangler", "pages", "deploy"}, true},
		{[]string{"/usr/local/bin/wrangler", "whoami"}, true},
		{[]string{"wrangler", "whoami", "--json"}, false},
		{[]string{"wrangler", "pages", "project", "delete"}, false},
		{[]string{"curl", "-H", "Authorization: Bearer x", "https://api.cloudflare.com"}, false},
		{[]string{"sh", "-c", "wrangler whoami"}, false},
		{[]string{"gh", "release", "create", "--repo=NekroAI/kitestring"}, true},
		{[]string{"gh", "release", "create", "--repo=other/kitestring"}, false},
		{[]string{"gh", "release", "--repo=NekroAI/kitestring"}, false},
	}
	for _, c := range cases {
		if got := commandAllowed(allowed, c.argv); got != c.want {
			t.Errorf("%v → %v，应为 %v", c.argv, got, c.want)
		}
	}
	if !commandAllowed(nil, []string{"anything"}) || commandAllowed(nil, nil) {
		t.Fatal("空表 = 不限命令（但要有命令）")
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
	st, err := os.Stat(keyPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("主密钥文件应为 0600：%v %v", st, err)
	}
	got, err := h.openSecret("S1", sealed)
	if err != nil || got["CLOUDFLARE_API_TOKEN"] != "tok-abcdef-123" || got["CLOUDFLARE_ACCOUNT_ID"] != "acct-987654" {
		t.Fatalf("解密不对：%v %v", got, err)
	}
	if _, err := h.openSecret("S2", sealed); err != errSecretUnreadable {
		t.Fatalf("密文挪到别的凭据上应解不开：%v", err)
	}
	// 重开（新的 Hub 读同一个主密钥文件）
	_ = os.Chmod(keyPath, 0o644)
	h2 := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h2.SetSecretKeyFile(keyPath)
	if got, err := h2.openSecret("S1", sealed); err != nil || got["CLOUDFLARE_API_TOKEN"] != "tok-abcdef-123" {
		t.Fatalf("重开后应能解开：%v %v", got, err)
	}
	if st, _ := os.Stat(keyPath); st.Mode().Perm() != 0o600 {
		t.Fatalf("权限宽了要收紧回 0600：%v", st.Mode())
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
