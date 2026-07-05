package auth

import (
	"context"
	"log"
	"net/http"
	"strings"
)

// context key 类型，避免冲突
type contextKey string

const (
	// CtxKeyUserID 用户 ID
	CtxKeyUserID contextKey = "user_id"
	// CtxKeyUsername 用户名
	CtxKeyUsername contextKey = "username"
	// CtxKeyRole 角色
	CtxKeyRole contextKey = "role"
)

// Middleware 返回 JWT 认证中间件
// 白名单中的路径不需要认证（如 /api/login, /web/, /api/health）
func (m *JWTManager) Middleware(whitelist []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// 白名单检查
		for _, p := range whitelist {
			if strings.HasPrefix(path, p) {
				next.ServeHTTP(w, r)
				return
			}
		}

		// 从 Cookie 读取 token
		tokenString := m.ReadTokenFromRequest(r)
		if tokenString == "" {
			http.Error(w, `{"error":"unauthorized","message":"login required"}`, http.StatusUnauthorized)
			return
		}

		// 验证 token
		claims, err := m.ParseToken(tokenString)
		if err != nil {
			log.Printf("[Auth] token verify failed: %v path=%s", err, path)
			http.Error(w, `{"error":"unauthorized","message":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		// 将用户信息存入 context
		ctx := context.WithValue(r.Context(), CtxKeyUserID, claims.UserID)
		ctx = context.WithValue(ctx, CtxKeyUsername, claims.Username)
		ctx = context.WithValue(ctx, CtxKeyRole, claims.Role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserIDFromContext 从 context 中提取用户 ID
func UserIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(CtxKeyUserID).(string); ok {
		return v
	}
	return ""
}

// UsernameFromContext 从 context 中提取用户名
func UsernameFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(CtxKeyUsername).(string); ok {
		return v
	}
	return ""
}

// RoleFromContext 从 context 中提取角色
func RoleFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(CtxKeyRole).(string); ok {
		return v
	}
	return ""
}
