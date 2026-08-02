package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"time"
)

// ============== SSO 常量 ==============
const (
	SSOSessionCookieName = "fs_sso_session"
	DefaultSSOTTL        = 24 * time.Hour
)

// ============== SSO 仓储接口 ==============

// SSOSessionStore SSO 会话存储接口（DB/Redis 实现）
type SSOSessionStore interface {
	Save(sessionID, userID, ip, ua string, expiresAt time.Time) error
	GetBySessionID(sessionID string) (userID string, err error) // 过期/不存在返回 error
	Delete(sessionID string) error
	DeleteByUser(userID string) error // 用户改密码时清除所有 SSO 会话
}

// ============== SSOSessionManager ==============

// SSOSessionManager SSO 会话管理器
// 职责：用户在统一登录页登录后写入 fs_sso_session Cookie（跨子域），
// 当用户访问其他接入应用的 SSO 回调端点时，免登录直接跳转授权。
type SSOSessionManager struct {
	store  SSOSessionStore
	ttl    time.Duration
	domain string
	secure bool
}

// NewSSOSessionManager 创建 SSO 管理器
func NewSSOSessionManager(store SSOSessionStore, domain string, secure bool) *SSOSessionManager {
	return &SSOSessionManager{
		store:  store,
		ttl:    DefaultSSOTTL,
		domain: domain,
		secure: secure,
	}
}

// WithTTL 自定义 SSO 会话有效期
func (m *SSOSessionManager) WithTTL(ttl time.Duration) *SSOSessionManager {
	if ttl > 0 {
		m.ttl = ttl
	}
	return m
}

// CreateSession 创建 SSO 会话（用户在统一登录页登录成功后调用）
// 返回生成的 sessionID
func (m *SSOSessionManager) CreateSession(w http.ResponseWriter, userID, ip, ua string) (string, error) {
	if m == nil || m.store == nil {
		return "", errors.New("sso store not configured")
	}
	if userID == "" {
		return "", errors.New("empty userID")
	}

	sessionID, err := generateSSOSessionID()
	if err != nil {
		return "", err
	}

	expiresAt := time.Now().Add(m.ttl)
	err = m.store.Save(sessionID, userID, ip, ua, expiresAt)
	if err != nil {
		return "", err
	}

	m.setCookie(w, sessionID)
	return sessionID, nil
}

// GetSessionUser 从请求读取 SSO Cookie 并校验
// 返回：已登录的用户ID；未登录/过期/不存在返回空串
func (m *SSOSessionManager) GetSessionUser(r *http.Request) string {
	if m == nil || m.store == nil {
		return ""
	}
	c, err := r.Cookie(SSOSessionCookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	userID, err := m.store.GetBySessionID(c.Value)
	if err != nil {
		return ""
	}
	return userID
}

// DestroySession 销毁当前 SSO 会话（统一登出时调用）
func (m *SSOSessionManager) DestroySession(w http.ResponseWriter, r *http.Request) error {
	if m == nil || m.store == nil {
		m.clearCookie(w)
		return nil
	}
	c, err := r.Cookie(SSOSessionCookieName)
	if err == nil && c.Value != "" {
		_ = m.store.Delete(c.Value)
	}
	m.clearCookie(w)
	return nil
}

// DestroyAllUserSessions 清除用户的所有 SSO 会话（改密码 / 强制下线）
func (m *SSOSessionManager) DestroyAllUserSessions(userID string) error {
	if m == nil || m.store == nil {
		return errors.New("sso store not configured")
	}
	if userID == "" {
		return errors.New("empty userID")
	}
	return m.store.DeleteByUser(userID)
}

// ============== 内部辅助 ==============

// generateSSOSessionID 生成 32 字节安全随机 hex = 64 字符
func generateSSOSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("generate sso session id: random source error")
	}
	return hex.EncodeToString(b), nil
}

// setCookie 写入 SSO Cookie（跨子域）
func (m *SSOSessionManager) setCookie(w http.ResponseWriter, sessionID string) {
	sameSite := http.SameSiteStrictMode
	if !m.secure {
		sameSite = http.SameSiteLaxMode
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SSOSessionCookieName,
		Value:    sessionID,
		Path:     "/",
		Domain:   m.domain,
		MaxAge:   int(m.ttl.Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: sameSite,
	})
}

// clearCookie 清除 SSO Cookie
func (m *SSOSessionManager) clearCookie(w http.ResponseWriter) {
	sameSite := http.SameSiteStrictMode
	if !m.secure {
		sameSite = http.SameSiteLaxMode
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SSOSessionCookieName,
		Value:    "",
		Path:     "/",
		Domain:   m.domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: sameSite,
	})
}
