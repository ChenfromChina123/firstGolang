package handler

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/email"
	"filesync/internal/model"
	"filesync/internal/store"
)

// AuthHandler 处理登录/登出/注册/激活/忘记密码/重置密码
type AuthHandler struct {
	db      *store.DB
	jwt     *auth.JWTManager
	mailer  *email.SMTPMailer // 可为 nil（未配置 SMTP 时禁用注册功能）
	baseURL string            // 应用根地址，用于生成激活链接，如 https://aistudy.icu
}

// NewAuthHandler 创建认证 handler
// mailer 可为 nil（未配置 SMTP 时注册/忘记密码接口返回 503）
// baseURL 用于拼接激活链接，如 https://aistudy.icu
func NewAuthHandler(db *store.DB, jwt *auth.JWTManager, mailer *email.SMTPMailer, baseURL string) *AuthHandler {
	return &AuthHandler{db: db, jwt: jwt, mailer: mailer, baseURL: baseURL}
}

// loginRequest 登录请求体
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponse 登录响应体
type loginResponse struct {
	Success  bool   `json:"success"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// Login 处理登录请求
// POST /api/login  Body: {"username":"xxx","password":"xxx"}
// username 字段同时支持用户名和邮箱登录
// 速率限制由 main.go 用 middleware 包装（5次/分钟/IP）
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	// 限制请求体大小（防 DoS）
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request","message":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	// 基本校验
	if req.Username == "" || req.Password == "" {
		http.Error(w, `{"error":"invalid_request","message":"username and password required"}`, http.StatusBadRequest)
		return
	}

	// 查询用户：支持用户名或邮箱登录
	// 输入包含 @ 视为邮箱，按邮箱查询；否则按用户名查询
	var user *model.User
	var err error
	if strings.Contains(req.Username, "@") {
		emailNorm := auth.NormalizeEmail(req.Username)
		user, err = h.db.GetUserByEmail(emailNorm)
	} else {
		user, err = h.db.GetUserByUsername(req.Username)
	}
	if err != nil {
		// 用户不存在，返回通用错误（避免枚举攻击）
		log.Printf("[Auth] login failed (user not found): username=%s ip=%s", req.Username, r.RemoteAddr)
		http.Error(w, `{"error":"invalid_credentials","message":"invalid username or password"}`, http.StatusUnauthorized)
		return
	}

	// 校验密码
	if !auth.CheckPassword(req.Password, user.PasswordHash) {
		log.Printf("[Auth] login failed (wrong password): username=%s ip=%s", req.Username, r.RemoteAddr)
		http.Error(w, `{"error":"invalid_credentials","message":"invalid username or password"}`, http.StatusUnauthorized)
		return
	}

	// 检查账号状态：未激活账号拒绝登录
	if user.Status != "active" {
		log.Printf("[Auth] login failed (not activated): username=%s status=%s ip=%s", user.Username, user.Status, r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"account_not_activated","message":"账号未激活，请查收激活邮件后激活账号"}`))
		return
	}

	// 签发 JWT
	token, err := h.jwt.GenerateToken(user.ID, user.Username, user.Role)
	if err != nil {
		log.Printf("[Auth] generate token error: %v", err)
		http.Error(w, `{"error":"internal_error","message":"failed to generate token"}`, http.StatusInternalServerError)
		return
	}

	// 设置 HttpOnly Cookie
	h.jwt.SetAuthCookie(w, token)

	log.Printf("[Auth] login success: username=%s ip=%s", user.Username, r.RemoteAddr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(loginResponse{
		Success:  true,
		Username: user.Username,
		Role:     user.Role,
	})
}

// Logout 处理登出请求
// POST /api/logout
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	h.jwt.ClearAuthCookie(w)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success":true}`))
}

// Me 返回当前登录用户信息
// GET /api/me
// 双重保障：即使中间件配置错误放行了未认证请求，
// 此处也会因 userID 为空而返回 401
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())

	// 双重保障：userID 为空说明未经过认证
	if userID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized","message":"login required"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"user_id":  userID,
		"username": username,
		"role":     role,
	})
}

