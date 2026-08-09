package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/storage"
	"filesync/internal/store"
)

// trashRetentionDays 回收站文件保留天数（超过后自动清理）
const trashRetentionDays = 30

// TrashHandler 处理回收站相关操作：列出、恢复、永久删除、清空。
// 回收站文件为软删除（deleted_at IS NOT NULL），保留存储文件以便恢复。
type TrashHandler struct {
	db      *store.DB
	storage storage.Storage
	redis   *store.RedisCache
}

// NewTrashHandler 创建回收站 handler。rc 可为 nil（无 Redis 时）。
func NewTrashHandler(db *store.DB, s storage.Storage, rc *store.RedisCache) *TrashHandler {
	return &TrashHandler{db: db, storage: s, redis: rc}
}

// ServeHTTP 路由分发回收站请求。
// GET    /api/trash           — 列出回收站文件
// POST   /api/trash/{id}/restore — 恢复文件
// DELETE /api/trash/{id}      — 永久删除单个文件
// DELETE /api/trash           — 清空回收站（永久删除所有）
func (h *TrashHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// 路由匹配
	switch {
	case path == "/api/trash" && r.Method == http.MethodGet:
		h.ListTrash(w, r)
	case path == "/api/trash" && r.Method == http.MethodDelete:
		h.EmptyTrash(w, r)
	case strings.HasPrefix(path, "/api/trash/") && strings.HasSuffix(path, "/restore") && r.Method == http.MethodPost:
		h.RestoreFile(w, r)
	case strings.HasPrefix(path, "/api/trash/") && r.Method == http.MethodDelete:
		h.PermanentDelete(w, r)
	default:
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
	}
}

// ListTrash 列出回收站中的文件（GET /api/trash）。
// 普通用户仅看到自己的回收站文件；admin 可看到所有用户的（通过 ?all=true 查询参数）。
// 返回 TrashItem 数组，包含删除时间和过期时间（30 天后自动清理）。
func (h *TrashHandler) ListTrash(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())

	// 空间隔离：通过 space_id query 指定（admin 默认全部空间，普通用户默认「我的空间」）
	spaceID := resolveSpaceID(spaceIDFromRequest(r, role), username, role)
	// admin 可查看所有；普通用户仅自己的
	owner := username
	if role == "admin" && r.URL.Query().Get("all") == "true" {
		owner = ""
	}

	files, err := h.db.ListTrashedFiles(spaceID, owner)
	if err != nil {
		log.Printf("[Trash] list error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	// 转换为 TrashItem 响应
	items := make([]model.TrashItem, 0, len(files))
	now := time.Now()
	for _, f := range files {
		item := model.TrashItem{
			ID:        f.ID,
			Filename:  f.Filename,
			Size:      f.Size,
			Hash:      f.Hash,
			Owner:     f.Owner,
			CreatedAt: f.CreatedAt.Format(time.RFC3339),
		}
		if f.DeletedAt != nil {
			item.DeletedAt = f.DeletedAt.Format(time.RFC3339)
			expiresAt := f.DeletedAt.AddDate(0, 0, trashRetentionDays)
			item.ExpiresAt = expiresAt.Format(time.RFC3339)
			item.IsExpired = now.After(expiresAt)
		}
		items = append(items, item)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"items":     items,
		"total":     len(items),
		"retention": trashRetentionDays,
	})
}

