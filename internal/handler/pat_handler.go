package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/store"
)

// ============================================================
// PATHandler 管理个人访问令牌（PAT）
// 供 Web 控制台用户自助生成/查看/吊销 PAT（用于 MCP / 外部 Agent）。
// 挂载路径：/api/tokens（需认证；admin 可查看/吊销任意用户 token）
//
// GET    /api/tokens          - 列出我的 PAT（admin 传 ?all=true 列全部）
// POST   /api/tokens          - 生成 PAT（返回明文一次）
// DELETE /api/tokens/{id}     - 吊销 PAT
// ============================================================

// PATHandler PAT 管理 handler
type PATHandler struct {
	db *store.DB
}

// NewPATHandler 创建 PAT 管理 handler
func NewPATHandler(db *store.DB) *PATHandler {
	return &PATHandler{db: db}
}

// ServeHTTP 路由分发（所有路由需认证）
func (h *PATHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 认证（JWT 或 PAT 均可；但 PAT 本身不管理其它 PAT，避免越权）
	username := auth.UsernameFromContext(r.Context())
	if username == "" {
		http.Error(w, `{"error":"unauthorized","message":"login required"}`, http.StatusUnauthorized)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/tokens")
	path = strings.TrimSuffix(path, "/")

	switch {
	case path == "" && r.Method == http.MethodGet:
		h.listTokens(w, r, username)
	case path == "" && r.Method == http.MethodPost:
		h.createToken(w, r, username)
	case strings.HasPrefix(path, "/") && r.Method == http.MethodDelete:
		tokenID := strings.TrimPrefix(path, "/")
		h.revokeToken(w, r, username, tokenID)
	default:
		http.NotFound(w, r)
	}
}

// listTokens 列出当前用户的未吊销 PAT
func (h *PATHandler) listTokens(w http.ResponseWriter, r *http.Request, username string) {
	// admin 可列出全部（?all=true）
	listAll := r.URL.Query().Get("all") == "true"
	if listAll && auth.RoleFromContext(r.Context()) != "admin" {
		http.Error(w, `{"error":"forbidden","message":"only admin can list all tokens"}`, http.StatusForbidden)
		return
	}
	userID := ""
	if !listAll {
		user, err := h.db.GetUserByUsername(username)
		if err != nil {
			http.Error(w, `{"error":"user_not_found"}`, http.StatusNotFound)
			return
		}
		userID = user.ID
	}
	tokens, err := h.db.ListAPITokens(userID)
	if err != nil {
		log.Printf("[PAT] list error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	// 不返回 token_hash
	type view struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Scopes     string `json:"scopes"`
		SpaceID    string `json:"space_id"`
		PathPrefix string `json:"path_prefix"`
		QuotaBytes int64  `json:"quota_bytes"`
		QuotaUsed  int64  `json:"quota_used"`
		CreatedAt  string `json:"created_at"`
		ExpiresAt  string `json:"expires_at"`
		LastUsedAt string `json:"last_used_at"`
	}
	views := make([]view, 0, len(tokens))
	for _, t := range tokens {
		v := view{
			ID:         t.ID,
			Name:       t.Name,
			Scopes:     t.Scopes,
			SpaceID:    t.SpaceID,
			PathPrefix: t.PathPrefix,
			QuotaBytes: t.QuotaBytes,
			QuotaUsed:  t.QuotaUsed,
			CreatedAt:  t.CreatedAt.Format(time.RFC3339),
		}
		if t.ExpiresAt != nil {
			v.ExpiresAt = t.ExpiresAt.Format(time.RFC3339)
		}
		if t.LastUsedAt != nil {
			v.LastUsedAt = t.LastUsedAt.Format(time.RFC3339)
		}
		views = append(views, v)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"tokens": views})
}

// createToken 生成 PAT。请求体：
// {
//   "name": "claude-desktop",          // 必填，1-64 字符
//   "scopes": ["filesync:read", "filesync:write"],  // 必填，非空
//   "space_id": "",                     // 可选，限定单空间
//   "path_prefix": "",                  // 可选，限定目录前缀
//   "quota_bytes": 0,                   // 可选，写入配额（0=不限）
//   "expires_in": 0                     // 可选，有效期秒（0=永久）
// }
func (h *PATHandler) createToken(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Name       string   `json:"name"`
		Scopes     []string `json:"scopes"`
		SpaceID    string   `json:"space_id"`
		PathPrefix string   `json:"path_prefix"`
		QuotaBytes int64    `json:"quota_bytes"`
		ExpiresIn  int64    `json:"expires_in"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	if req.Name == "" || len(req.Name) > 64 {
		http.Error(w, `{"error":"invalid_name","message":"name required (1-64 chars)"}`, http.StatusBadRequest)
		return
	}
	if len(req.Scopes) == 0 {
		http.Error(w, `{"error":"invalid_scopes","message":"at least one scope required"}`, http.StatusBadRequest)
		return
	}
	// scope 白名单
	allowed := map[string]bool{
		auth.ScopeRead:  true,
		auth.ScopeWrite: true,
		auth.ScopeShare: true,
	}
	seen := make(map[string]bool)
	var scopeList []string
	for _, s := range req.Scopes {
		s = strings.TrimSpace(s)
		if !allowed[s] {
			http.Error(w, `{"error":"invalid_scope","message":"unknown scope: `+s+`"}`, http.StatusBadRequest)
			return
		}
		if !seen[s] {
			seen[s] = true
			scopeList = append(scopeList, s)
		}
	}
	// 校验 space_id（如果指定）
	if req.SpaceID != "" {
		if _, err := h.db.GetSpace(req.SpaceID); err != nil {
			http.Error(w, `{"error":"invalid_space","message":"space not found"}`, http.StatusBadRequest)
			return
		}
	}
	// path_prefix 校验
	if req.PathPrefix != "" {
		if err := validateFilePath(strings.TrimSuffix(req.PathPrefix, "/") + "/x"); err != nil {
			http.Error(w, `{"error":"invalid_path_prefix","message":"invalid path prefix"}`, http.StatusBadRequest)
			return
		}
	}
	if req.QuotaBytes < 0 {
		http.Error(w, `{"error":"invalid_quota"}`, http.StatusBadRequest)
		return
	}

	// 生成 PAT
	plain, hash, err := auth.GeneratePAT()
	if err != nil {
		log.Printf("[PAT] generate error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	user, err := h.db.GetUserByUsername(username)
	if err != nil {
		log.Printf("[PAT] user lookup error: %v", err)
		http.Error(w, `{"error":"user_not_found"}`, http.StatusNotFound)
		return
	}

	now := time.Now()
	rec := &model.APIToken{
		ID:         generateID(),
		UserID:     user.ID,
		Username:   username,
		Name:       req.Name,
		TokenHash:  hash,
		Scopes:     strings.Join(scopeList, " "),
		SpaceID:    req.SpaceID,
		PathPrefix: req.PathPrefix,
		QuotaBytes: req.QuotaBytes,
		CreatedAt:  now,
	}
	if req.ExpiresIn > 0 {
		t := now.Add(time.Duration(req.ExpiresIn) * time.Second)
		rec.ExpiresAt = &t
	}
	if err := h.db.CreateAPIToken(rec); err != nil {
		log.Printf("[PAT] create db error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("[PAT] created token id=%s name=%q user=%s scopes=%v", rec.ID, req.Name, username, scopeList)
	w.Header().Set("Content-Type", "application/json")
	// 201 Created；明文只返回这一次
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"id":          rec.ID,
		"name":        rec.Name,
		"token":       plain,
		"scopes":      rec.Scopes,
		"space_id":    rec.SpaceID,
		"path_prefix": rec.PathPrefix,
		"quota_bytes": rec.QuotaBytes,
		"expires_at":  func() string {
			if rec.ExpiresAt != nil {
				return rec.ExpiresAt.Format(time.RFC3339)
			}
			return ""
		}(),
		"warning": "store this token now; it will not be shown again",
	})
}

// revokeToken 吊销 PAT。本人可吊销自己的；admin 可吊销任意。
func (h *PATHandler) revokeToken(w http.ResponseWriter, r *http.Request, username, tokenID string) {
	tok, err := h.db.GetAPITokenByID(tokenID)
	if err != nil {
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
		return
	}
	role := auth.RoleFromContext(r.Context())
	if tok.Username != username && role != "admin" {
		http.Error(w, `{"error":"forbidden","message":"cannot revoke other user's token"}`, http.StatusForbidden)
		return
	}
	if err := h.db.RevokeAPIToken(tokenID); err != nil {
		log.Printf("[PAT] revoke error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	log.Printf("[PAT] revoked token id=%s by %s", tokenID, username)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"revoked": true, "id": tokenID})
}
