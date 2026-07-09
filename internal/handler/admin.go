package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/store"
)

// AdminHandler 处理管理员后台 API（系统统计、用户管理、分享管理）
type AdminHandler struct {
	db  *store.DB
	rsa *auth.RSAKeys // RSA 密钥对，用于解密前端加密的 new_password 字段
}

// NewAdminHandler 创建管理员 handler
// rsaKeys 用于解密重置密码接口前端用公钥加密的 new_password 字段
func NewAdminHandler(db *store.DB, rsaKeys *auth.RSAKeys) *AdminHandler {
	return &AdminHandler{db: db, rsa: rsaKeys}
}

// ServeHTTP 路由分发（所有路由需认证 + admin 权限）
// GET    /api/admin/stats                       - 系统统计总览
// GET    /api/admin/users                       - 用户列表（含各自用量）
// POST   /api/admin/users/{id}/status           - 禁用/启用用户
// POST   /api/admin/users/{id}/reset-password   - 重置用户密码
// GET    /api/admin/shares                      - 所有分享列表
// DELETE /api/admin/shares/{id}                 - 删除分享
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 权限检查：必须是 admin
	role := auth.RoleFromContext(r.Context())
	if role != "admin" {
		http.Error(w, `{"error":"forbidden","message":"需要管理员权限"}`, http.StatusForbidden)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/admin")
	path = strings.TrimSuffix(path, "/")

	switch {
	case path == "/stats" && r.Method == http.MethodGet:
		h.getStats(w, r)
	case path == "/users" && r.Method == http.MethodGet:
		h.listUsers(w, r)
	case strings.HasPrefix(path, "/users/") && strings.HasSuffix(path, "/status") && r.Method == http.MethodPost:
		userID := strings.TrimPrefix(path, "/users/")
		userID = strings.TrimSuffix(userID, "/status")
		h.updateUserStatus(w, r, userID)
	case strings.HasPrefix(path, "/users/") && strings.HasSuffix(path, "/reset-password") && r.Method == http.MethodPost:
		userID := strings.TrimPrefix(path, "/users/")
		userID = strings.TrimSuffix(userID, "/reset-password")
		h.resetUserPassword(w, r, userID)
	case path == "/shares" && r.Method == http.MethodGet:
		h.listShares(w, r)
	case strings.HasPrefix(path, "/shares/") && r.Method == http.MethodDelete:
		shareID := strings.TrimPrefix(path, "/shares/")
		h.deleteShare(w, r, shareID)
	default:
		http.NotFound(w, r)
	}
}

