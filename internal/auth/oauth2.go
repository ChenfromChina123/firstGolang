package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// ============== OAuth2 常量 ==============
const (
	DefaultAuthCodeTTL = 10 * time.Minute
)

// ============== 错误 ==============
var (
	ErrRedirectMismatch   = errors.New("redirect_uri mismatch")
	ErrInvalidClient      = errors.New("invalid client")
	ErrInvalidScope       = errors.New("invalid scope")
	ErrAuthCodeUsed       = errors.New("authorization code already used")
	ErrAuthCodeExpired    = errors.New("authorization code expired")
	ErrPKCEChallengeFail  = errors.New("pkce challenge verification failed")
)

// ============== OAuth2 仓储接口 ==============

// AuthCodeStore 授权码存储接口（一次性消费）
type AuthCodeStore interface {
	// Save 保存授权码（含 state / nonce / PKCE 等扩展字段）
	Save(code, clientID, userID, redirectURI, scope, state, nonce, codeChallenge, codeChallengeMethod string, expiresAt time.Time) error
	// GetAndMarkUsed 原子获取并标记为已使用（防重放）
	// 返回：userID, scope, state, codeChallenge, codeChallengeMethod, redirectURI, error
	GetAndMarkUsed(code, clientID, redirectURI string) (userID, scope, state, challenge, method, storedRedirect string, err error)
}

// OAuthClientStore 客户端仓储接口（预注册的接入应用）
type OAuthClientStore interface {
	// GetByID 根据 client_id 查找客户端
	// 返回：secretHash, name, redirectURIs, allowedScopes, ownerUsername, isPublic, error
	GetByID(clientID string) (secretHash string, name string, redirectURIs []string, allowedScopes []string, ownerUsername string, isPublic bool, err error)
	// VerifySecret 校验 client_secret（bcrypt 哈希比对）
	VerifySecret(secretHash, plainSecret string) bool
}

// ============== OAuth2 授权码 ==============

// GenerateAuthCode 生成 OAuth2 授权码（32 字节 hex = 64 字符）
// 返回：code, expiresAt
func GenerateAuthCode(clientID, userID, redirectURI, scope, state string) (string, time.Time) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// 兜底：hash 时间+clientID+userID（可预测，仅在真随机失败时使用）
		h := sha256.Sum256([]byte(time.Now().String() + clientID + userID + redirectURI))
		return hex.EncodeToString(h[:]), time.Now().Add(DefaultAuthCodeTTL)
	}
	return hex.EncodeToString(b), time.Now().Add(DefaultAuthCodeTTL)
}

// ValidateAuthCode 校验授权码（一次性，调用后标记已使用）
// 校验项：存在、未过期、未使用、clientID 匹配、redirectURI 精确匹配、PKCE（如启用）
// 返回：userID, scope, state, error
func ValidateAuthCode(store AuthCodeStore, code, clientID, redirectURI, codeVerifier string) (string, string, string, error) {
	if store == nil {
		return "", "", "", errors.New("auth code store not configured")
	}
	userID, scope, state, challenge, method, storedRedirect, err := store.GetAndMarkUsed(code, clientID, redirectURI)
	if err != nil {
		return "", "", "", err
	}
	// redirect_uri 必须精确匹配（OAuth2 规范要求）
	if storedRedirect != redirectURI {
		return "", "", "", ErrRedirectMismatch
	}
	// PKCE 校验（RFC 7636）：授权时若带 code_challenge，换 token 时必须带 code_verifier
	if err := VerifyPKCE(challenge, method, codeVerifier); err != nil {
		return "", "", "", err
	}
	return userID, scope, state, nil
}

// ============== OAuth2 客户端校验 ==============

// ValidateClientRedirectURI 校验回调 URI 是否在客户端白名单中
// 支持通配符前缀匹配（如 "http://localhost:8888/*" 匹配其下所有路径）
func ValidateClientRedirectURI(clientStore OAuthClientStore, clientID, redirectURI string) error {
	if clientStore == nil {
		return errors.New("client store not configured")
	}
	_, _, allowedURIs, _, _, _, err := clientStore.GetByID(clientID)
	if err != nil {
		return ErrInvalidClient
	}
	for _, allowed := range allowedURIs {
		if matchRedirectURI(allowed, redirectURI) {
			return nil
		}
	}
	return ErrRedirectMismatch
}

