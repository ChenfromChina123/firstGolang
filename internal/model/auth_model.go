package model

import "time"

// ============================================================
// OAuth2 与 SSO 相关模型（独立于文件模型）
// Phase 1.4: 从 model.go 拆分出认证相关模型
// ============================================================

// OAuthClient OAuth2 预注册接入客户端
// 对应表：oauth_clients
type OAuthClient struct {
	ClientID       string    `json:"client_id"`       // 客户端ID，如 "personal-ip-site"
	ClientSecretHash string   `json:"-"`               // bcrypt 哈希后的密钥（不序列化）
	Name           string    `json:"name"`            // 显示名，如 "个人IP品牌网站"
	RedirectURIs   []string  `json:"redirect_uris"`   // 回调 URL 白名单
	AllowedScopes  []string  `json:"allowed_scopes"`  // 允许的 scope 列表
	IsPublic       bool      `json:"is_public"`       // 是否公开客户端（SPA/桌面App）
	CreatedAt      time.Time `json:"created_at"`
}

// OAuthCode OAuth2 授权码（一次性消费，10 分钟过期）
// 对应表：oauth_codes
type OAuthCode struct {
	Code                string    `json:"code"`                  // 32 字节 hex
	ClientID            string    `json:"client_id"`
	UserID              string    `json:"user_id"`
	RedirectURI         string    `json:"redirect_uri"`
	Scope               string    `json:"scope"`                 // 空格分隔
	State               string    `json:"state,omitempty"`       // CSRF state
	Nonce               string    `json:"nonce,omitempty"`       // OIDC nonce
	CodeChallenge       string    `json:"code_challenge,omitempty"` // PKCE challenge
	CodeChallengeMethod string    `json:"code_challenge_method,omitempty"` // S256 | plain
	ExpiresAt           time.Time `json:"expires_at"`
	Used                bool      `json:"used"`                  // 是否已消费
	CreatedAt           time.Time `json:"created_at"`
}

// RefreshSession Refresh Token 会话记录（绑定 JTI，轮换机制）
// 对应表：refresh_sessions
type RefreshSession struct {
	SessionID string    `json:"session_id"` // 会话唯一ID
	UserID    string    `json:"user_id"`
	ClientID  string    `json:"client_id"`
	JTI       string    `json:"jti"`        // 当前 Refresh Token JTI（用于黑名单）
	Scope     string    `json:"scope"`      // 授权 scope
	IP        string    `json:"ip,omitempty"`
	UserAgent string    `json:"user_agent,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"` // nil=有效，非nil=已吊销
	CreatedAt time.Time `json:"created_at"`
}

// SSOSession SSO 单点登录会话（跨应用免登凭据）
// 对应表：sso_sessions
type SSOSession struct {
	SessionID string    `json:"session_id"`
	UserID    string    `json:"user_id"`
	IP        string    `json:"ip,omitempty"`
	UserAgent string    `json:"user_agent,omitempty"`
	ExpiresAt time.Time `json:"expires_at"` // 24 小时
	CreatedAt time.Time `json:"created_at"`
}

// TokenPairResponse 签发/刷新 Token 时的标准响应体
type TokenPairResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`    // 固定 "Bearer"
	ExpiresIn    int64  `json:"expires_in"`    // Access Token 有效期（秒）
	Scope        string `json:"scope,omitempty"`
	// Refresh Token 不返回 JSON 体，仅走 HttpOnly Cookie
}

// JWK JSON Web Key（公钥）结构，供网关/下游服务拉取
type JWK struct {
	Kty string `json:"kty"` // 固定 "RSA"
	Use string `json:"use"` // 固定 "sig"
	Alg string `json:"alg"` // 固定 "RS256"
	Kid string `json:"kid"` // 密钥ID（版本号）
	N   string `json:"n"`   // RSA 公钥模数（Base64URL）
	E   string `json:"e"`   // RSA 公钥指数（Base64URL）
}

// JWKSResponse JWKS 端点响应体
type JWKSResponse struct {
	Keys []JWK `json:"keys"`
}

// SSOAuthorizeResponse SSO 授权端点响应（已登录时自动重定向）
type SSOAuthorizeResponse struct {
	RedirectURI string `json:"redirect_uri,omitempty"` // 前端自行跳转
	NeedLogin   bool   `json:"need_login"`             // true=未登录，前端跳转统一登录页
}
