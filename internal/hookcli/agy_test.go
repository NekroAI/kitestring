package hookcli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/protocol"
)

func TestCollectEnv_AgyWhitelistAndNoSecrets(t *testing.T) {
	t.Setenv("ANTIGRAVITY_AGENT", "1")
	t.Setenv("ANTIGRAVITY_APP_DATA_DIR", "/tmp/agy-app-data")
	t.Setenv("ANTIGRAVITY_CSRF_TOKEN", "secret-csrf-token")
	t.Setenv("ANTIGRAVITY_LS_ADDRESS", "127.0.0.1:42100")

	env := collectEnv()
	if env["ANTIGRAVITY_AGENT"] != "1" {
		t.Fatalf("expected ANTIGRAVITY_AGENT=1, got %q", env["ANTIGRAVITY_AGENT"])
	}
	if env["ANTIGRAVITY_APP_DATA_DIR"] != "/tmp/agy-app-data" {
		t.Fatalf("expected ANTIGRAVITY_APP_DATA_DIR, got %q", env["ANTIGRAVITY_APP_DATA_DIR"])
	}
	for _, forbidden := range []string{"ANTIGRAVITY_CSRF_TOKEN", "ANTIGRAVITY_LS_ADDRESS"} {
		if _, ok := env[forbidden]; ok {
			t.Fatalf("forbidden secret env %s must not be collected", forbidden)
		}
	}
}

func TestRun_AgyFallbacksWhenDaemonUnavailable(t *testing.T) {
	tmp := shortTempDir(t)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("KS_HOME", filepath.Join(tmp, ".kitestring"))

	cases := []struct {
		name     string
		args     []string
		stdin    string
		wantKey  string
		wantVal  string
		wantJSON string
	}{
		{
			name:    "PreToolUse fallback allow",
			args:    []string{protocol.ToolAgy, "PreToolUse"},
			stdin:   `{"conversationId":"cid-1","toolName":"read_file"}`,
			wantKey: "decision",
			wantVal: "allow",
		},
		{
			name:    "Stop fallback allow",
			args:    []string{protocol.ToolAgy, "Stop"},
			stdin:   `{"conversationId":"cid-1"}`,
			wantKey: "decision",
			wantVal: "allow",
		},
		{
			name:     "SessionStart fallback empty json",
			args:     []string{protocol.ToolAgy, "SessionStart"},
			stdin:    `{"conversationId":"cid-1"}`,
			wantJSON: "{}",
		},
		{
			name:    "Invalid JSON still falls back for PreToolUse",
			args:    []string{protocol.ToolAgy, "PreToolUse"},
			stdin:   `{bad`,
			wantKey: "decision",
			wantVal: "allow",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			code := Run(tc.args, strings.NewReader(tc.stdin), &out)
			if code != 0 {
				t.Fatalf("expected exit 0, got %d", code)
			}
			var got map[string]any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("stdout must be valid JSON, got %q: %v", out.String(), err)
			}
			if tc.wantJSON != "" {
				if strings.TrimSpace(out.String()) != tc.wantJSON {
					t.Fatalf("expected %s, got %q", tc.wantJSON, out.String())
				}
				return
			}
			if got[tc.wantKey] != tc.wantVal {
				t.Fatalf("expected %s=%q, got %#v", tc.wantKey, tc.wantVal, got)
			}
		})
	}
}

