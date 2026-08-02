package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/service"
)

// ============================================================
// AuthSvcHandler 认证微服务 HTTP 端点
// 挂载路径前缀：/auth/*  (AuthSvc 端口)
// 统一响应格式：所有错误 JSON，所有成功 Content-Type: application/json
// ============================================================

// AuthSvcHandler 组合认证服务 + OAuth 服务 + JWKS 服务
type AuthSvcHandler struct {
	Auth  *service.AuthService
	OAuth *service.OAuthService
	JWKSSvc  *service.JWKSService  // 重命名避免与 JWKS() 方法同名
	// 统一登录页地址（未登录时重定向）
	LoginPageURL string
}

// NewAuthSvcHandler 创建认证 Handler
func NewAuthSvcHandler(
	authSvc *service.AuthService,
	oauthSvc *service.OAuthService,
	jwksSvc *service.JWKSService,
	loginPageURL string,
) *AuthSvcHandler {
	return &AuthSvcHandler{
		Auth:         authSvc,
		OAuth:        oauthSvc,
		JWKSSvc:      jwksSvc,
		LoginPageURL: loginPageURL,
	}
}

// ===== 工具函数 =====

// writeJSON 写 JSON 响应
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

// writeError 写错误 JSON
func writeError(w http.ResponseWriter, status int, errCode, message string) {
	writeJSON(w, status, map[string]any{
		"error":   errCode,
		"message": message,
	})
}

// readBody 限制请求体大小并解析 JSON
func readBody(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return err
	}
	return nil
}

// ===== 1. 健康检查 =====

// Health GET /health
func (h *AuthSvcHandler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "auth-svc",
		"status":  "ok",
		"time":    time.Now().Format(time.RFC3339),
	})
}

// ===== 2. 登录 / 登出 =====

// Login POST /auth/login
// Body: { "username": "...", "password": "...", "client_id": "..." }
func (h *AuthSvcHandler) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		ClientID string `json:"client_id"`
	}
	if err := readBody(w, r, 4096, &body); err != nil {
		return
	}
	result, err := h.Auth.Login(w, r, body.Username, body.Password, body.ClientID)
	if err != nil {
		errMsg := err.Error()
		switch {
		case errors.Is(err, errors.New("invalid username or password")):
			writeError(w, http.StatusUnauthorized, "invalid_credentials", errMsg)
		case errMsg != "" && len(errMsg) > 17 && errMsg[:17] == "account not activ":
			writeError(w, http.StatusForbidden, "account_not_activated", errMsg)
		default:
			writeError(w, http.StatusBadRequest, "login_failed", errMsg)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"user_id":    result.UserID,
		"username":   result.Username,
		"role":       result.Role,
		"expires_in": result.ExpiresIn,
		"token_type": "Bearer",
	})
}

// Logout POST /auth/logout
func (h *AuthSvcHandler) Logout(w http.ResponseWriter, r *http.Request) {
	_ = h.Auth.Logout(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ===== 3. 当前用户信息 =====

// Me GET /auth/me
// 要求：中间件已校验 JWT 并注入 user_id/username/role 到 context
func (h *AuthSvcHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	if userID == "" {
		// 兜底：尝试从 Bearer/SSO 直接解析
		uid, un, rl := h.OAuth.GetCurrentUser(r)
		if uid == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		userID, username, role = uid, un, rl
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":  userID,
		"username": username,
		"role":     role,
	})
}

// ===== 4. 刷新 Token =====

// RefreshToken POST /auth/token/refresh
// Body: { "client_id": "..." } （Refresh Token 走 HttpOnly Cookie）
func (h *AuthSvcHandler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientID string `json:"client_id"`
	}
	_ = readBody(w, r, 2048, &body) // 空 body 也允许
	result, err := h.Auth.RefreshToken(w, r, body.ClientID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "refresh_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": result.AccessToken,
		"token_type":   "Bearer",
		"expires_in":   result.ExpiresIn,
		"username":     result.Username,
		"role":         result.Role,
	})
}

// ===== 5. 注册 =====

// Register POST /auth/register
func (h *AuthSvcHandler) Register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email           string `json:"email"`
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := readBody(w, r, 4096, &body); err != nil {
		return
	}
	if err := h.Auth.Register(body.Email, body.Password, body.ConfirmPassword); err != nil {
		writeError(w, http.StatusBadRequest, "register_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "if email is valid, activation email has been sent",
	})
}

