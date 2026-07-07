package middleware

import "net/http"

// SecurityHeaders 为所有响应添加安全响应头，包括 HSTS、CSP、Permissions-Policy 等。
// enableHSTS 控制是否发送 Strict-Transport-Security，仅在 HTTPS 模式下启用，
// 避免开发环境（HTTP）误锁死浏览器。
//
// 设置的安全头：
//   - Strict-Transport-Security: 强制 HTTPS，防止 SSL 剥离（仅 HTTPS 模式）
//   - Content-Security-Policy: 限制资源加载来源，防御 XSS 和数据注入
//   - Permissions-Policy: 禁用不必要的浏览器 API（地理位置/麦克风/摄像头/支付）
//   - X-Frame-Options: 防止点击劫持（与 CSP frame-ancestors 双重保险）
//   - X-Content-Type-Options: 防止 MIME 嗅探
//   - Referrer-Policy: 限制 Referer 泄露
//   - X-Permitted-Cross-Domain-Policies: 禁止 Adobe 跨域策略文件
//   - X-XSS-Protection: 旧浏览器 XSS 过滤（现代浏览器已废弃但不冲突）
func SecurityHeaders(enableHSTS bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HSTS：仅在 HTTPS 模式下发送，避免 HTTP 误锁死
		if enableHSTS {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		// CSP：default-src 'self' 兜底，其他指令细化白名单
		// style-src 'unsafe-inline'：前端有内联样式（SVG、动态 style 属性），无法完全禁用
		// img-src data: blob:：缩略图使用 data URL，预览使用 blob URL
		// font-src data:：Google Fonts 字体允许 data URL 内联
		// connect-src 'self'：fetch/XHR 仅同源
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: blob:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'self'; "+
				"base-uri 'self'; "+
				"form-action 'self'")

		// Permissions-Policy：禁用不必要的浏览器 API
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=(), payment=()")

		// X-Frame-Options：与 CSP frame-ancestors 双重保险，兼容旧浏览器
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")

		// X-Content-Type-Options：阻止 MIME 嗅探
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// Referrer-Policy：仅同源发送完整 Referer，跨源仅发 origin
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// X-Permitted-Cross-Domain-Policies：禁止 Adobe PDF/XLSX 嵌入跨域策略
		w.Header().Set("X-Permitted-Cross-Domain-Policies", "none")

		// X-XSS-Protection：旧浏览器 XSS 过滤（现代浏览器已废弃，但不冲突）
		w.Header().Set("X-XSS-Protection", "1; mode=block")

		next.ServeHTTP(w, r)
	})
}
