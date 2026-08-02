package service

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/email"
	"filesync/internal/model"
	"filesync/internal/store"
)

// ============================================================
// AuthService 核心认证服务（登录/注册/登出/刷新/改密）
// 职责：编排 store + auth 算法层，不直接写 HTTP 响应
// ============================================================

// AuthService 认证服务
type AuthService struct {
	db       *store.DB
	tokens   *auth.RefreshTokenManager
	sso      *auth.SSOSessionManager
	mailer   *email.SMTPMailer // 可为 nil（注册/忘记密码功能禁用）
	baseURL  string
	rsaKeys  *auth.RSAKeys // 解密前端 RSA 加密的密码（可为 nil，接受明文）
	defaultClientID string // 默认客户端（FileSync Web 控制台）
}

// NewAuthService 创建认证服务
func NewAuthService(
	db *store.DB,
	tokens *auth.RefreshTokenManager,
	sso *auth.SSOSessionManager,
	mailer *email.SMTPMailer,
	baseURL string,
	rsaKeys *auth.RSAKeys,
	defaultClientID string,
) *AuthService {
	return &AuthService{
		db:       db,
		tokens:   tokens,
		sso:      sso,
		mailer:   mailer,
		baseURL:  baseURL,
		rsaKeys:  rsaKeys,
		defaultClientID: defaultClientID,
	}
}

// ---- 请求上下文工具 ----

// ClientIP 从请求获取真实客户端 IP（考虑 X-Forwarded-For）
func ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if net.ParseIP(strings.TrimSpace(xri)) != nil {
			return strings.TrimSpace(xri)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// UserAgent 获取 User-Agent
func UserAgent(r *http.Request) string {
	if r == nil {
		return ""
	}
	ua := r.Header.Get("User-Agent")
	if len(ua) > 512 {
		return ua[:512]
	}
	return ua
}

// ---- 登录 ----

// LoginResult 登录结果
type LoginResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
	UserID       string
	Username     string
	Role         string
}

// Login 用户登录（用户名/邮箱 + 密码）
// 成功后同时写入双 Token Cookie 和 SSO 会话 Cookie
func (s *AuthService) Login(w http.ResponseWriter, r *http.Request, usernameOrEmail, password string, clientID string) (*LoginResult, error) {
	if usernameOrEmail == "" || password == "" {
		return nil, errors.New("username and password required")
	}
	// 密码处理：优先尝试 RSA 解密（前端加密场景）；解密失败则把原密码当明文再试一次（开发/调试场景）
	if s.rsaKeys != nil {
		if decrypted, rsaErr := s.rsaKeys.DecryptPassword(password); rsaErr == nil && decrypted != "" {
			password = decrypted
		}
	}
	// 查询用户
	var user *model.User
	var err error
	if strings.Contains(usernameOrEmail, "@") {
		user, err = s.db.GetUserByEmail(auth.NormalizeEmail(usernameOrEmail))
	} else {
		user, err = s.db.GetUserByUsername(usernameOrEmail)
	}
	if err != nil || user == nil {
		log.Printf("[Auth] login failed: user not found id=%s ip=%s", usernameOrEmail, ClientIP(r))
		return nil, errors.New("invalid username or password")
	}
	// 校验密码
	if !auth.CheckPassword(password, user.PasswordHash) {
		log.Printf("[Auth] login failed: wrong password user=%s ip=%s", user.Username, ClientIP(r))
		return nil, errors.New("invalid username or password")
	}
	// 检查状态
	if user.Status != "active" {
		return nil, fmt.Errorf("account not activated: status=%s", user.Status)
	}
	// 默认客户端
	if clientID == "" {
		clientID = s.defaultClientID
	}
	scope := "filesync:read filesync:write"
	ip := ClientIP(r)
	ua := UserAgent(r)

	// 签发双 Token
	accessTok, refreshTok, expiresIn, err := s.tokens.IssueTokenPair(
		w, user.ID, user.Username, user.Role, clientID, scope, ip, ua,
	)
	if err != nil {
		return nil, fmt.Errorf("issue tokens: %w", err)
	}
	// 创建 SSO 会话（跨应用免登）
	if s.sso != nil {
		if _, ssoErr := s.sso.CreateSession(w, user.ID, ip, ua); ssoErr != nil {
			log.Printf("[Auth] create SSO session warning: %v", ssoErr)
		}
	}
	log.Printf("[Auth] login success: user=%s ip=%s", user.Username, ip)
	return &LoginResult{
		AccessToken:  accessTok,
		RefreshToken: refreshTok,
		ExpiresIn:    expiresIn,
		UserID:       user.ID,
		Username:     user.Username,
		Role:         user.Role,
	}, nil
}

