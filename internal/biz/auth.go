package biz

import (
	"context"

	"filesync/internal/jwks"
)

// ============================================================
// 服务间认证（安全技术栈）
// 默认 AUTH_MODE=jwt：必须携带 AuthSvc 签发的 Access Token
// （Authorization: Bearer 或 auth.AccessTokenCookieName Cookie），由
// internal/jwks 通过 AuthSvc JWKS 公钥本地验签。
// 开发模式 AUTH_MODE=dev：仅本机联调用，直接信任 X-Auth-*。
// ============================================================

// NewValidator 供 main 构建中间件

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
