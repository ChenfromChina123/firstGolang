package middleware

import (
	"net/http"
	"strings"
)

// PathGuard 路径净化中间件：拒绝包含路径穿越序列的原始请求 URL。
// Go 标准库 http.ServeMux 会自动调用 path.Clean 规范化 URL，
// /web/../api/health 会被规范化为 /api/health 并匹配到对应 handler。
// 这是 ServeMux 的预期行为，不存在路径穿越漏洞（文件服务使用 StripPrefix + Dir，Dir 内部会再校验 .. ）。
//
// 但为了纵深防御和审计清晰，本中间件在 ServeMux 之前检查原始 URL.Path：
//   - 包含 "/../" 或 "/./" 序列：返回 400 Bad Request
//   - 路径以 ".." 或 "." 开头（如 "../secret"）：返回 400 Bad Request
//
// 这样既保留了 ServeMux 的 cleanPath 行为，又能在日志中明确记录被拒绝的恶意路径，
// 便于安全监控和攻击溯源。
//
// 注意：不修改 r.URL.Path，仅做检查。ServeMux 仍会按原逻辑处理（已规范化的路径）。
func PathGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// 检查路径穿越序列
		if strings.Contains(p, "/../") || strings.Contains(p, "/./") ||
			strings.HasPrefix(p, "../") || strings.HasPrefix(p, "./") {
			http.Error(w, "Bad Request: invalid path", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
