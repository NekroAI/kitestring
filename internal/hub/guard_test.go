package hub

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestLoginLimiterLocksAfterFiveFailures(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	l := newLoginLimiter()
	l.now = func() time.Time { return now }
	for i := 1; i <= loginMaxFails; i++ {
		if d := l.attempt("1.2.3.4"); d > 0 {
			t.Fatalf("第 %d 次尝试不应被锁", i)
		}
		locked := l.failed("1.2.3.4")
		if locked != (i == loginMaxFails) {
			t.Fatalf("第 %d 次失败后 locked=%v", i, locked)
		}
	}
	if d := l.attempt("1.2.3.4"); d <= 0 {
		t.Fatal("5 次失败后应锁定")
	}
	if d := l.attempt("5.6.7.8"); d > 0 {
		t.Fatal("其他 IP 不受影响")
	}
	now = now.Add(loginLockFor + time.Second)
	if d := l.attempt("1.2.3.4"); d > 0 {
		t.Fatal("锁定到期后应允许重试")
	}
}

func TestLoginLimiterSuccessResets(t *testing.T) {
	l := newLoginLimiter()
	for i := 0; i < loginMaxFails-1; i++ {
		l.attempt("ip")
		l.failed("ip")
	}
	l.attempt("ip")
	l.success("ip")
	for i := 0; i < loginMaxFails; i++ {
		if d := l.attempt("ip"); d > 0 {
			t.Fatal("登录成功后计数应清零")
		}
	}
}

// 并发请求在任何一次失败被记录之前涌入，也最多只能尝试 loginMaxFails 次。
func TestLoginLimiterConcurrentBurst(t *testing.T) {
	l := newLoginLimiter()
	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.attempt("burst") == 0 {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != loginMaxFails {
		t.Fatalf("并发下允许了 %d 次尝试，期望 %d", allowed, loginMaxFails)
	}
}

func TestResolveClientTrustsOnlyPrivateProxies(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "172.17.0.2:51234" // 容器里的反向代理
	r.Header.Set("X-Real-IP", "203.0.113.9")
	if ip := resolveClient(r).IP; ip != "203.0.113.9" {
		t.Fatalf("内网代理转发时应采信 X-Real-IP，得到 %s", ip)
	}
	r = httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "172.17.0.2:51234"
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 203.0.113.9")
	if ip := resolveClient(r).IP; ip != "203.0.113.9" {
		t.Fatalf("应取 X-Forwarded-For 最后一跳，得到 %s", ip)
	}
	r = httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "198.51.100.7:4000" // 公网直连
	r.Header.Set("X-Real-IP", "10.0.0.1")
	if ip := resolveClient(r).IP; ip != "198.51.100.7" {
		t.Fatalf("公网直连不应采信伪造的头，得到 %s", ip)
	}
}
