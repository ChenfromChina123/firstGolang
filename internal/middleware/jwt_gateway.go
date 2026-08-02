package middleware

import (
	"context"
	"log"
	"net/http"
	"strings"

	"filesync/internal/auth"
)

// contextKey 自定义上下文键（避免和其它包冲突）
type ctxKey string

const (
	UserIDKey    ctxKey = "user_id"
	UsernameKey  ctxKey = "username"
	RoleKey      ctxKey = "role"
	ScopeKey     ctxKey = "scope"
)

// JWTAuthMiddleware JWT 认证中间件
// 两种工作模式：
//   - 网关模式：信任网关已经校验过 JWT，直接从 X-Auth-User-ID 等头读取用户信息（tokens=nil）
//   - 本地校验模式：tokens!=nil 时本地解析 Bearer Token 或 Cookie
type JWTAuthMiddleware struct {
	tokens *auth.RefreshTokenManager
}

// NewJWTAuthMiddleware 创建中间件。tokens=nil 时进入网关透传模式。
func NewJWTAuthMiddleware(tokens *auth.RefreshTokenManager) *JWTAuthMiddleware {
	return &JWTAuthMiddleware{tokens: tokens}
}

// Wrap 强制认证：未通过返回 401
func (m *JWTAuthMiddleware) Wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, username, role, scope := m.authenticate(r)
		if userID == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized","message":"login required or invalid token"}`))
			return
		}
		ctx := m.inject(r.Context(), userID, username, role, scope)
		next(w, r.WithContext(ctx))
	}
}

// WrapOptional 可选认证：允许未登录用户通过（Me 端点用于检测登录态）
func (m *JWTAuthMiddleware) WrapOptional(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, username, role, scope := m.authenticate(r)
		if userID != "" {
			ctx := m.inject(r.Context(), userID, username, role, scope)
			r = r.WithContext(ctx)
		}
		next(w, r)
	}
}

// authenticate 从请求解析并验证 Token
// 优先级：1. 网关透传 Header  2. Authorization: Bearer  3. Cookie
func (m *JWTAuthMiddleware) authenticate(r *http.Request) (string, string, string, string) {
	// 1. 网关透传模式（X-Forwarded-User 等头，仅内网可信环境使用）
	if uid := r.Header.Get("X-Auth-User-ID"); uid != "" {
		return uid,
			r.Header.Get("X-Auth-Username"),
			r.Header.Get("X-Auth-Role"),
			r.Header.Get("X-Auth-Scope")
	}

	if m.tokens == nil {
		// 纯透传模式无 Token 解析，返回空
		return "", "", "", ""
	}

	// 2. Authorization: Bearer
	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		tok := strings.TrimPrefix(ah, "Bearer ")
		if claims, err := m.tokens.ParseAccessToken(tok); err == nil {
			return claims.UserID, claims.Username, claims.Role, claims.Scope
		}
	}

	// 3. Cookie
	if c, err := r.Cookie("fs_access_token"); err == nil && c.Value != "" {
		if claims, err := m.tokens.ParseAccessToken(c.Value); err == nil {
			return claims.UserID, claims.Username, claims.Role, claims.Scope
		}
	}
	return "", "", "", ""
}

// inject 把用户信息注入上下文
func (m *JWTAuthMiddleware) inject(ctx context.Context, userID, username, role, scope string) context.Context {
	ctx = context.WithValue(ctx, UserIDKey, userID)
	ctx = context.WithValue(ctx, UsernameKey, username)
	ctx = context.WithValue(ctx, RoleKey, role)
	ctx = context.WithValue(ctx, ScopeKey, scope)
	return ctx
}

// ---------- 给 handler/auth 使用的兼容函数 ----------

// UserIDFromContext 从上下文取当前用户ID
func UserIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(UserIDKey).(string)
	return v
}

// UsernameFromContext 从上下文取当前用户名
func UsernameFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(UsernameKey).(string)
	return v
}

// RoleFromContext 从上下文取当前角色
func RoleFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(RoleKey).(string)
	return v
}

// ScopeFromContext 从上下文取 scope
func ScopeFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ScopeKey).(string)
	return v
}

// suppress unused log import
var _ = log.Printf