// EnsureInitialAdmin 确保 users 表有至少一个管理员
// 首次启动（users 表为空）时，从环境变量读取初始管理员账号密码
func EnsureInitialAdmin(db *store.DB, envUsername, envPassword string) error {
	count, err := db.CountUsers()
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	// users 表为空，需要创建初始管理员
	if envUsername == "" || envPassword == "" {
		log.Printf("[Auth] users table empty, using default admin: admin / changeme123")
		log.Printf("[Auth] IMPORTANT: change default password immediately!")
		envUsername = "admin"
		envPassword = "changeme123"
	} else {
		if err := auth.ValidatePasswordStrength(envPassword); err != nil {
			return fmt.Errorf("initial password too weak: %w", err)
		}
	}

	hash, err := auth.HashPassword(envPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	now := time.Now()
	user := &model.User{
		ID:           generateUserID(),
		Username:     envUsername,
		PasswordHash: hash,
		Role:         "admin",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.CreateUser(user); err != nil {
		return fmt.Errorf("create initial admin: %w", err)
	}
	log.Printf("[Auth] initial admin created: username=%s", envUsername)
	return nil
}

// generateUserID 生成 32 字符 hex 用户 ID
func generateUserID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405")))
	}
	return hex.EncodeToString(b)
}

// === 注册 / 激活 / 忘记密码 / 重置密码 ===

// 激活令牌有效期 24 小时
const activationTokenExpiry = 24 * time.Hour

// 密码重置验证码有效期 10 分钟
const resetCodeExpiry = 10 * time.Minute

// registerRequest 注册请求体
type registerRequest struct {
	Email           string `json:"email"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
}

// resendActivationRequest 重新发送激活邮件请求体
type resendActivationRequest struct {
	Email string `json:"email"`
}

// forgotPasswordRequest 忘记密码请求体
type forgotPasswordRequest struct {
	Email string `json:"email"`
}

// resetPasswordRequest 重置密码请求体
type resetPasswordRequest struct {
	Email           string `json:"email"`
	Code            string `json:"code"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

// Register 处理注册请求
// POST /api/register  Body: {"email":"xxx","password":"xxx","confirm_password":"xxx"}
// 流程：校验 → 创建 pending 用户 → 发激活邮件 → 返回成功
// 速率限制由 main.go 中 middleware 包装（3次/小时/IP）
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	if h.mailer == nil {
		http.Error(w, `{"error":"email_service_disabled","message":"邮件服务未配置，无法注册"}`, http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request","message":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	email := auth.NormalizeEmail(req.Email)
	if err := auth.ValidateEmail(email); err != nil {
		http.Error(w, `{"error":"invalid_email","message":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	if err := auth.ValidatePasswordStrength(req.Password); err != nil {
		http.Error(w, `{"error":"weak_password","message":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	if req.Password != req.ConfirmPassword {
		http.Error(w, `{"error":"password_mismatch","message":"两次输入的密码不一致"}`, http.StatusBadRequest)
		return
	}

	// 检查邮箱是否已注册
	existing, err := h.db.GetUserByEmail(email)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("[Auth] register: query existing email error: %v", err)
		http.Error(w, `{"error":"internal_error","message":"查询邮箱失败"}`, http.StatusInternalServerError)
		return
	}
	if existing != nil {
		if existing.Status == "active" {
			// 邮箱已激活：返回通用成功（避免枚举攻击），不告知是否存在
			// 但仍提示用户检查邮件（若用户本人就是注册者，会知道账号已存在）
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"message": "若邮箱可用，激活邮件已发送，请查收邮箱",
			})
			return
		}
		// 邮箱已注册但未激活：清理旧 token，重新发送激活邮件
		if err := h.db.DeleteActivationTokensByUserID(existing.ID); err != nil {
			log.Printf("[Auth] register: delete old tokens error: %v", err)
		}
		h.sendActivationEmail(existing.ID, email)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "若邮箱可用，激活邮件已发送，请查收邮箱",
		})
		return
	}

	// 生成 username（避免与现有冲突）
	var username string
	for i := 0; i < 5; i++ {
		candidate := auth.GenerateUsernameFromEmail(email)
		_, err := h.db.GetUserByUsername(candidate)
		if err == sql.ErrNoRows {
			username = candidate
			break
		}
		if err != nil {
			log.Printf("[Auth] register: query candidate username error: %v", err)
			http.Error(w, `{"error":"internal_error","message":"生成用户名失败"}`, http.StatusInternalServerError)
			return
		}
	}
	if username == "" {
		// 5 次都冲突，用 email 全量作为 username（截断到 60 字符避免超长）
		username = email
		if len(username) > 60 {
			username = username[:60]
		}
	}

	// 哈希密码
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		log.Printf("[Auth] register: hash password error: %v", err)
		http.Error(w, `{"error":"internal_error","message":"密码加密失败"}`, http.StatusInternalServerError)
		return
	}

	// 创建用户（status=pending, role=user）
	now := time.Now()
	user := &model.User{
		ID:           generateUserID(),
		Username:     username,
		Email:        email,
		PasswordHash: hash,
		Role:         "user",
		Status:       "pending",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := h.db.CreateUser(user); err != nil {
		log.Printf("[Auth] register: create user error: %v", err)
		http.Error(w, `{"error":"internal_error","message":"创建用户失败"}`, http.StatusInternalServerError)
		return
	}

	// 生成激活 token 并发送邮件
	h.sendActivationEmail(user.ID, email)

	log.Printf("[Auth] register success: username=%s email=%s ip=%s", username, email, r.RemoteAddr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "若邮箱可用，激活邮件已发送，请查收邮箱",
	})
}

