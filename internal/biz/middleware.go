package biz

import (
	"log"
	"net/http"

	"filesync/internal/authutil"
	"filesync/internal/jwks"
)

// AuthMiddleware 业务服务认证中间件
//   - AUTH_MODE=jwt（默认）：校验 AuthSvc Access Token（JWKS 验签）
//   - AUTH_MODE=dev：仅本机联调，信任 X-Auth-* 头（启动日志明确告警）
func AuthMiddleware(validator *jwks.Validator) func(http.Handler) http.Handler {
	devMode := authutil.DevAuthMode()
	if devMode {
		log.Println("[WARN] AUTH_MODE=dev：信任客户端 X-Auth-* 头，仅限本机联调，禁止生产使用")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			if devMode {
				ac := resolveAuth(r)
				if ac.UserID == "" {
					writeErr(w, http.StatusUnauthorized, "unauthorized", "missing X-Auth-User-ID (dev mode)")
					return
				}
				next.ServeHTTP(w, r.WithContext(WithAuth(r.Context(), ac)))
				return
			}
			token := authutil.BearerToken(r)
			ac, err := VerifyBearer(validator, token)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid or missing access token")
				return
			}
			ac.Token = token
			next.ServeHTTP(w, r.WithContext(WithAuth(r.Context(), ac)))
		})
	}
}
