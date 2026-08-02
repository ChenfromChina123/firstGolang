package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ============== 常量定义 ==============
const (
	AccessTokenCookieName  = "fs_access_token"
	RefreshTokenCookieName = "fs_refresh_token"
	DefaultAccessTTL       = 15 * time.Minute
	DefaultRefreshTTL      = 7 * 24 * time.Hour
	DefaultIssuer          = "filesync-auth"
)

// ============== 自定义 Claims ==============

// AccessClaims Access Token 声明（短生命周期，15 分钟）
type AccessClaims struct {
	UserID   string `json:"uid"`
	Username string `json:"un"`
	Role     string `json:"rl"`
	Scope    string `json:"scp,omitempty"` // 空格分隔的授权 scope
	Azp      string `json:"azp,omitempty"` // 授权客户端 ID (Authorized Party)
	jwt.RegisteredClaims
}

// RefreshClaims Refresh Token 声明（长生命周期，7 天）
// 每次刷新 Token 时轮换（Rotation），旧 JTI 加入黑名单
type RefreshClaims struct {
	UserID    string `json:"uid"`
	SessionID string `json:"sid"` // 刷新会话 ID，绑定 refresh_sessions 表
	Azp       string `json:"azp,omitempty"`
	jwt.RegisteredClaims
}

// ============== 仓储接口（由 store 层实现）==============

// RefreshSessionStore Refresh Token 会话仓储接口
type RefreshSessionStore interface {
	// CreateRefreshSession 持久化刷新会话记录
	CreateRefreshSession(sessionID, userID, clientID, jti, scope, ip, ua string, expiresAt time.Time) error
	// GetSessionByJTI 根据 Refresh Token JTI 查找会话（用于校验有效性）
	GetSessionByJTI(jti string) (sessionID, userID, scope string, revoked bool, err error)
	// RotateSession 轮换会话：吊销旧 JTI（加入黑名单），写入新 JTI
	RotateSession(oldJTI, newSessionID, newJTI string, newExpiresAt time.Time) error
	// RevokeAllUserSessions 吊销用户所有会话（改密码/登出所有设备时调用）
	RevokeAllUserSessions(userID string) error
	// RevokeSessionByJTI 吊销单个会话（普通登出）
	RevokeSessionByJTI(jti string) error
}

// BlacklistStore Token 黑名单仓储接口（短期缓存 revoked JTI）
type BlacklistStore interface {
	// AddToBlacklist 将 JTI 加入黑名单（ttl = token 剩余有效期即可）
	AddToBlacklist(jti string, ttl time.Duration) error
	// IsBlacklisted 检查 JTI 是否已被吊销
	IsBlacklisted(jti string) (bool, error)
}

// ============== RefreshTokenManager ==============

// RefreshTokenManager 双 Token 管理器
// 职责：签发 Access+Refresh、刷新轮换、登出吊销
type RefreshTokenManager struct {
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	issuer     string
	accessTTL  time.Duration
	refreshTTL time.Duration
	domain     string
	secure     bool
	sessions   RefreshSessionStore // 可 nil（纯内存模式）
	blacklist  BlacklistStore      // 可 nil（无短期缓存黑名单）
}

// NewRefreshTokenManager 创建双 Token 管理器
// sessions / blacklist 均可 nil（开发模式）
func NewRefreshTokenManager(
	privateKey *rsa.PrivateKey,
	domain string,
	secure bool,
	sessions RefreshSessionStore,
	blacklist BlacklistStore,
) *RefreshTokenManager {
	return &RefreshTokenManager{
		privateKey: privateKey,
		publicKey:  &privateKey.PublicKey,
		issuer:     DefaultIssuer,
		accessTTL:  DefaultAccessTTL,
		refreshTTL: DefaultRefreshTTL,
		domain:     domain,
		secure:     secure,
		sessions:   sessions,
		blacklist:  blacklist,
	}
}

