package biz

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"filesync/internal/jwks"
)

// ============================================================
// 服务间认证（安全修复）
// 默认 AUTH_MODE=jwt：必须携带 AuthSvc 签发的 Access Token
// （Authorization: Bearer 或 fs_access_token Cookie），由
// internal/jwks 通过 AuthSvc JWKS 公钥本地验签。
// 开发模式 AUTH_MODE=dev：仅本机联调用，直接信任 X-Auth-*。
// ============================================================

// AuthMode 返回当前认证模式
func AuthMode() string {
	m := strings.ToLower(strings.TrimSpace(os.Getenv("AUTH_MODE")))
	if m == "" {
		return "jwt" // 默认生产安全模式
	}
	return m
}

// Validator 供 main 构建中间件
func NewValidator(authSvcURL string) *jwks.Validator {
	return jwks.New(authSvcURL)
}

// VerifyBearer 校验 Bearer Token
func VerifyBearer(validator *jwks.Validator, token string) (authCtx, error) {
	claims, err := validator.Verify(token)
	if err != nil {
		return authCtx{}, err
	}
	return authCtx{UserID: claims.UserID, Username: claims.Username, Role: claims.Role, Scope: claims.Scope}, nil
}

// bearerToken 从请求提取 Access Token（Authorization 优先，Cookie 兜底）
func bearerToken(r *http.Request) string {
	authHdr := r.Header.Get("Authorization")
	if len(authHdr) > 7 && strings.EqualFold(authHdr[:7], "Bearer ") {
		return strings.TrimSpace(authHdr[7:])
	}
	if c, err := r.Cookie("fs_access_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// ---------- 请求上下文 ----------

type authCtxKey struct{}

// WithAuth 把已校验身份写入请求上下文
func WithAuth(ctx context.Context, ac authCtx) context.Context {
	return context.WithValue(ctx, authCtxKey{}, ac)
}

// AuthFromContext 从请求上下文读取已校验身份
func AuthFromContext(ctx context.Context) (authCtx, bool) {
	ac, ok := ctx.Value(authCtxKey{}).(authCtx)
	return ac, ok
}

var errNoToken = errors.New("missing access token")