func TestRun_AgyDaemonResponses(t *testing.T) {
	tmp := shortTempDir(t)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	ksHome := filepath.Join(tmp, "ks")
	t.Setenv("KS_HOME", ksHome)

	sock := daemon.SocketPath()
	if !strings.HasPrefix(sock, tmp) {
		t.Fatalf("unexpected socket path outside temp dir: %s", sock)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var nextResp protocol.HookResponse
	var gotReq protocol.HookRequest
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		_ = json.NewEncoder(w).Encode(nextResp)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	// 1. Stop guard -> decision=continue + reason
	nextResp = protocol.HookResponse{Block: "还有未确认需求"}
	var out bytes.Buffer
	code := Run([]string{protocol.ToolAgy, ""}, strings.NewReader(`{"hookEventName":"Stop","conversationId":"c1"}`), &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if gotReq.Event != "Stop" {
		t.Fatalf("expected hookEventName fallback to set Event=Stop, got %q (stdout=%q)", gotReq.Event, out.String())
	}
	var stopOut map[string]any
	if err := json.Unmarshal(out.Bytes(), &stopOut); err != nil {
		t.Fatal(err)
	}
	if stopOut["decision"] != "continue" || stopOut["reason"] != "还有未确认需求" {
		t.Fatalf("unexpected Stop continue payload: %#v", stopOut)
	}

	// 2. PreInvocation context -> injectSteps.systemMessage
	nextResp = protocol.HookResponse{Context: "来自团队的消息"}
	out.Reset()
	code = Run([]string{protocol.ToolAgy, "PreInvocation"}, strings.NewReader(`{"conversationId":"c1"}`), &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	var injectOut struct {
		InjectSteps []struct {
			SystemMessage struct {
				SystemMessage string `json:"systemMessage"`
			} `json:"systemMessage"`
		} `json:"injectSteps"`
	}
	if err := json.Unmarshal(out.Bytes(), &injectOut); err != nil {
		t.Fatal(err)
	}
	if len(injectOut.InjectSteps) != 1 || injectOut.InjectSteps[0].SystemMessage.SystemMessage != "来自团队的消息" {
		t.Fatalf("unexpected injectSteps output: %s", out.String())
	}

	// 3. PreToolUse empty response -> decision=allow
	nextResp = protocol.HookResponse{}
	out.Reset()
	code = Run([]string{protocol.ToolAgy, "PreToolUse"}, strings.NewReader(`{"conversationId":"c1"}`), &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	var preOut map[string]any
	if err := json.Unmarshal(out.Bytes(), &preOut); err != nil {
		t.Fatal(err)
	}
	if preOut["decision"] != "allow" {
		t.Fatalf("expected PreToolUse decision=allow, got %#v", preOut)
	}

	// 4. SessionStart accepts injectSteps when daemon returns Context
	nextResp = protocol.HookResponse{Context: "欢迎回来"}
	out.Reset()
	code = Run([]string{protocol.ToolAgy, "SessionStart"}, strings.NewReader(`{"conversationId":"c1"}`), &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	injectOut.InjectSteps = nil
	if err := json.Unmarshal(out.Bytes(), &injectOut); err != nil {
		t.Fatal(err)
	}
	if len(injectOut.InjectSteps) != 1 || injectOut.InjectSteps[0].SystemMessage.SystemMessage != "欢迎回来" {
		t.Fatalf("SessionStart should output injectSteps, got: %s", out.String())
	}

	// 5. PreToolUse / PostToolUse / Stop must NEVER emit injectSteps (AGY protojson rejects it)
	for _, tc := range []struct {
		event        string
		resp         protocol.HookResponse
		wantDecision string
	}{
		{"PreToolUse", protocol.HookResponse{Context: "旧终端提醒"}, "allow"},
		{"PreToolUse", protocol.HookResponse{Block: "ignored-non-stop"}, "allow"},
		{"PostToolUse", protocol.HookResponse{Context: "旧终端提醒"}, ""},
		{"Stop", protocol.HookResponse{Context: "旧终端提醒"}, "allow"},
	} {
		nextResp = tc.resp
		out.Reset()
		code = Run([]string{protocol.ToolAgy, tc.event}, strings.NewReader(`{"conversationId":"c1"}`), &out)
		if code != 0 {
			t.Fatalf("%s expected exit 0, got %d", tc.event, code)
		}
		if strings.Contains(out.String(), "injectSteps") {
			t.Fatalf("%s must not emit injectSteps, got: %s", tc.event, out.String())
		}
		var m map[string]any
		if err := json.Unmarshal(out.Bytes(), &m); err != nil {
			t.Fatalf("%s output not valid JSON: %q: %v", tc.event, out.String(), err)
		}
		if tc.wantDecision == "" {
			if len(m) != 0 {
				t.Fatalf("%s expected empty JSON {}, got %#v", tc.event, m)
			}
		} else if m["decision"] != tc.wantDecision {
			t.Fatalf("%s expected decision=%q, got %#v", tc.event, tc.wantDecision, m)
		}
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	base := "/tmp"
	if runtime.GOOS == "windows" {
		base = ""
	}
	dir, err := os.MkdirTemp(base, "ks-hook-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