// Activate GET /auth/activate?token=xxx
// 激活后重定向到前端激活结果页
func (h *AuthSvcHandler) Activate(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	err := h.Auth.Activate(token)
	redirectBase := h.LoginPageURL
	if redirectBase == "" {
		redirectBase = "/"
	}
	u, _ := url.Parse(redirectBase)
	q := u.Query()
	if err != nil {
		if errors.Is(err, errors.New("activation token expired")) {
			q.Set("activate", "expired")
		} else {
			q.Set("activate", "error")
		}
	} else {
		q.Set("activate", "success")
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// ===== 6. 忘记密码 / 重置密码 =====

// ForgotPassword POST /auth/password/forgot
func (h *AuthSvcHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := readBody(w, r, 2048, &body); err != nil {
		return
	}
	if err := h.Auth.ForgotPassword(body.Email); err != nil {
		writeError(w, http.StatusBadRequest, "forgot_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "if email registered, reset code has been sent",
	})
}

// ResetPassword POST /auth/password/reset
func (h *AuthSvcHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email           string `json:"email"`
		Code            string `json:"code"`
		NewPassword     string `json:"new_password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := readBody(w, r, 4096, &body); err != nil {
		return
	}
	if err := h.Auth.ResetPassword(body.Email, body.Code, body.NewPassword, body.ConfirmPassword); err != nil {
		writeError(w, http.StatusBadRequest, "reset_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ===== 7. OAuth2 授权端点 =====

// Authorize GET /oauth/authorize
// Query: response_type=code&client_id=xxx&redirect_uri=xxx&scope=...&state=...
func (h *AuthSvcHandler) Authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := service.AuthorizeRequest{
		ResponseType:        q.Get("response_type"),
		ClientID:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		Scope:               q.Get("scope"),
		State:               q.Get("state"),
		Nonce:               q.Get("nonce"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
	}
	result, err := h.OAuth.Authorize(w, r, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "authorize_failed", err.Error())
		return
	}
	if result.NeedLogin {
		// 未登录 → 重定向到统一登录页（附 redirect 参数）
		original := r.URL.String()
		// 处理相对路径
		if original == "" || original[0] == '/' {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			original = scheme + "://" + r.Host + original
		}
		loginURL := service.BuildLoginRedirect(h.LoginPageURL, original)
		// Accept: application/json → 返回 JSON 告知前端跳转；否则 302
		if r.Header.Get("Accept") == "application/json" {
			writeJSON(w, http.StatusOK, map[string]any{
				"need_login": true,
				"login_url":  loginURL,
			})
			return
		}
		http.Redirect(w, r, loginURL, http.StatusFound)
		return
	}
	// 已登录 → 302 重定向到 redirect_uri?code=xxx&state=xxx
	http.Redirect(w, r, result.RedirectURI, http.StatusFound)
}

// Token POST /oauth/token
// Body: grant_type=authorization_code | refresh_token + code/refresh_token + redirect_uri + client_id + client_secret
func (h *AuthSvcHandler) Token(w http.ResponseWriter, r *http.Request) {
	// 支持 application/x-www-form-urlencoded 和 application/json
	var req service.TokenRequest
	contentType := r.Header.Get("Content-Type")
	if contentType != "" && len(contentType) >= 16 && contentType[:16] == "application/json" {
		if err := readBody(w, r, 8192, &req); err != nil {
			return
		}
	} else {
		// form 编码（RFC 6749 标准）
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "parse form failed: "+err.Error())
			return
		}
		req = service.TokenRequest{
			GrantType:    r.FormValue("grant_type"),
			Code:         r.FormValue("code"),
			RedirectURI:  r.FormValue("redirect_uri"),
			ClientID:     r.FormValue("client_id"),
			ClientSecret: r.FormValue("client_secret"),
			RefreshToken: r.FormValue("refresh_token"),
			Scope:        r.FormValue("scope"),
			CodeVerifier: r.FormValue("code_verifier"),
		}
	}
	// Authorization: Basic client_id:client_secret 兜底
	if req.ClientID == "" || req.ClientSecret == "" {
		if cid, csec, ok := r.BasicAuth(); ok {
			if req.ClientID == "" {
				req.ClientID = cid
			}
			if req.ClientSecret == "" {
				req.ClientSecret = csec
			}
		}
	}
	result, err := h.OAuth.Token(w, r, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_grant", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ===== 8. JWKS 公钥端点 =====

// JWKS GET /.well-known/jwks.json
// 公开端点，无认证，供网关和下游服务拉取公钥
func (h *AuthSvcHandler) JWKS(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.JWKSSvc.GetJWKS())
}

// ===== 9. OpenID 配置发现（可选） =====

// OpenIDConfig GET /.well-known/openid-configuration
func (h *AuthSvcHandler) OpenIDConfig(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	base := scheme + "://" + r.Host
	cfg := map[string]any{
		"issuer":                 base,
		"authorization_endpoint": base + "/oauth/authorize",
		"token_endpoint":         base + "/oauth/token",
		"userinfo_endpoint":      base + "/auth/me",
		"jwks_uri":               base + "/.well-known/jwks.json",
		"scopes_supported":       []string{"filesync:read", "filesync:write", "filesync:*"},
		"response_types_supported": []string{"code"},
		"grant_types_supported":    []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"subject_types_supported":                []string{"public"},
	}
	writeJSON(w, http.StatusOK, cfg)
}

// ===== 10. 管理员端点（users CRUD）=====

// ListUsers GET /auth/admin/users?page=1&page_size=20&keyword=
func (h *AuthSvcHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	role := auth.RoleFromContext(r.Context())
	if role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden", "admin required")
		return
	}
	q := r.URL.Query()
	_ = q  // 预留分页参数
	page := 1
	pageSize := 20
	_ = page
	_ = pageSize
	// 简化实现（用户太多时加分页）
	users, err := h.Auth.ListUsersExport()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	// 移除 password_hash
	result := make([]map[string]any, 0, len(users))
	for _, u := range users {
		result = append(result, map[string]any{
			"id":         u.ID,
			"username":   u.Username,
			"email":      u.Email,
			"role":       u.Role,
			"status":     u.Status,
			"created_at": u.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": result, "total": len(result)})
}

// AuthSvc_DB_ListUsers 辅助：暴露给 handler 的用户列表查询
func (h *AuthSvcHandler) AuthSvc_DB_ListUsers() ([]*model.User, error) {
	return h.Auth.ListUsersExport()
}
