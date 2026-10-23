package hub

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 公网暴露下的防护：识别真实客户端 IP，并对登录失败限流。

const (
	loginMaxFails = 5
	loginLockFor  = 15 * time.Minute
	loginFailTTL  = time.Hour // 超过该时长无新失败则清零计数
)

type clientInfo struct {
	IP string
}

const clientKey ctxKey = 2

func clientFrom(ctx context.Context) clientInfo {
	c, _ := ctx.Value(clientKey).(clientInfo)
	return c
}

// withClient 记录请求的真实来源。只有直连地址是本机或内网（即自己的反向代理）时，
// 才采信 X-Real-IP / X-Forwarded-For；否则直接用 TCP 对端地址，防止公网客户端伪造。
func withClient(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientKey, resolveClient(r))))
	})
}

func resolveClient(r *http.Request) clientInfo {
	ip := remoteIP(r.RemoteAddr)
	c := clientInfo{IP: ip}
	if !trustedProxy(ip) {
		return c
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(v) != nil {
		c.IP = v
	} else if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// 取最后一跳：由我们自己的代理追加，客户端无法伪造。
		parts := strings.Split(xff, ",")
		if v := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(v) != nil {
			c.IP = v
		}
	}
	return c
}

func remoteIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func trustedProxy(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && (p.IsLoopback() || p.IsPrivate() || p.IsLinkLocalUnicast())
}

// loginLimiter 按客户端 IP 限制登录尝试：窗口内最多 loginMaxFails 次失败，超出后锁定 loginLockFor。
// 每次尝试在校验密码之前就先计数，并发请求也无法突破上限；登录成功则清零。
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginEntry
	now     func() time.Time
}

type loginEntry struct {
	attempts    int
	last        time.Time
	lockedUntil time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: map[string]*loginEntry{}, now: time.Now}
}

// attempt 登记一次尝试。允许时返回 0；被锁定时返回剩余锁定时长。
func (l *loginLimiter) attempt(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.gcLocked(now)
	e := l.entries[ip]
	if e == nil || (now.Sub(e.last) > loginFailTTL && !now.Before(e.lockedUntil)) {
		e = &loginEntry{}
		l.entries[ip] = e
	}
	if d := e.lockedUntil.Sub(now); d > 0 {
		return d
	}
	if e.attempts >= loginMaxFails {
		if e.lockedUntil.IsZero() {
			// 前面的尝试还在校验中就已用完次数（并发请求）：直接锁定。
			e.lockedUntil = now.Add(loginLockFor)
			return loginLockFor
		}
		// 上一轮锁定已到期：重新计数。
		e.attempts = 0
		e.lockedUntil = time.Time{}
	}
	e.attempts++
	e.last = now
	return 0
}

// locked 只查看是否在锁定中、还剩多久，不计入尝试（passkey 登录的“开始”一步用）。
func (l *loginLimiter) locked(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[ip]; e != nil {
		return max(e.lockedUntil.Sub(l.now()), 0)
	}
	return 0
}

// failed 记录这次尝试失败；达到上限时开始锁定，返回是否刚进入锁定。
func (l *loginLimiter) failed(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[ip]
	if e == nil || e.attempts < loginMaxFails || !e.lockedUntil.IsZero() {
		return false
	}
	e.lockedUntil = l.now().Add(loginLockFor)
	return true
}

func (l *loginLimiter) success(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}

func (l *loginLimiter) gcLocked(now time.Time) {
	for ip, e := range l.entries {
		if now.Sub(e.last) > loginFailTTL && !now.Before(e.lockedUntil) {
			delete(l.entries, ip)
		}
	}
}
