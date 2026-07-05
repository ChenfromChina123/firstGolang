package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/store"
)

// AuthHandler 处理登录/登出/当前用户信息
type AuthHandler struct {
	db  *store.DB
	jwt *auth.JWTManager
}

// NewAuthHandler 创建认证 handler
func NewAuthHandler(db *store.DB, jwt *auth.JWTManager) *AuthHandler {
	return &AuthHandler{db: db, jwt: jwt}
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

	// 查询用户
	user, err := h.db.GetUserByUsername(req.Username)
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