// WithTTL 自定义 TTL（链式调用）
func (m *RefreshTokenManager) WithTTL(access, refresh time.Duration) *RefreshTokenManager {
	if access > 0 {
		m.accessTTL = access
	}
	if refresh > 0 {
		m.refreshTTL = refresh
	}
	return m
}

// WithIssuer 自定义 Issuer
func (m *RefreshTokenManager) WithIssuer(iss string) *RefreshTokenManager {
	if iss != "" {
		m.issuer = iss
	}
	return m
}

// ============== 核心方法 ==============

// IssueTokenPair 签发 Access + Refresh 双 Token 并写入 HttpOnly Cookie
// 返回：accessToken, refreshToken, accessExpiresInSec, error
func (m *RefreshTokenManager) IssueTokenPair(
	w http.ResponseWriter,
	userID, username, role, clientID, scope, ip, ua string,
) (string, string, int64, error) {
	now := time.Now()

	// 1. 生成 jti / sessionID
	accessJTI := generateRandomHex(16)
	refreshJTI := generateRandomHex(16)
	sessionID := generateRandomHex(16)

	// 2. 签发 Access Token
	accessClaims := &AccessClaims{
		UserID:   userID,
		Username: username,
		Role:     role,
		Scope:    scope,
		Azp:      clientID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   userID,
			Audience:  jwt.ClaimStrings{clientID},
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessTTL)),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        accessJTI,
		},
	}
	accessTok, err := m.signClaims(accessClaims)
	if err != nil {
		return "", "", 0, fmt.Errorf("sign access token: %w", err)
	}

	// 3. 签发 Refresh Token
	refreshClaims := &RefreshClaims{
		UserID:    userID,
		SessionID: sessionID,
		Azp:       clientID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   userID,
			Audience:  jwt.ClaimStrings{clientID},
			ExpiresAt: jwt.NewNumericDate(now.Add(m.refreshTTL)),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        refreshJTI,
		},
	}
	refreshTok, err := m.signClaims(refreshClaims)
	if err != nil {
		return "", "", 0, fmt.Errorf("sign refresh token: %w", err)
	}

	// 4. 持久化 Refresh Session
	if m.sessions != nil {
		expiresAt := now.Add(m.refreshTTL)
		err = m.sessions.CreateRefreshSession(sessionID, userID, clientID, refreshJTI, scope, ip, ua, expiresAt)
		if err != nil {
			return "", "", 0, fmt.Errorf("persist refresh session: %w", err)
		}
	}

	// 5. 写入 HttpOnly Cookie
	m.setCookie(w, AccessTokenCookieName, accessTok, m.accessTTL)
	m.setCookie(w, RefreshTokenCookieName, refreshTok, m.refreshTTL)

	expiresIn := int64(m.accessTTL.Seconds())
	return accessTok, refreshTok, expiresIn, nil
}

