package middleware

import (
	"context"
	"log"
	"net/http"
	"strings"

	"filesync/internal/auth"
	"filesync/internal/authutil"
	"filesync/internal/jwks"
	"filesync/internal/model"
	"filesync/internal/store"
)

// contextKey 自定义上下文键（避免和其它包冲突）
type ctxKey string

const (
	UserIDKey    ctxKey = "user_id"
	UsernameKey  ctxKey = "username"
	RoleKey      ctxKey = "role"
	ScopeKey     ctxKey = "scope"
	// PAT 相关上下文键（外部 Agent / MCP 令牌元数据）
	APITokenIDKey ctxKey = "api_token_id"
	SpaceIDKey    ctxKey = "pat_space_id"
	PathPrefixKey ctxKey = "pat_path_prefix"
	QuotaBytesKey ctxKey = "pat_quota_bytes"
	QuotaUsedKey  ctxKey = "pat_quota_used"
)

// JWTAuthMiddleware JWT 认证中间件
// 三种工作模式：
//   - 网关模式：AUTH_MODE=dev 时信任网关已经校验过 JWT，
//     直接从 X-Auth-User-ID 等头读取用户信息
//   - 本地校验模式：tokens!=nil 时本地解析 Bearer Token 或 Cookie
//   - JWKS 校验模式：validator!=nil 时通过 AuthSvc JWKS 公钥验签
// 注意：网关透传模式仅限开发联调（AUTH_MODE=dev），生产默认关闭透传。
type JWTAuthMiddleware struct {
	tokens    *auth.RefreshTokenManager
	validator *jwks.Validator
	// PAT 支持：fsk_ 前缀令牌走独立验证器（MCP / 外部 Agent）
	patValidator *auth.PATValidator
	// 用于 PAT 认证时查询用户 role/status 与 Touch last_used_at（nil 时 PAT 仅信任 token 表）
	db *store.DB
}

// NewJWTAuthMiddleware 创建中间件。tokens 非 nil 时本地解析 Bearer/Cookie。
func NewJWTAuthMiddleware(tokens *auth.RefreshTokenManager) *JWTAuthMiddleware {
	return &JWTAuthMiddleware{tokens: tokens}
}

// WithPATValidator 启用 PAT 认证（fsk_ 前缀）。db 用于查询用户角色/状态，可为 nil。
func (m *JWTAuthMiddleware) WithPATValidator(v *auth.PATValidator, db *store.DB) *JWTAuthMiddleware {
	m.patValidator = v
	m.db = db
	return m
}

// NewJWKSJWTAuthMiddleware 创建基于 AuthSvc JWKS 公钥验签的中间件。
// 比透传模式安全：不信任请求头，必须携带合法 Access Token。
func NewJWKSJWTAuthMiddleware(validator *jwks.Validator) *JWTAuthMiddleware {
	return &JWTAuthMiddleware{validator: validator}
}

// Wrap 强制认证：未通过返回 401
func (m *JWTAuthMiddleware) Wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, username, role, scope, pat := m.authenticate(r)
		if userID == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized","message":"login required or invalid token"}`))
			return
		}
		ctx := m.inject(r.Context(), userID, username, role, scope, pat)
		next(w, r.WithContext(ctx))
	}
}

// WrapOptional 可选认证：允许未登录用户通过（Me 端点用于检测登录态）
func (m *JWTAuthMiddleware) WrapOptional(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, username, role, scope, pat := m.authenticate(r)
		if userID != "" {
			ctx := m.inject(r.Context(), userID, username, role, scope, pat)
			r = r.WithContext(ctx)
		}
		next(w, r)
	}
}