// getStats 返回系统统计总览
// GET /api/admin/stats
func (h *AdminHandler) getStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.db.GetSystemStats()
	if err != nil {
		log.Printf("[Admin] get stats error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// userWithUsage 用户信息含存储用量
type userWithUsage struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
	UsedSize  int64  `json:"used_size"`
	FileCount int64  `json:"file_count"`
}

// listUsers 返回所有用户列表（含各自存储用量）
// GET /api/admin/users
func (h *AdminHandler) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.db.ListUsers()
	if err != nil {
		log.Printf("[Admin] list users error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	result := make([]userWithUsage, 0, len(users))
	for _, u := range users {
		usage, err := h.db.GetUserStorageUsage(u.Username)
		if err != nil {
			log.Printf("[Admin] get user usage error: user=%s err=%v", u.Username, err)
			usage = &store.StorageUsage{}
		}
		result = append(result, userWithUsage{
			ID:        u.ID,
			Username:  u.Username,
			Email:     u.Email,
			Role:      u.Role,
			Status:    u.Status,
			CreatedAt: u.CreatedAt.Unix(),
			UsedSize:  usage.UsedSize,
			FileCount: usage.FileCount,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"users":  result,
		"total":  len(result),
	})
}

// updateStatusRequest 禁用/启用用户请求体
type updateStatusRequest struct {
	Status string `json:"status"` // "active" | "disabled"
}

// updateUserStatus 禁用/启用用户
// POST /api/admin/users/{id}/status  Body: {"status":"disabled"}
func (h *AdminHandler) updateUserStatus(w http.ResponseWriter, r *http.Request, userID string) {
	if userID == "" {
		http.Error(w, `{"error":"id_required"}`, http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var req updateStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}
	if req.Status != "active" && req.Status != "disabled" && req.Status != "pending" {
		http.Error(w, `{"error":"invalid_status","message":"status 必须为 active/disabled/pending"}`, http.StatusBadRequest)
		return
	}

	// 防止管理员禁用自己
	currentUserID := auth.UserIDFromContext(r.Context())
	if currentUserID == userID {
		http.Error(w, `{"error":"cannot_disable_self","message":"不能禁用自己的账号"}`, http.StatusBadRequest)
		return
	}

	// 校验目标用户存在 + 防止降级其他 admin
	target, err := h.db.GetUserByID(userID)
	if err != nil {
		http.Error(w, `{"error":"user_not_found"}`, http.StatusNotFound)
		return
	}
	if target.Role == "admin" {
		http.Error(w, `{"error":"cannot_modify_admin","message":"不能修改管理员账号"}`, http.StatusBadRequest)
		return
	}

	if err := h.db.UpdateUserStatus(userID, req.Status); err != nil {
		log.Printf("[Admin] update user status error: id=%s status=%s err=%v", userID, req.Status, err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	log.Printf("[Admin] user status updated: id=%s username=%s status=%s by=%s",
		userID, target.Username, req.Status, currentUserID)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success":true,"status":"` + req.Status + `"}`))
}

// adminResetPasswordRequest 管理员重置密码请求体
type adminResetPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

// resetUserPassword 管理员重置用户密码
// POST /api/admin/users/{id}/reset-password  Body: {"new_password":"xxx"}
func (h *AdminHandler) resetUserPassword(w http.ResponseWriter, r *http.Request, userID string) {
	if userID == "" {
		http.Error(w, `{"error":"id_required"}`, http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req adminResetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}
	// 解密前端用 RSA 公钥加密的 new_password 字段（不接受明文回退）
	if h.rsa != nil {
		decrypted, err := h.rsa.DecryptPassword(req.NewPassword)
		if err != nil {
			http.Error(w, `{"error":"password_encryption_required","message":"密码必须经 RSA 加密传输"}`, http.StatusBadRequest)
			return
		}
		req.NewPassword = decrypted
	}
	if err := auth.ValidatePasswordStrength(req.NewPassword); err != nil {
		http.Error(w, `{"error":"weak_password","message":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	// 校验目标用户存在 + 防止修改其他 admin 密码
	target, err := h.db.GetUserByID(userID)
	if err != nil {
		http.Error(w, `{"error":"user_not_found"}`, http.StatusNotFound)
		return
	}
	if target.Role == "admin" {
		http.Error(w, `{"error":"cannot_modify_admin","message":"不能修改管理员账号"}`, http.StatusBadRequest)
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		log.Printf("[Admin] hash password error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	if err := h.db.UpdateUserPassword(userID, hash); err != nil {
		log.Printf("[Admin] update password error: id=%s err=%v", userID, err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	currentUserID := auth.UserIDFromContext(r.Context())
	log.Printf("[Admin] user password reset: id=%s username=%s by=%s",
		userID, target.Username, currentUserID)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success":true}`))
}

// shareListItem 分享列表项
type shareListItem struct {
	ID            string `json:"id"`
	ShareType     string `json:"share_type"`
	FileID        string `json:"file_id,omitempty"`
	DirPrefix     string `json:"dir_prefix,omitempty"`
	CreatedBy     string `json:"created_by"`
	CreatedAt     int64  `json:"created_at"`
	ExpiresAt     *int64 `json:"expires_at,omitempty"`
	DownloadCount int    `json:"download_count"`
	IsActive      bool   `json:"is_active"`
	IsExpired     bool   `json:"is_expired"`
	HasPassword   bool   `json:"has_password"`
}

// listShares 返回所有分享列表
// GET /api/admin/shares
func (h *AdminHandler) listShares(w http.ResponseWriter, r *http.Request) {
	shares, err := h.db.ListAllShares()
	if err != nil {
		log.Printf("[Admin] list shares error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	items := make([]shareListItem, 0, len(shares))
	for _, s := range shares {
		var expiresAt *int64
		isExpired := false
		if s.ExpiresAt != nil {
			ts := s.ExpiresAt.Unix()
			expiresAt = &ts
			isExpired = s.ExpiresAt.Before(time.Now())
		}
		items = append(items, shareListItem{
			ID:            s.ID,
			ShareType:     s.ShareType,
			FileID:        s.FileID,
			DirPrefix:     s.DirPrefix,
			CreatedBy:     s.CreatedBy,
			CreatedAt:     s.CreatedAt.Unix(),
			ExpiresAt:     expiresAt,
			DownloadCount: s.DownloadCount,
			IsActive:      s.IsActive,
			IsExpired:     isExpired,
			HasPassword:   s.PasswordHash != "",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"shares": items,
		"total":  len(items),
	})
}

// deleteShare 管理员删除分享
// DELETE /api/admin/shares/{id}
func (h *AdminHandler) deleteShare(w http.ResponseWriter, r *http.Request, shareID string) {
	if shareID == "" {
		http.Error(w, `{"error":"id_required"}`, http.StatusBadRequest)
		return
	}
	if err := h.db.DeleteShare(shareID); err != nil {
		log.Printf("[Admin] delete share error: id=%s err=%v", shareID, err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	currentUser := auth.UsernameFromContext(r.Context())
	log.Printf("[Admin] share deleted: id=%s by=%s", shareID, currentUser)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success":true}`))
}