// RotateTokens 刷新 Token（轮换机制：旧 Refresh JTI 吊销，签发新的一对）
// 返回：新 accessToken, 新 refreshToken, accessExpiresInSec, 用户名, 角色, error
func (m *RefreshTokenManager) RotateTokens(
	w http.ResponseWriter,
	oldRefreshTok, clientID, ip, ua string,
	userGetter func(userID string) (username, role string, err error),
) (string, string, int64, string, string, error) {
	if oldRefreshTok == "" {
		return "", "", 0, "", "", errors.New("empty refresh token")
	}

	// 1. 解析并校验旧 Refresh Token
	oldClaims, err := m.ParseRefreshToken(oldRefreshTok)
	if err != nil {
		return "", "", 0, "", "", fmt.Errorf("invalid refresh token: %w", err)
	}

	// 2. 检查旧 JTI 是否在黑名单（被吊销/已轮换过）
	if m.blacklist != nil {
		bl, err := m.blacklist.IsBlacklisted(oldClaims.ID)
		if err == nil && bl {
			return "", "", 0, "", "", errors.New("refresh token revoked (reuse detected)")
		}
	}

	// 3. 检查会话有效性（DB 层校验：会话是否被吊销）
	var scopeStr string
	if m.sessions != nil {
		_, userIDFromDB, scope, revoked, err := m.sessions.GetSessionByJTI(oldClaims.ID)
		if err != nil {
			return "", "", 0, "", "", fmt.Errorf("refresh session not found: %w", err)
		}
		if revoked {
			return "", "", 0, "", "", errors.New("refresh session revoked")
		}
		// 防篡改：token 中的 userID 必须与 DB 会话中的 userID 一致
		if userIDFromDB != oldClaims.UserID {
			return "", "", 0, "", "", errors.New("refresh session user mismatch")
		}
		scopeStr = scope
	}

	// 4. 查询用户最新信息（用户名/角色可能已变更）
	username, role, err := userGetter(oldClaims.UserID)
	if err != nil {
		return "", "", 0, "", "", fmt.Errorf("user lookup failed: %w", err)
	}

	// 5. 签发新 Token 对
	now := time.Now()
	newAccessJTI := generateRandomHex(16)
	newRefreshJTI := generateRandomHex(16)
	newSessionID := generateRandomHex(16)

	newAccessClaims := &AccessClaims{
		UserID:   oldClaims.UserID,
		Username: username,
		Role:     role,
		Scope:    scopeStr,
		Azp:      clientID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   oldClaims.UserID,
			Audience:  jwt.ClaimStrings{clientID},
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessTTL)),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        newAccessJTI,
		},
	}
	newAccessTok, err := m.signClaims(newAccessClaims)
	if err != nil {
		return "", "", 0, "", "", fmt.Errorf("sign new access: %w", err)
	}

	newRefreshClaims := &RefreshClaims{
		UserID:    oldClaims.UserID,
		SessionID: newSessionID,
		Azp:       clientID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   oldClaims.UserID,
			Audience:  jwt.ClaimStrings{clientID},
			ExpiresAt: jwt.NewNumericDate(now.Add(m.refreshTTL)),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        newRefreshJTI,
		},
	}
	newRefreshTok, err := m.signClaims(newRefreshClaims)
	if err != nil {
		return "", "", 0, "", "", fmt.Errorf("sign new refresh: %w", err)
	}

	// 6. DB 层轮换：旧 JTI 标记为吊销，写入新会话
	if m.sessions != nil {
		newExpires := now.Add(m.refreshTTL)
		if err := m.sessions.RotateSession(oldClaims.ID, newSessionID, newRefreshJTI, newExpires); err != nil {
			return "", "", 0, "", "", fmt.Errorf("rotate session: %w", err)
		}
	}

	// 7. 短期黑名单缓存旧 JTI（TTL = 旧 Token 剩余有效期，取保守值 1 小时兜底）
	if m.blacklist != nil {
		remaining := time.Until(oldClaims.ExpiresAt.Time)
		if remaining <= 0 {
			remaining = time.Hour
		}
		_ = m.blacklist.AddToBlacklist(oldClaims.ID, remaining)
	}

	// 8. 写入新 Cookie
	m.setCookie(w, AccessTokenCookieName, newAccessTok, m.accessTTL)
	m.setCookie(w, RefreshTokenCookieName, newRefreshTok, m.refreshTTL)

	expiresIn := int64(m.accessTTL.Seconds())
	return newAccessTok, newRefreshTok, expiresIn, username, role, nil
}

// RevokeRefreshToken 吊销单个 Refresh Token（普通登出）
func (m *RefreshTokenManager) RevokeRefreshToken(w http.ResponseWriter, refreshTok string) error {
	if refreshTok == "" {
		// 清除 Cookie 即可
		m.clearCookies(w)
		return nil
	}
	claims, err := m.ParseRefreshToken(refreshTok)
	if err == nil {
		if m.sessions != nil {
			_ = m.sessions.RevokeSessionByJTI(claims.ID)
		}
		if m.blacklist != nil {
			remaining := time.Until(claims.ExpiresAt.Time)
			if remaining > 0 {
				_ = m.blacklist.AddToBlacklist(claims.ID, remaining)
			}
		}
	}
	m.clearCookies(w)
	return nil
}