// authenticate 从请求解析并验证 Token
// 优先级：1.（仅 dev 模式）网关透传 Header  2. PAT (fsk_)  3. Authorization: Bearer  4. Cookie
// 返回值扩展：第 5 个返回值为 PAT 记录（非 nil 表示本次认证走 PAT，需注入 PAT 元数据）。
func (m *JWTAuthMiddleware) authenticate(r *http.Request) (string, string, string, string, *model.APIToken) {
	// 1. 开发模式透传（AUTH_MODE=dev，仅本机联调）
	if authutil.DevAuthMode() {
		if uid := r.Header.Get("X-Auth-User-ID"); uid != "" {
			return uid,
				r.Header.Get("X-Auth-Username"),
				r.Header.Get("X-Auth-Role"),
				r.Header.Get("X-Auth-Scope"),
				nil
		}
	}

	// 2. Authorization: Bearer / Cookie 统一提取 token（authutil 唯一定义）
	tok := authutil.BearerToken(r)
	if tok == "" {
		return "", "", "", "", nil
	}

	// 3. PAT 认证（fsk_ 前缀，MCP / 外部 Agent）
	if auth.LooksLikePAT(tok) {
		if m.patValidator == nil {
			return "", "", "", "", nil
		}
		rec, err := m.patValidator.Authenticate(tok)
		if err != nil {
			return "", "", "", "", nil
		}
		// 用户角色/状态以 users 表为准（PAT 表不冗余 role）
		role := "user"
		if m.db != nil {
			if u, uErr := m.db.GetUserByUsername(rec.Username); uErr == nil {
				if u.Status != "active" {
					return "", "", "", "", nil
				}
				role = u.Role
			}
		}
		// 异步非致命：更新 last_used_at
		if m.db != nil {
			m.db.TouchAPIToken(rec.ID)
		}
		return rec.UserID, rec.Username, role, rec.Scopes, rec
	}

	// 4. JWKS 验签（优先，不再信任透传头）
	if m.validator != nil {
		claims, err := m.validator.Verify(tok)
		if err != nil {
			return "", "", "", "", nil
		}
		return claims.UserID, claims.Username, claims.Role, claims.Scope, nil
	}

	// 5. 本地 RefreshTokenManager 验签
	if m.tokens != nil {
		if claims, err := m.tokens.ParseAccessToken(tok); err == nil {
			return claims.UserID, claims.Username, claims.Role, claims.Scope, nil
		}
	}
	return "", "", "", "", nil
}

// inject 把用户信息注入上下文
// 同时写入 middleware 自有键和 auth 包键（兼容旧 JWTManager 中间件注入的读取方）
// pat 非 nil 时额外注入 PAT 元数据（token_id/space_id/path_prefix/quota）
func (m *JWTAuthMiddleware) inject(ctx context.Context, userID, username, role, scope string, pat *model.APIToken) context.Context {
	ctx = context.WithValue(ctx, UserIDKey, userID)
	ctx = context.WithValue(ctx, UsernameKey, username)
	ctx = context.WithValue(ctx, RoleKey, role)
	ctx = context.WithValue(ctx, ScopeKey, scope)
	ctx = context.WithValue(ctx, auth.CtxKeyUserID, userID)
	ctx = context.WithValue(ctx, auth.CtxKeyUsername, username)
	ctx = context.WithValue(ctx, auth.CtxKeyRole, role)
	if pat != nil {
		ctx = context.WithValue(ctx, APITokenIDKey, pat.ID)
		ctx = context.WithValue(ctx, SpaceIDKey, pat.SpaceID)
		ctx = context.WithValue(ctx, PathPrefixKey, pat.PathPrefix)
		ctx = context.WithValue(ctx, QuotaBytesKey, pat.QuotaBytes)
		ctx = context.WithValue(ctx, QuotaUsedKey, pat.QuotaUsed)
	}
	return ctx
}

// Mux 返回带白名单的认证中间件（单体 server 使用）
// 白名单匹配规则与旧 auth.JWTManager.Middleware 一致：
//   - "/" 精确匹配；以 "/" 结尾前缀匹配；其余精确匹配
func (m *JWTAuthMiddleware) Mux(whitelist []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		for _, p := range whitelist {
			if p == "/" {
				if path == "/" {
					next.ServeHTTP(w, r)
					return
				}
			} else if strings.HasSuffix(p, "/") {
				if strings.HasPrefix(path, p) {
					next.ServeHTTP(w, r)
					return
				}
			} else {
				if path == p {
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		userID, username, role, scope, pat := m.authenticate(r)
		if userID == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized","message":"login required"}`))
			return
		}
		ctx := m.inject(r.Context(), userID, username, role, scope, pat)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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

// APITokenIDFromContext 从上下文取当前 PAT 的 token_id（非 PAT 请求返回空串）。
func APITokenIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(APITokenIDKey).(string)
	return v
}

// PATSpaceIDFromContext 从上下文取 PAT 限定的 space_id（空=不限定）。
func PATSpaceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(SpaceIDKey).(string)
	return v
}

// PATPathPrefixFromContext 从上下文取 PAT 限定的 path_prefix（空=不限定）。
func PATPathPrefixFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(PathPrefixKey).(string)
	return v
}

// PATQuotaFromContext 从上下文取 PAT 配额（bytes=0 表示不限制）。返回 (bytes, used)。
func PATQuotaFromContext(ctx context.Context) (int64, int64) {
	if ctx == nil {
		return 0, 0
	}
	bytes, _ := ctx.Value(QuotaBytesKey).(int64)
	used, _ := ctx.Value(QuotaUsedKey).(int64)
	return bytes, used
}

// suppress unused log import
var _ = log.Printf
