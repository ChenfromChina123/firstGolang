package authutil

import (
	"net/http"
	"os"
	"strings"

	"filesync/internal/auth"
)

// AuthMode 返回当前认证模式（AUTH_MODE 环境变量，默认 jwt 生产安全模式）
// jwt = 校验 AuthSvc 签发的 Access Token；dev = 仅本机联调，信任 X-Auth-* 透传头
func AuthMode() string {
	m := strings.ToLower(strings.TrimSpace(os.Getenv("AUTH_MODE")))
	if m == "" {
		return "jwt"
	}
	return m
}

// DevAuthMode 是否开发透传模式（AUTH_MODE=dev）
func DevAuthMode() bool {
	return AuthMode() == "dev"
}

// BearerToken 从请求提取 Access Token：Authorization: Bearer 优先，Cookie 兜底。
// Cookie 名取 auth.AccessTokenCookieName 唯一定义（fs_access_token）。
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if c, err := r.Cookie(auth.AccessTokenCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}