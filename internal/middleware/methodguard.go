package middleware

import "net/http"

// MethodGuard 拒绝 TRACE、CONNECT 方法的请求，防止 XST（Cross-Site Tracing）攻击
// 和潜在的方法滥用。其他标准方法（GET/POST/PUT/DELETE/PATCH/HEAD/OPTIONS）
// 放行由各 handler 自行根据 r.Method 处理。
//
// 安全背景：
//   - TRACE 方法可能反射请求头导致敏感信息泄露（XST 攻击）
//   - CONNECT 方法用于建立隧道，Web 业务不应接受
//   - 现代浏览器已禁止 TRACE，但 curl/攻击工具仍可发送
func MethodGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodTrace, http.MethodConnect:
			w.Header().Set("Allow", "GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}