// sendActivationEmail 生成激活 token 存库 + 异步发送激活邮件
// 抽出为独立方法，避免 Register 和 ResendActivation 重复代码
func (h *AuthHandler) sendActivationEmail(userID, email string) {
	token := auth.GenerateActivationToken()
	expiresAt := time.Now().Add(activationTokenExpiry)
	if err := h.db.CreateActivationToken(userID, token, expiresAt); err != nil {
		log.Printf("[Auth] create activation token error: %v", err)
		return
	}
	activationURL := fmt.Sprintf("%s/api/activate?token=%s", strings.TrimRight(h.baseURL, "/"), token)
	h.mailer.SendActivationEmailAsync(email, activationURL)
}

// Activate 处理账号激活
// GET /api/activate?token=xxx
// 流程：校验 token → 更新用户 status=active → 删除 token → 重定向到前端激活结果页
func (h *AuthHandler) Activate(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Redirect(w, r, "/web/activate.html?status=invalid", http.StatusFound)
		return
	}

	userID, expiresAt, err := h.db.GetActivationToken(token)
	if err != nil {
		log.Printf("[Auth] activate: token not found: %v", err)
		http.Redirect(w, r, "/web/activate.html?status=invalid", http.StatusFound)
		return
	}
	if time.Now().After(expiresAt) {
		// token 过期，清理掉
		_ = h.db.DeleteActivationToken(token)
		log.Printf("[Auth] activate: token expired: user_id=%s", userID)
		http.Redirect(w, r, "/web/activate.html?status=expired", http.StatusFound)
		return
	}

	// 激活用户
	if err := h.db.UpdateUserStatus(userID, "active"); err != nil {
		log.Printf("[Auth] activate: update user status error: %v", err)
		http.Redirect(w, r, "/web/activate.html?status=error", http.StatusFound)
		return
	}
	// 删除已使用的 token（确保一次性）
	_ = h.db.DeleteActivationToken(token)

	log.Printf("[Auth] activate success: user_id=%s", userID)
	http.Redirect(w, r, "/web/login.html?activated=1", http.StatusFound)
}

