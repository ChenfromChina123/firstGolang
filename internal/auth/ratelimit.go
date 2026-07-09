package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// visitorEntry 单个 IP 的限流条目（含最后访问时间，用于 LRU 淘汰）
type visitorEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// trustedProxies 可信反向代理 IP 集合。
// 仅当请求直连来自这些 IP 时，才采信 X-Forwarded-For / X-Real-IP 头，
// 防止客户端伪造 XFF 绕过 IP 限流。未配置时直接用 RemoteAddr（最安全）。
var trustedProxies map[string]bool

// SetTrustedProxies 设置可信代理 IP 列表（启动时调用一次）。
// proxies 为空则不信任任何 XFF（所有请求按直连 IP 限流）。
func SetTrustedProxies(proxies []string) {
	trustedProxies = make(map[string]bool, len(proxies))
	for _, p := range proxies {
		p = strings.TrimSpace(p)
		if p != "" {
			trustedProxies[p] = true
		}
	}
}

// LoginRateLimiter 登录接口速率限制器
// 按 IP 限流，每个 IP 每分钟最多 5 次登录请求
type LoginRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitorEntry
	rps      rate.Limit
	burst    int
}

// NewLoginRateLimiter 创建登录速率限制器
// rps: 每秒允许的请求数（0.1 = 每 10 秒 1 次）
// burst: 突发上限
func NewLoginRateLimiter(rps float64, burst int) *LoginRateLimiter {
	return &LoginRateLimiter{
		visitors: make(map[string]*visitorEntry),
		rps:      rate.Limit(rps),
		burst:    burst,
	}
}

// getLimiter 获取或创建 IP 对应的限流器，并更新最后访问时间（LRU 用）
func (l *LoginRateLimiter) getLimiter(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, exists := l.visitors[ip]
	if !exists {
		entry = &visitorEntry{
			limiter:  rate.NewLimiter(l.rps, l.burst),
			lastSeen: time.Now(),
		}
		l.visitors[ip] = entry
	} else {
		entry.lastSeen = time.Now()
	}
	return entry.limiter
}

// cleanup 清理超过 10 分钟未访问的限流条目（LRU 策略，避免内存泄漏）
// 不再清空整个 map，防止攻击者通过 IP 轮换触发清空后重新爆破
func (l *LoginRateLimiter) cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-10 * time.Minute)
	for ip, entry := range l.visitors {
		if entry.lastSeen.Before(cutoff) {
			delete(l.visitors, ip)
		}
	}
}

// Middleware 速率限制中间件
func (l *LoginRateLimiter) Middleware(next http.Handler) http.Handler {
	// 启动定期清理协程
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			l.cleanup()
		}
	}()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		limiter := l.getLimiter(ip)
		if !limiter.Allow() {
			w.Header().Set("Retry-After", "60")
			http.Error(w, `{"error":"rate_limited","message":"too many login attempts, try again later"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Allow 检查请求是否允许通过（用于 handler 内直接调用，而非中间件包装）。
// 返回 false 表示已超限。首次调用会启动 cleanup 协程。
func (l *LoginRateLimiter) Allow(r *http.Request) bool {
	l.mu.Lock()
	if len(l.visitors) == 0 {
		// 首次使用时启动 cleanup 协程
		go func() {
			ticker := time.NewTicker(10 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				l.cleanup()
			}
		}()
	}
	l.mu.Unlock()
	ip := clientIP(r)
	return l.getLimiter(ip).Allow()
}

// clientIP 从请求中提取客户端 IP。
// 仅当请求直连来自可信代理（trustedProxies）时才采信 X-Forwarded-For / X-Real-IP，
// 防止客户端伪造 XFF 头绕过 IP 限流。未配置可信代理时直接用 RemoteAddr（最安全）。
func clientIP(r *http.Request) string {
	// 先解析直连 IP（RemoteAddr）
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}
	// 仅信任来自可信代理的 XFF / X-Real-IP
	if trustedProxies[remoteIP] {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if idx := strings.Index(xff, ","); idx > 0 {
				return strings.TrimSpace(xff[:idx])
			}
			return strings.TrimSpace(xff)
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	return remoteIP
}
