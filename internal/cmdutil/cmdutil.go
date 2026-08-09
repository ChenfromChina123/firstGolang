// Package cmdutil 提供各 cmd/* 主入口共享的辅助函数（env/method/CORS/日志），
// 消除 5 个 main.go 中逐字重复的样板代码。
package cmdutil

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// GetEnv 读取环境变量，为空时返回 fallback
func GetEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// GetEnvInt 读取整数环境变量，非法值回退默认
func GetEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// GetEnvBool 读取布尔环境变量（true/1 视为真）
func GetEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		return strings.EqualFold(v, "true") || v == "1"
	}
	return fallback
}

// MaskDSN 脱敏数据库 DSN（隐藏密码）
func MaskDSN(dsn string) string {
	parts := strings.SplitN(dsn, "@", 2)
	if len(parts) != 2 {
		return dsn
	}
	up := strings.SplitN(parts[0], ":", 2)
	if len(up) != 2 {
		return dsn
	}
	return up[0] + ":***@" + parts[1]
}

// Method 限制 HTTP 方法（OPTIONS 直接 204）
func Method(m string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !strings.EqualFold(r.Method, m) {
			w.Header().Set("Allow", m)
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

// CorsWrap 内联 CORS 处理（允许 X-Auth-* 透传头，供 AUTH_MODE=dev 联调）
func CorsWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With, Accept, Origin, Range, X-Auth-User-ID, X-Auth-Username, X-Auth-Role, X-Auth-Scope")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition, Content-Length, Accept-Ranges")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// LogMiddleware 请求日志中间件。
// uid 返回请求对应的用户标识（透传头或上下文）；nil 时输出 "-"。
func LogMiddleware(prefix string, uid func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &LogWriter{ResponseWriter: w, Status: http.StatusOK}
		next.ServeHTTP(lrw, r)
		user := "-"
		if uid != nil {
			if u := uid(r); u != "" {
				user = u
			}
		}
		log.Printf("[%s] %s %s -> %d (%s)  user=%s",
			prefix, r.Method, r.URL.Path, lrw.Status, time.Since(start), user)
	})
}

// LogWriter 记录响应状态码
type LogWriter struct {
	http.ResponseWriter
	Status int
}

// WriteHeader 记录状态码并透传
func (l *LogWriter) WriteHeader(s int) { l.Status = s; l.ResponseWriter.WriteHeader(s) }