// ResendActivation 重新发送激活邮件
// POST /api/resend-activation  Body: {"email":"xxx"}
// 速率限制由 main.go 中 middleware 包装（3次/小时/IP）
func (h *AuthHandler) ResendActivation(w http.ResponseWriter, r *http.Request) {
	if h.mailer == nil {
		http.Error(w, `{"error":"email_service_disabled","message":"邮件服务未配置"}`, http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req resendActivationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request","message":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	email := auth.NormalizeEmail(req.Email)
	if err := auth.ValidateEmail(email); err != nil {
		http.Error(w, `{"error":"invalid_email","message":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	// 查询用户（不存在也返回成功，避免枚举）
	user, err := h.db.GetUserByEmail(email)
	if err != nil || user == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "若邮箱可用且未激活，激活邮件已发送",
		})
		return
	}
	if user.Status == "active" {
		// 已激活，告知用户直接登录
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "账号已激活，请直接登录",
		})
		return
	}

	// 清理旧 token，重新发送
	_ = h.db.DeleteActivationTokensByUserID(user.ID)
	h.sendActivationEmail(user.ID, email)

	log.Printf("[Auth] resend activation: email=%s ip=%s", email, r.RemoteAddr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "若邮箱可用且未激活，激活邮件已发送",
	})
}

// ForgotPassword 处理忘记密码请求
// POST /api/forgot-password  Body: {"email":"xxx"}
// 流程：校验邮箱 → 查询用户 → 生成 6 位验证码 → 发送邮件
// 速率限制由 main.go 中 middleware 包装（3次/小时/IP）
func (h *AuthHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	if h.mailer == nil {
		http.Error(w, `{"error":"email_service_disabled","message":"邮件服务未配置"}`, http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req forgotPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request","message":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	email := auth.NormalizeEmail(req.Email)
	if err := auth.ValidateEmail(email); err != nil {
		http.Error(w, `{"error":"invalid_email","message":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	// 查询用户（不存在也返回成功，避免枚举攻击）
	user, err := h.db.GetUserByEmail(email)
	if err != nil || user == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "若邮箱可用，验证码已发送",
		})
		return
	}
	// 未激活账号不允许重置密码（应先激活）
	if user.Status != "active" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "若邮箱可用，验证码已发送",
		})
		return
	}

	// 生成验证码并存库
	code := auth.GenerateResetCode()
	expiresAt := time.Now().Add(resetCodeExpiry)
	if err := h.db.CreateResetCode(user.ID, email, code, expiresAt); err != nil {
		log.Printf("[Auth] forgot password: create reset code error: %v", err)
		http.Error(w, `{"error":"internal_error","message":"生成验证码失败"}`, http.StatusInternalServerError)
		return
	}

	// 异步发送邮件
	h.mailer.SendResetCodeEmailAsync(email, code)

	log.Printf("[Auth] forgot password: email=%s ip=%s", email, r.RemoteAddr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "若邮箱可用，验证码已发送",
	})
}

// ResetPassword 处理密码重置请求
// POST /api/reset-password  Body: {"email":"xxx","code":"xxx","new_password":"xxx","confirm_password":"xxx"}
func (h *AuthHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req resetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request","message":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	email := auth.NormalizeEmail(req.Email)
	if err := auth.ValidateEmail(email); err != nil {
		http.Error(w, `{"error":"invalid_email","message":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	// 验证码格式：6 位数字
	if !isValidResetCode(req.Code) {
		http.Error(w, `{"error":"invalid_code","message":"验证码格式错误，应为 6 位数字"}`, http.StatusBadRequest)
		return
	}
	if err := auth.ValidatePasswordStrength(req.NewPassword); err != nil {
		http.Error(w, `{"error":"weak_password","message":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	if req.NewPassword != req.ConfirmPassword {
		http.Error(w, `{"error":"password_mismatch","message":"两次输入的密码不一致"}`, http.StatusBadRequest)
		return
	}

	// 查询验证码
	userID, used, expiresAt, err := h.db.GetResetCode(email, req.Code)
	if err != nil {
		log.Printf("[Auth] reset password: code not found: email=%s ip=%s", email, r.RemoteAddr)
		http.Error(w, `{"error":"invalid_code","message":"验证码错误或已过期"}`, http.StatusBadRequest)
		return
	}
	if used {
		http.Error(w, `{"error":"code_used","message":"验证码已使用，请重新获取"}`, http.StatusBadRequest)
		return
	}
	if time.Now().After(expiresAt) {
		http.Error(w, `{"error":"code_expired","message":"验证码已过期，请重新获取"}`, http.StatusBadRequest)
		return
	}

	// 标记验证码已使用（防止重放）
	if err := h.db.MarkResetCodeUsed(email, req.Code); err != nil {
		log.Printf("[Auth] reset password: mark code used error: %v", err)
	}

	// 哈希新密码
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		log.Printf("[Auth] reset password: hash error: %v", err)
		http.Error(w, `{"error":"internal_error","message":"密码加密失败"}`, http.StatusInternalServerError)
		return
	}

	// 更新用户密码
	if err := h.db.UpdateUserPassword(userID, hash); err != nil {
		log.Printf("[Auth] reset password: update password error: %v", err)
		http.Error(w, `{"error":"internal_error","message":"更新密码失败"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("[Auth] reset password success: user_id=%s ip=%s", userID, r.RemoteAddr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "密码已重置，请使用新密码登录",
	})
}

// isValidResetCode 校验验证码格式：6 位数字
func isValidResetCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
