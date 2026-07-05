package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Token 过期时间
const TokenExpiry = 24 * time.Hour

// Cookie 名称
const CookieName = "filesync_token"

// 常见错误
var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("token expired")
)

// Claims JWT 自定义声明
type Claims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// JWTManager 管理 JWT 签发与验证
type JWTManager struct {
	secret []byte
	domain string
	secure bool
}

// NewJWTManager 创建 JWT 管理器
// secret: JWT 签名密钥（至少 32 字节）
// domain: Cookie 的 Domain（如 aistudy.icu）
// secure: Cookie 是否启用 Secure 标记（HTTPS 时为 true）
func NewJWTManager(secret, domain string, secure bool) *JWTManager {
	return &JWTManager{
		secret: []byte(secret),
		domain: domain,
		secure: secure,
	}
}

// HashPassword 用 bcrypt 哈希密码（cost=12）
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	return string(bytes), err
}

// CheckPassword 校验明文密码与 bcrypt 哈希是否匹配
func CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// GenerateToken 签发 JWT token
func (m *JWTManager) GenerateToken(userID, username, role string) (string, error) {
	claims := &Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(TokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Issuer:    "filesync",
			Subject:   userID,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

// ParseToken 解析并验证 JWT token
func (m *JWTManager) ParseToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "token is expired") {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// SetAuthCookie 将 JWT token 设置到 HttpOnly Cookie
func (m *JWTManager) SetAuthCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Domain:   m.domain,
		MaxAge:   int(TokenExpiry.Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearAuthCookie 清除认证 Cookie（登出时调用）
func (m *JWTManager) ClearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		Domain:   m.domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// ReadTokenFromRequest 从请求中读取 JWT token（仅支持 Cookie 方式）
func (m *JWTManager) ReadTokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// GenerateSecret 生成 32 字节随机密钥（用于 JWT_SECRET 环境变量未设置时）
func GenerateSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("generate secret: %v", err))
	}
	return hex.EncodeToString(b)
}

// ValidatePasswordStrength 校验密码强度
// 规则：至少 8 位，包含字母和数字
func ValidatePasswordStrength(password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	hasLetter := false
	hasDigit := false
	for _, c := range password {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			hasLetter = true
		case c >= '0' && c <= '9':
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return errors.New("password must contain at least one letter and one digit")
	}
	return nil
}
