package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// LoginRateLimiter 登录接口速率限制器
// 按 IP 限流，每个 IP 每分钟最多 5 次登录请求
type LoginRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*rate.Limiter
	rps      rate.Limit
	burst    int
}

// NewLoginRateLimiter 创建登录速率限制器
// rps: 每秒允许的请求数（0.1 = 每 10 秒 1 次）
// burst: 突发上限
func NewLoginRateLimiter(rps float64, burst int) *LoginRateLimiter {
	return &LoginRateLimiter{
		visitors: make(map[string]*rate.Limiter),
		rps:      rate.Limit(rps),
		burst:    burst,
	}
}

// getLimiter 获取或创建 IP 对应的限流器
func (l *LoginRateLimiter) getLimiter(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	limiter, exists := l.visitors[ip]
	if !exists {
		limiter = rate.NewLimiter(l.rps, l.burst)
		l.visitors[ip] = limiter
	}
	return limiter
}

// cleanup 清理过期的限流器（避免内存泄漏）
func (l *LoginRateLimiter) cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()
	// 简单策略：超过 1000 个 IP 时清空（实际生产可用 LRU）
	if len(l.visitors) > 1000 {
		l.visitors = make(map[string]*rate.Limiter)
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

// clientIP 从请求中提取客户端 IP
func clientIP(r *http.Request) string {
	// 优先从 X-Forwarded-For 取（反代场景）
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.Index(xff, ","); idx > 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