// RestoreFile 恢复回收站中的文件（POST /api/trash/{id}/restore）。
// 将 deleted_at 设为 NULL，文件重新出现在文件列表中。
// 权限：仅 owner 或 admin 可恢复。
// 注意：如果恢复后文件名与现有文件冲突，返回 409 错误（调用方需重命名后恢复）。
func (h *TrashHandler) RestoreFile(w http.ResponseWriter, r *http.Request) {
	// 从路径提取 file ID：/api/trash/{id}/restore
	path := strings.TrimPrefix(r.URL.Path, "/api/trash/")
	fileID := strings.TrimSuffix(path, "/restore")
	if fileID == "" {
		http.Error(w, `{"error":"file_id_required"}`, http.StatusBadRequest)
		return
	}

	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())

	// 权限校验：admin 可恢复所有，普通用户仅自己的
	owner := username
	if role == "admin" {
		owner = ""
	}

	// 检查恢复后是否会文件名冲突
	// 先获取回收站文件信息（需要绕过 deleted_at IS NULL 过滤）
	// 空间隔离：admin 默认全部空间，普通用户按「我的空间」过滤
	trashedFiles, err := h.db.ListTrashedFiles(resolveSpaceID(spaceIDFromRequest(r, role), username, role), owner)
	if err != nil {
		log.Printf("[Trash] restore: list trashed error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	var targetFile *model.FileRecord
	for i := range trashedFiles {
		if trashedFiles[i].ID == fileID {
			targetFile = &trashedFiles[i]
			break
		}
	}
	if targetFile == nil {
		http.Error(w, `{"error":"file_not_in_trash"}`, http.StatusNotFound)
		return
	}

	// 检查文件名冲突（正常文件中是否有同名，限定在恢复目标空间内）
	existing, _ := h.db.FindFileByName(targetFile.SpaceID, targetFile.Filename, owner)
	if existing != nil {
		http.Error(w, `{"error":"filename_conflict","message":"恢复失败：同名文件已存在，请先重命名现有文件"}`, http.StatusConflict)
		return
	}

	// 执行恢复
	affected, err := h.db.RestoreFile(fileID, owner)
	if err != nil {
		log.Printf("[Trash] restore %s error: %v", fileID, err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	if affected == 0 {
		http.Error(w, `{"error":"file_not_in_trash"}`, http.StatusNotFound)
		return
	}

	// 恢复后重新标记 Redis 冲突检查集合
	if h.redis != nil {
		h.redis.MarkFileExists(r.Context(), targetFile.Filename)
	}

	log.Printf("[Trash] restored: file=%s user=%s", fileID, username)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"restored": true,
		"id":       fileID,
		"filename": targetFile.Filename,
	})
}

// PermanentDelete 永久删除回收站中的单个文件（DELETE /api/trash/{id}）。
// 物理删除数据库记录 + 删除存储文件（不可恢复）。
// 权限：仅 owner 或 admin 可永久删除。
func (h *TrashHandler) PermanentDelete(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimPrefix(r.URL.Path, "/api/trash/")
	fileID = strings.TrimSuffix(fileID, "/")
	if fileID == "" {
		http.Error(w, `{"error":"file_id_required"}`, http.StatusBadRequest)
		return
	}

	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	owner := username
	if role == "admin" {
		owner = ""
	}

	f, affected, err := h.db.PermanentlyDeleteFile(fileID, owner)
	if err != nil {
		log.Printf("[Trash] permanent delete %s error: %v", fileID, err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	if affected == 0 {
		http.Error(w, `{"error":"file_not_in_trash"}`, http.StatusNotFound)
		return
	}

	// 全局存储引用计数：检查是否还有其他记录引用同一 storage_path
	// count > 0 说明还有其他用户/记录共享此物理文件，不能删除物理文件
	refCount, err := h.db.CountByStoragePath(f.StoragePath)
	if err != nil {
		log.Printf("[Trash] count by storage path %s error: %v (skip physical delete)", f.StoragePath, err)
	} else if refCount > 0 {
		log.Printf("[Trash] skip physical delete %s: %d other references exist (global storage)",
			f.StoragePath, refCount)
	} else {
		// 引用计数为 0，安全删除物理文件
		if err := h.storage.DeleteFile(f.StoragePath); err != nil {
			log.Printf("[Trash] permanent delete storage %s error: %v", f.StoragePath, err)
			// 存储删除失败不阻断（数据库已删除，避免残留记录）
		}
	}

	log.Printf("[Trash] permanently deleted: file=%s user=%s", fileID, username)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted":  true,
		"id":       fileID,
		"filename": f.Filename,
	})
}

// EmptyTrash 清空回收站（DELETE /api/trash）。
// 永久删除回收站中的所有文件（数据库记录 + 存储文件）。
// 权限：普通用户清空自己的；admin 可清空所有（通过 ?all=true 查询参数）。
func (h *TrashHandler) EmptyTrash(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	owner := username
	if role == "admin" && r.URL.Query().Get("all") == "true" {
		owner = ""
	}

	// 空间隔离：admin 默认全部空间，普通用户按「我的空间」过滤
	// 获取回收站所有文件
	files, err := h.db.ListTrashedFiles(resolveSpaceID(spaceIDFromRequest(r, role), username, role), owner)
	if err != nil {
		log.Printf("[Trash] empty: list error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	if len(files) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"deleted": true,
			"count":   0,
		})
		return
	}

	// 逐个永久删除（数据库 + 存储），全局存储引用计数检查
	var successCount, failCount int
	for _, f := range files {
		_, affected, err := h.db.PermanentlyDeleteFile(f.ID, owner)
		if err != nil || affected == 0 {
			log.Printf("[Trash] empty: delete %s error: %v", f.ID, err)
			failCount++
			continue
		}
		// 引用计数：还有其他记录引用同一 storage_path 时不删物理文件
		refCount, err := h.db.CountByStoragePath(f.StoragePath)
		if err != nil {
			log.Printf("[Trash] empty: count refs %s error: %v (skip physical delete)", f.StoragePath, err)
		} else if refCount > 0 {
			log.Printf("[Trash] empty: skip physical delete %s: %d refs exist", f.StoragePath, refCount)
		} else {
			if err := h.storage.DeleteFile(f.StoragePath); err != nil {
				log.Printf("[Trash] empty: storage delete %s error: %v", f.StoragePath, err)
			}
		}
		successCount++
	}

	log.Printf("[Trash] emptied: user=%s success=%d fail=%d", username, successCount, failCount)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted": true,
		"count":   successCount,
		"fail":    failCount,
	})
}
