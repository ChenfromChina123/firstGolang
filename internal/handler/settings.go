package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/store"
)

// SettingsHandler 处理用户跨浏览器配置同步（分片大小、并发数）
type SettingsHandler struct {
	db *store.DB
}

// NewSettingsHandler 创建配置同步 handler
func NewSettingsHandler(db *store.DB) *SettingsHandler {
	return &SettingsHandler{db: db}
}

// settingsRequest 保存配置请求体
type settingsRequest struct {
	ChunkSize   int64 `json:"chunk_size"`
	Concurrency int   `json:"concurrency"`
}

// settingsResponse 配置响应体
type settingsResponse struct {
	Username    string `json:"username"`
	ChunkSize   int64  `json:"chunk_size"`
	Concurrency int    `json:"concurrency"`
}

// ServeHTTP 处理 GET（获取）/ POST（保存）配置
// GET  /api/settings - 返回当前用户配置（不存在则返回默认值）
// POST /api/settings - 保存配置（替换）
func (h *SettingsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	username := auth.UsernameFromContext(r.Context())
	if username == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getSettings(w, r, username)
	case http.MethodPost:
		// 仅管理员可修改分片大小和并发数（普通用户只读）
		role := auth.RoleFromContext(r.Context())
		if role != "admin" {
			http.Error(w, `{"error":"forbidden","message":"only admin can change upload settings"}`, http.StatusForbidden)
			return
		}
		h.saveSettings(w, r, username)
	default:
		http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
	}
}

// getSettings 返回用户配置。数据库无记录时返回默认值（512KB / 3）
func (h *SettingsHandler) getSettings(w http.ResponseWriter, r *http.Request, username string) {
	s, err := h.db.GetUserSettings(username)
	if err != nil {
		log.Printf("[Settings] get error: user=%s err=%v", username, err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	resp := settingsResponse{
		Username:    username,
		ChunkSize:   512 * 1024, // 默认 512KB（与前端 select 默认选中项一致）
		Concurrency: 3,          // 默认 3
	}
	if s != nil {
		resp.ChunkSize = s.ChunkSize
		resp.Concurrency = s.Concurrency
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// saveSettings 保存用户配置（不存在则插入，存在则替换）
func (h *SettingsHandler) saveSettings(w http.ResponseWriter, r *http.Request, username string) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req settingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}
	// 基本校验：防止 0 或负数
	if req.ChunkSize < 1024 {
		req.ChunkSize = 1024 // 最小 1KB
	}
	if req.Concurrency < 1 {
		req.Concurrency = 1
	}
	if req.Concurrency > 16 {
		req.Concurrency = 16
	}

	s := &model.UserSettings{
		Username:    username,
		ChunkSize:   req.ChunkSize,
		Concurrency: req.Concurrency,
		UpdatedAt:   time.Now(),
	}
	if err := h.db.SaveUserSettings(s); err != nil {
		log.Printf("[Settings] save error: user=%s err=%v", username, err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settingsResponse{
		Username:    username,
		ChunkSize:   req.ChunkSize,
		Concurrency: req.Concurrency,
	})
}

// StorageUsage 处理存储用量查询
// GET /api/storage-usage - 返回当前用户存储用量
// admin 可通过 ?username=xxx 查看指定用户，?username= 查看全局
func (h *SettingsHandler) StorageUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	if username == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// admin 可通过 ?username= 查看指定用户或全局；普通用户只能看自己
	targetOwner := username
	if role == "admin" {
		if q := r.URL.Query().Get("username"); q != "" {
			targetOwner = q
		} else if r.URL.Query().Has("username") {
			// ?username= 显式空值 → 全局统计
			targetOwner = ""
		}
	}

	usage, err := h.db.GetUserStorageUsage(targetOwner)
	if err != nil {
		log.Printf("[StorageUsage] error: owner=%s err=%v", targetOwner, err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(usage)
}
