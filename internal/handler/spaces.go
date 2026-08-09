package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/store"
)

// SpaceHandler 管理用户的存储空间（多空间隔离）。
// 路由：
//
//	GET    /api/spaces       列出当前用户的空间（admin 列出全部）
//	POST   /api/spaces       创建空间 {"name":"工作","storage_type":"local"}
//	GET    /api/spaces/{id}  空间详情
//	DELETE /api/spaces/{id}  删除空间（仅空空间，默认空间不可删）
type SpaceHandler struct {
	db *store.DB
}

// NewSpaceHandler creates a new space handler.
func NewSpaceHandler(db *store.DB) *SpaceHandler {
	return &SpaceHandler{db: db}
}

// ServeHTTP 路由分发。
func (h *SpaceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/spaces")
	path = strings.TrimSuffix(path, "/")
	switch {
	case path == "":
		switch r.Method {
		case http.MethodGet:
			h.listSpaces(w, r)
		case http.MethodPost:
			h.createSpace(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case strings.HasPrefix(path, "/"):
		id := strings.TrimPrefix(path, "/")
		switch r.Method {
		case http.MethodGet:
			h.getSpace(w, r, id)
		case http.MethodDelete:
			h.deleteSpace(w, r, id)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

// listSpaces 列出当前用户的空间。
// 普通用户仅列出自己的空间（含自动创建的默认空间）；admin 列出全部。
func (h *SpaceHandler) listSpaces(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())

	owner := username
	if role == "admin" {
		owner = ""
	} else if username != "" {
		// 确保用户存在默认空间记录（注册后可能尚未创建）
		if err := h.db.EnsureUserDefaultSpace(username); err != nil {
			log.Printf("[Space] ensure default space for %s error: %v", username, err)
		}
	}

	spaces, err := h.db.ListSpaces(owner)
	if err != nil {
		log.Printf("[Space] list error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	if spaces == nil {
		spaces = []model.Space{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"spaces": spaces,
		"total":  len(spaces),
	})
}

// createSpace 创建空间。
// 请求体：{"name":"工作空间","storage_type":"local"}（storage_type 可选，默认 local）
func (h *SpaceHandler) createSpace(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		Name        string `json:"name"`
		StorageType string `json:"storage_type,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		http.Error(w, `{"error":"name_required","message":"空间名称不能为空"}`, http.StatusBadRequest)
		return
	}
	if len(req.Name) > 64 {
		http.Error(w, `{"error":"name_too_long","message":"空间名称不能超过 64 字符"}`, http.StatusBadRequest)
		return
	}
	// 存储后端：仅支持已配置的类型（local 必须，s3 可选）
	if req.StorageType == "" {
		req.StorageType = "local"
	}
	if req.StorageType != "local" && req.StorageType != "s3" {
		http.Error(w, `{"error":"invalid_storage_type","message":"storage_type must be local or s3"}`, http.StatusBadRequest)
		return
	}

	now := time.Now()
	space := &model.Space{
		ID:          generateID(),
		Name:        req.Name,
		Owner:       username,
		StorageType: req.StorageType,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if role == "admin" {
		// admin 创建空间归 admin 名下（owner 空表示全局空间）
		space.Owner = ""
	}
	if err := h.db.CreateSpace(space); err != nil {
		log.Printf("[Space] create error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("[Space] created: id=%s name=%s owner=%s type=%s", space.ID, space.Name, space.Owner, space.StorageType)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(space)
}

// getSpace 空间详情（含文件数）。
func (h *SpaceHandler) getSpace(w http.ResponseWriter, r *http.Request, id string) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())

	space, err := h.db.GetSpace(id)
	if err != nil {
		http.Error(w, `{"error":"space_not_found"}`, http.StatusNotFound)
		return
	}
	// 权限：仅空间 owner 或 admin 可访问；默认空间（owner 空）对普通用户开放其默认空间
	if role != "admin" && space.Owner != username && !(space.Owner == "" && id == "default-"+username) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	count, _ := h.db.CountFilesBySpace(id)
	space.FileCount = count

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(space)
}

// deleteSpace 删除空间（仅空空间可删，默认空间不可删）。
func (h *SpaceHandler) deleteSpace(w http.ResponseWriter, r *http.Request, id string) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())

	space, err := h.db.GetSpace(id)
	if err != nil {
		http.Error(w, `{"error":"space_not_found"}`, http.StatusNotFound)
		return
	}
	// 权限：仅空间 owner 或 admin 可删除
	if role != "admin" && space.Owner != username {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	// 默认空间不可删除（文件归 space_id=''，删除会导致其无法被列出）
	if strings.HasPrefix(id, "default-") {
		http.Error(w, `{"error":"default_space_not_deletable","message":"默认空间不可删除"}`, http.StatusConflict)
		return
	}
	// 非空空间不可删除：空间内文件需先移出或删除
	count, _ := h.db.CountFilesBySpace(id)
	if count > 0 {
		http.Error(w, fmt.Sprintf(`{"error":"space_not_empty","message":"空间内还有 %d 个文件，请先清空空间"}`, count), http.StatusConflict)
		return
	}
	if err := h.db.DeleteSpace(id); err != nil {
		log.Printf("[Space] delete error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("[Space] deleted: id=%s name=%s", id, space.Name)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted": true,
		"id":      id,
	})
}
