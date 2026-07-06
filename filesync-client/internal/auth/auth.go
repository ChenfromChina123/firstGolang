// Package auth 实现客户端认证管理。
// 使用 net/http/cookiejar 自动管理 JWT Cookie，密码不持久化（仅内存）。
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

// UserInfo 当前用户信息（从 /api/me 获取）
type UserInfo struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// 登录响应体（与服务器 loginResponse 对齐）
type loginResponse struct {
	Success  bool   `json:"success"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// 服务器错误响应体
type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// 预定义错误
var (
	ErrInvalidCredentials  = errors.New("用户名或密码错误")
	ErrAccountNotActivated = errors.New("账号未激活，请查收激活邮件后激活账号")
	ErrServerUnavailable   = errors.New("无法连接服务器")
)

// AuthManager 管理客户端认证状态。
// 使用 cookiejar 自动管理 JWT Cookie，应用重启后 Cookie 丢失需重新登录。
type AuthManager struct {
	serverURL string
	client    *http.Client
	user      *UserInfo
}

// New 创建 AuthManager 实例。
// serverURL: 服务器地址（如 http://localhost:8080）
func New(serverURL string) *AuthManager {
	jar, _ := cookiejar.New(nil)
	return &AuthManager{
		serverURL: strings.TrimRight(serverURL, "/"),
		client: &http.Client{
			Jar:     jar,
			Timeout: 10 * time.Second,
		},
	}
}

// SetServerURL 切换服务器地址。
// 重置 cookiejar 和 user 缓存，避免跨域 Cookie 污染。
func (a *AuthManager) SetServerURL(url string) {
	if a.serverURL == strings.TrimRight(url, "/") {
		return
	}
	jar, _ := cookiejar.New(nil)
	a.serverURL = strings.TrimRight(url, "/")
	a.client.Jar = jar
	a.user = nil
}

// HTTPClient 返回带 cookiejar 的 http.Client，供 API Client 复用。
func (a *AuthManager) HTTPClient() *http.Client {
	return a.client
}

// ServerURL 返回当前服务器地址。
func (a *AuthManager) ServerURL() string {
	return a.serverURL
}

// Login 登录服务器。
// POST /api/login，请求体 {"username":"xxx","password":"xxx"}
// 成功后服务器设置 HttpOnly Cookie，cookiejar 自动保存。
func (a *AuthManager) Login(username, password string) error {
	if a.serverURL == "" {
		return ErrServerUnavailable
	}

	body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
	resp, err := a.client.Post(a.serverURL+"/api/login", "application/json", strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrServerUnavailable, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var loginResp loginResponse
		if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
			return fmt.Errorf("解析登录响应失败: %w", err)
		}
		a.user = &UserInfo{
			Username: loginResp.Username,
			Role:     loginResp.Role,
		}
		return nil
	case http.StatusUnauthorized:
		return ErrInvalidCredentials
	case http.StatusForbidden:
		return ErrAccountNotActivated
	default:
		var errResp errorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err == nil && errResp.Message != "" {
			return fmt.Errorf("登录失败 (HTTP %d): %s", resp.StatusCode, errResp.Message)
		}
		return fmt.Errorf("登录失败 (HTTP %d)", resp.StatusCode)
	}
}

// Logout 登出服务器。
// POST /api/logout，清除服务器端 Cookie。
func (a *AuthManager) Logout() error {
	resp, err := a.client.Post(a.serverURL+"/api/logout", "application/json", nil)
	if err != nil {
		return fmt.Errorf("登出请求失败: %v", err)
	}
	defer resp.Body.Close()
	a.user = nil
	return nil
}

// IsAuthenticated 检查是否已登录。
// 通过 GET /api/me 探测（避免解析 JWT），200 则缓存用户信息。
func (a *AuthManager) IsAuthenticated() bool {
	resp, err := a.client.Get(a.serverURL + "/api/me")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var info UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err == nil && info.UserID != "" {
		a.user = &info
		return true
	}
	return a.user != nil
}

// GetUserInfo 返回缓存的用户信息（未登录返回 nil）。
func (a *AuthManager) GetUserInfo() *UserInfo {
	return a.user
}
