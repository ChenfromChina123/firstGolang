package middleware

import "net/http"

// SecurityHeaders 为所有响应添加安全响应头，防止页面被 iframe 嵌入和 MIME 嗅探。
// 这是防盗链的第一道防线：即使其他网站尝试通过 iframe 嵌入本站页面，
// 浏览器也会因 X-Frame-Options 和 CSP frame-ancestors 拒绝渲染。
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
}
