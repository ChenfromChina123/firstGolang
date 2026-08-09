package workbench

import (
	"context"
	"log"
	"net/http"

	"filesync/internal/authutil"
	"filesync/internal/jwks"
)

// ============================================================
// 服务间认证（安全修复，与 BizSvc 同一策略）
// 默认 AUTH_MODE=jwt：校验 AuthSvc Access Token（JWKS 验签）
// AUTH_MODE=dev：仅本机联调，信任 X-Auth-User-ID
// ============================================================

type ownerCtxKey struct{}

// WithOwner 把已校验用户 ID 写入请求上下文
func WithOwner(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ownerCtxKey{}, userID)
}

// OwnerFromContext 从请求上下文读取已校验用户 ID
func OwnerFromContext(ctx context.Context) (string, bool) {
	uid, ok := ctx.Value(ownerCtxKey{}).(string)
	return uid, ok
}

// AuthMiddleware 工作台认证中间件
func AuthMiddleware(validator *jwks.Validator) func(http.Handler) http.Handler {
	devMode := authutil.DevAuthMode()
	if devMode {
		log.Println("[WARN] Workbench AUTH_MODE=dev：信任客户端 X-Auth-User-ID，仅限本机联调")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			if devMode {
				uid := r.Header.Get("X-Auth-User-ID")
				if uid == "" {
					uid = r.Header.Get("X-Forwarded-User")
				}
				if uid == "" {
					writeErr(w, http.StatusUnauthorized, "unauthorized", "missing X-Auth-User-ID (dev mode)")
					return
				}
				next.ServeHTTP(w, r.WithContext(WithOwner(r.Context(), uid)))
				return
			}
			claims, err := validator.Verify(authutil.BearerToken(r))
			if err != nil || claims.UserID == "" {
				writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid or missing access token")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithOwner(r.Context(), claims.UserID)))
		})
	}
}