// RevokeAllUserTokens 吊销用户所有会话（改密码 / 强制下线所有设备）
func (m *RefreshTokenManager) RevokeAllUserTokens(userID string) error {
	if m.sessions == nil {
		return errors.New("session store not configured")
	}
	return m.sessions.RevokeAllUserSessions(userID)
}

// ============== Token 解析 ==============

// ParseAccessToken 解析并验证 Access Token
func (m *RefreshTokenManager) ParseAccessToken(tokStr string) (*AccessClaims, error) {
	token, err := jwt.ParseWithClaims(tokStr, &AccessClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.publicKey, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*AccessClaims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid access token claims")
	}
	// 黑名单检查
	if m.blacklist != nil {
		bl, err := m.blacklist.IsBlacklisted(claims.ID)
		if err == nil && bl {
			return nil, errors.New("access token revoked")
		}
	}
	return claims, nil
}

// ParseRefreshToken 解析并验证 Refresh Token（签名 + 过期，不检查黑名单，由调用方决定）
func (m *RefreshTokenManager) ParseRefreshToken(tokStr string) (*RefreshClaims, error) {
	token, err := jwt.ParseWithClaims(tokStr, &RefreshClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.publicKey, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*RefreshClaims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid refresh token claims")
	}
	return claims, nil
}

// ReadTokensFromRequest 从请求的 Cookie / Authorization 头中读取 Token
// 返回：accessToken, refreshToken
func (m *RefreshTokenManager) ReadTokensFromRequest(r *http.Request) (string, string) {
	var accessTok, refreshTok string

	// 优先 Authorization: Bearer xxx 头（网关透传模式）
	authHdr := r.Header.Get("Authorization")
	if len(authHdr) > 7 && authHdr[:7] == "Bearer " {
		accessTok = authHdr[7:]
	}

	// Cookie 兜底（浏览器直接访问）
	if accessTok == "" {
		if c, err := r.Cookie(AccessTokenCookieName); err == nil {
			accessTok = c.Value
		}
	}
	if c, err := r.Cookie(RefreshTokenCookieName); err == nil {
		refreshTok = c.Value
	}
	return accessTok, refreshTok
}

// ============== 内部辅助 ==============

func (m *RefreshTokenManager) signClaims(claims jwt.Claims) (string, error) {
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return tok.SignedString(m.privateKey)
}

// setCookie 写入 HttpOnly Cookie（双 Token 策略：Refresh 走 HttpOnly，Access 也走 Cookie + 头）
func (m *RefreshTokenManager) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	sameSite := http.SameSiteStrictMode
	if !m.secure {
		// 开发环境允许跨端口 SameSite=Lax 即可；若 SSO 需要跨顶级域则配置为 None+Secure
		sameSite = http.SameSiteLaxMode
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Domain:   m.domain,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: sameSite,
	})
}

// clearCookies 清除 Access + Refresh Cookie
func (m *RefreshTokenManager) clearCookies(w http.ResponseWriter) {
	for _, name := range []string{AccessTokenCookieName, RefreshTokenCookieName} {
		sameSite := http.SameSiteStrictMode
		if !m.secure {
			sameSite = http.SameSiteLaxMode
		}
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			Domain:   m.domain,
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   m.secure,
			SameSite: sameSite,
		})
	}
}

// generateRandomHex 生成 n 字节安全随机数，hex 编码返回
func generateRandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// 极端降级：用时间戳哈希兜底
		h := fmt.Sprintf("%d-%d", time.Now().UnixNano(), n)
		return hex.EncodeToString([]byte(h))[:n*2]
	}
	return hex.EncodeToString(b)
}