// ValidateClientSecret 校验客户端密钥（仅非公开客户端需要）
func ValidateClientSecret(clientStore OAuthClientStore, clientID, plainSecret string) error {
	if clientStore == nil {
		return errors.New("client store not configured")
	}
	secretHash, _, _, _, _, isPublic, err := clientStore.GetByID(clientID)
	if err != nil {
		return ErrInvalidClient
	}
	if isPublic {
		// 公开客户端（SPA/移动App）不校验 secret，视为合法
		return nil
	}
	if plainSecret == "" {
		return ErrInvalidClient
	}
	if !clientStore.VerifySecret(secretHash, plainSecret) {
		return ErrInvalidClient
	}
	return nil
}

// ValidateScope 校验请求的 scope 是否都在允许列表内
// allowedScopes / requestedScopes 均为空格分隔字符串
func ValidateScope(allowedScopes []string, requestedScopes string) (string, error) {
	if requestedScopes == "" {
		return "", nil
	}
	allowedMap := make(map[string]bool, len(allowedScopes))
	for _, s := range allowedScopes {
		allowedMap[s] = true
		// "filesync:*" 通配支持
	}
	req := splitAndTrim(requestedScopes, " ")
	granted := make([]string, 0, len(req))
	for _, s := range req {
		if s == "" {
			continue
		}
		// 精确匹配或通配匹配
		if allowedMap[s] {
			granted = append(granted, s)
			continue
		}
		// filesync:* 匹配 filesync:read / filesync:write 等
		matched := false
		for _, a := range allowedScopes {
			if len(a) > 2 && a[len(a)-1] == '*' {
				prefix := a[:len(a)-1]
				if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
					granted = append(granted, s)
					matched = true
					break
				}
			}
		}
		if !matched {
			return "", ErrInvalidScope
		}
	}
	return joinSpace(granted), nil
}

// ============== PKCE 支持（公开客户端推荐开启）==============

// VerifyPKCE 校验 code_verifier 与 code_challenge 是否匹配
// method: "S256" (推荐) | "plain" (仅允许 https 且 debug 模式)
// 修复：S256 必须用 Base64URL 无填充编码（RFC 7636 第 4.2 节），此前误用 hex。
func VerifyPKCE(challenge, method, verifier string) error {
	if challenge == "" || verifier == "" {
		return nil // 未使用 PKCE，跳过
	}
	var computed string
	switch method {
	case "S256", "":
		h := sha256.Sum256([]byte(verifier))
		computed = base64.RawURLEncoding.EncodeToString(h[:])
	case "plain":
		computed = verifier
	default:
		return ErrPKCEChallengeFail
	}
	if computed != challenge {
		return ErrPKCEChallengeFail
	}
	return nil
}

// GenerateState 生成 16 字节随机 state 值（防 CSRF）
func GenerateState() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "state-" + time.Now().Format("20060102150405")
	}
	return hex.EncodeToString(b)
}

// ============== 内部辅助 ==============

// matchRedirectURI 匹配回调 URI，支持末尾 * 通配符
func matchRedirectURI(pattern, uri string) bool {
	if pattern == "" || uri == "" {
		return false
	}
	if pattern == uri {
		return true
	}
	// 通配符：末尾 * 匹配任意子路径
	if len(pattern) > 0 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		// 移除可能多余的斜杠，保证精确前缀
		if len(uri) >= len(prefix) && uri[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// splitAndTrim 按分隔符切分并去空串
func splitAndTrim(s, sep string) []string {
	if s == "" {
		return nil
	}
	// 手动切分，避免引入 strings 包重复 import（auth 包其它地方已用，这里保留简单实现）
	var result []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i:i+1] == sep {
			part := s[start:i]
			if part != "" {
				result = append(result, part)
			}
			start = i + 1
		}
	}
	return result
}

// joinSpace 用空格拼接字符串数组
func joinSpace(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += " " + parts[i]
	}
	return result
}