// ---- 登出 ----

// Logout 登出（吊销当前 Refresh Token + 清除 SSO 会话）
func (s *AuthService) Logout(w http.ResponseWriter, r *http.Request) error {
	_, refreshTok := s.tokens.ReadTokensFromRequest(r)
	if refreshTok != "" {
		_ = s.tokens.RevokeRefreshToken(w, refreshTok)
	} else {
		// 至少清除 Cookie
		s.tokens.RevokeRefreshToken(w, "")
	}
	if s.sso != nil {
		_ = s.sso.DestroySession(w, r)
	}
	return nil
}

// ---- 刷新 Token ----

// RefreshResult 刷新结果
type RefreshResult struct {
	AccessToken string
	ExpiresIn   int64
	Username    string
	Role        string
}

// RefreshToken 刷新 Access Token（轮换机制）
func (s *AuthService) RefreshToken(w http.ResponseWriter, r *http.Request, clientID string) (*RefreshResult, error) {
	if clientID == "" {
		clientID = s.defaultClientID
	}
	_, refreshTok := s.tokens.ReadTokensFromRequest(r)
	ip := ClientIP(r)
	ua := UserAgent(r)
	accessTok, _, expiresIn, username, role, err := s.tokens.RotateTokens(
		w, refreshTok, clientID, ip, ua,
		func(userID string) (string, string, error) {
			u, e := s.db.GetUserByID(userID)
			if e != nil {
				return "", "", e
			}
			return u.Username, u.Role, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return &RefreshResult{
		AccessToken: accessTok,
		ExpiresIn:   expiresIn,
		Username:    username,
		Role:        role,
	}, nil
}

// ---- 注册 ----

// Register 注册新用户（pending 状态，需邮件激活）
// mailer 为 nil 时返回 503 错误
func (s *AuthService) Register(email, password, confirmPassword string) error {
	if s.mailer == nil {
		return errors.New("email service disabled, registration not available")
	}
	// 解密密码
	if s.rsaKeys != nil {
		var err error
		if password, err = s.rsaKeys.DecryptPassword(password); err != nil {
			return errors.New("password must be RSA encrypted")
		}
		if confirmPassword, err = s.rsaKeys.DecryptPassword(confirmPassword); err != nil {
			return errors.New("confirm password must be RSA encrypted")
		}
	}
	email = auth.NormalizeEmail(email)
	if err := auth.ValidateEmail(email); err != nil {
		return err
	}
	if err := auth.ValidatePasswordStrength(password); err != nil {
		return err
	}
	if password != confirmPassword {
		return errors.New("passwords do not match")
	}
	// 检查邮箱是否已注册
	existing, err := s.db.GetUserByEmail(email)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("query existing email: %w", err)
	}
	if existing != nil {
		if existing.Status == "active" {
			// 邮箱已激活：静默返回成功（避免枚举），但实际不发邮件
			return nil
		}
		// 未激活：清理旧 token 重发
		_ = s.db.DeleteActivationTokensByUserID(existing.ID)
		return s.sendActivationEmail(existing.ID, email)
	}
	// 生成用户名（避免冲突）
	var username string
	for i := 0; i < 5; i++ {
		candidate := auth.GenerateUsernameFromEmail(email)
		_, err := s.db.GetUserByUsername(candidate)
		if err == sql.ErrNoRows {
			username = candidate
			break
		}
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("generate username: %w", err)
		}
	}
	if username == "" {
		username = email
		if len(username) > 60 {
			username = username[:60]
		}
	}
	// 哈希密码
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	// 创建用户
	now := time.Now()
	user := &model.User{
		ID:           generateUserIDSvc(),
		Username:     username,
		Email:        email,
		PasswordHash: hash,
		Role:         "user",
		Status:       "pending",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.db.CreateUser(user); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	// 发激活邮件
	return s.sendActivationEmail(user.ID, email)
}

// sendActivationEmail 生成激活 token 并存库 + 发邮件
func (s *AuthService) sendActivationEmail(userID, emailAddr string) error {
	token, err := auth.GenerateActivationToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(24 * time.Hour)
	if err := s.db.CreateActivationToken(userID, token, expiresAt); err != nil {
		return err
	}
	activationURL := fmt.Sprintf("%s/auth/activate?token=%s", strings.TrimRight(s.baseURL, "/"), token)
	s.mailer.SendActivationEmailAsync(emailAddr, activationURL)
	return nil
}

// Activate 激活账号
func (s *AuthService) Activate(token string) error {
	if token == "" {
		return errors.New("activation token required")
	}
	userID, expiresAt, err := s.db.GetActivationToken(token)
	if err != nil {
		return errors.New("invalid activation token")
	}
	if time.Now().After(expiresAt) {
		_ = s.db.DeleteActivationToken(token)
		return errors.New("activation token expired")
	}
	if err := s.db.UpdateUserStatus(userID, "active"); err != nil {
		return err
	}
	_ = s.db.DeleteActivationToken(token)
	log.Printf("[Auth] user activated: user_id=%s", userID)
	return nil
}

// ---- 忘记密码 / 重置密码 ----

// ForgotPassword 发送密码重置验证码到邮箱
func (s *AuthService) ForgotPassword(emailAddr string) error {
	if s.mailer == nil {
		return errors.New("email service disabled")
	}
	emailAddr = auth.NormalizeEmail(emailAddr)
	if err := auth.ValidateEmail(emailAddr); err != nil {
		return err
	}
	user, err := s.db.GetUserByEmail(emailAddr)
	if err != nil || user == nil {
		// 邮箱不存在，静默返回成功（避免枚举）
		return nil
	}
	code, err := auth.GenerateResetCode()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(10 * time.Minute)
	if err := s.db.CreateResetCode(user.ID, emailAddr, code, expiresAt); err != nil {
		return err
	}
	s.mailer.SendResetCodeEmailAsync(emailAddr, code)
	return nil
}

// ResetPassword 用验证码重置密码
func (s *AuthService) ResetPassword(emailAddr, code, newPassword, confirmPassword string) error {
	emailAddr = auth.NormalizeEmail(emailAddr)
	if emailAddr == "" || code == "" {
		return errors.New("email and code required")
	}
	// 解密密码
	if s.rsaKeys != nil {
		var err error
		if newPassword, err = s.rsaKeys.DecryptPassword(newPassword); err != nil {
			return errors.New("password must be RSA encrypted")
		}
		if confirmPassword, err = s.rsaKeys.DecryptPassword(confirmPassword); err != nil {
			return errors.New("confirm password must be RSA encrypted")
		}
	}
	if err := auth.ValidatePasswordStrength(newPassword); err != nil {
		return err
	}
	if newPassword != confirmPassword {
		return errors.New("passwords do not match")
	}
	userID, used, expiresAt, err := s.db.GetResetCode(emailAddr, code)
	if err != nil || userID == "" {
		return errors.New("invalid or expired reset code")
	}
	if used {
		return errors.New("reset code already used")
	}
	if time.Now().After(expiresAt) {
		return errors.New("reset code expired")
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.db.UpdateUserPassword(userID, hash); err != nil {
		return err
	}
	// 改密码后吊销所有会话 + SSO
	if s.tokens != nil {
		_ = s.tokens.RevokeAllUserTokens(userID)
	}
	if s.sso != nil {
		_ = s.sso.DestroyAllUserSessions(userID)
	}
	_ = s.db.MarkResetCodeUsed(emailAddr, code)
	log.Printf("[Auth] password reset: user_id=%s", userID)
	return nil
}

// ---- 工具 ----

// generateUserIDSvc 生成 32 字符 hex 用户ID
func generateUserIDSvc() string {
	return auth.GenerateSecret()
}
