package middleware

import (
	"net/http"

	"filesync/internal/auth"
)

// ============================================================
// Scope 强制中间件：校验当前请求上下文的 scope 是否满足要求。
// 补上 ScopeFromContext 零调用方的坑。
//
// 安全模型：
//   - PAT（fsk_）请求：权限 = 令牌声明的 scopes，必须显式校验。
//   - Web JWT 会话：已通过账号密码强认证，视为账号级完整权限，放行
//     （现有登录 scope 硬编码 read+write 且无 share，若强校验会误伤 Web 功能）。
//   - admin：恒放行（超级用户，与文件权限语义一致）。
// ============================================================

// isPATRequest 判断当前请求是否由 PAT 认证（上下文带 api_token_id）。
func isPATRequest(r *http.Request) bool {
	return APITokenIDFromContext(r.Context()) != ""
}

// writeMethod 判断 HTTP 方法是否为写操作。
func writeMethod(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// RequireScope 校验 PAT 请求需至少包含 required 中的任一 scope（OR 语义）。
// 非 PAT 请求（Web 会话）恒放行——账号级信任。
// 注意：PAT 即使底层是 admin 账号，也严格按 token scopes 校验（G2：不继承账号完整权限）。
// 用于 MCP 工具门禁与 REST 写接口。
func RequireScope(required ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isPATRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			for _, req := range required {
				if auth.HasScope(ScopeFromContext(r.Context()), req) {
					next.ServeHTTP(w, r)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"insufficient_scope","message":"token lacks required scope"}`))
		})
	}
}

// RequireFileScope 文件域 REST 接口的 PAT scope 门禁（按方法区分）：
//   - 读方法（GET/HEAD/OPTIONS）：需 filesync:read
//   - 写方法（POST/PUT/DELETE/PATCH）：需 filesync:write
//   - 非 PAT 请求（Web 会话）恒放行
//   - PAT 不继承底层账号的 admin 角色，严格按 token scopes 校验
func RequireFileScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isPATRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		scopes := ScopeFromContext(r.Context())
		var required string
		if writeMethod(r) {
			required = auth.ScopeWrite
		} else {
			required = auth.ScopeRead
		}
		if auth.HasScope(scopes, required) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"insufficient_scope","message":"token lacks required scope: ` + required + `"}`))
	})
}

// RequireShareScope 分享域接口的 PAT scope 门禁：需 filesync:share。
// 非 PAT 请求（Web 会话）恒放行；PAT 严格按 token scopes 校验。
func RequireShareScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isPATRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if auth.HasScope(ScopeFromContext(r.Context()), auth.ScopeShare) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"insufficient_scope","message":"token lacks required scope: filesync:share"}`))
	})
}

// RequireAllScopes 校验 PAT 请求需同时包含全部 required scopes（AND 语义）。
// 非 PAT 请求（Web 会话）恒放行；PAT 严格按 token scopes 校验。
func RequireAllScopes(required ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isPATRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			scopes := ScopeFromContext(r.Context())
			for _, req := range required {
				if !auth.HasScope(scopes, req) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					w.Write([]byte(`{"error":"insufficient_scope","message":"token lacks required scope: ` + req + `"}`))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
