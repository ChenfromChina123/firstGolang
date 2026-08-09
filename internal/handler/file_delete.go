package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"filesync/internal/auth"
)

// DeleteFileByID 删除单个文件（DELETE /api/files/{id}）。
// 软删除：标记 deleted_at 移入回收站，保留存储文件以便恢复。30 天后自动清理。
func (h *FileHandler) DeleteFileByID(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimPrefix(r.URL.Path, "/api/files/")
	fileID = strings.TrimSuffix(fileID, "/")
	if fileID == "" {
		http.Error(w, "file_id required", http.StatusBadRequest)
		return
	}

	f, err := h.db.GetFile(fileID)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// 权限校验：仅 owner 或 admin 可删除
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	if f.Owner != username && role != "admin" {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}

	// 软删除：仅标记 deleted_at，保留存储文件（回收站可恢复）
	if _, err := h.db.DeleteFile(fileID); err != nil {
		http.Error(w, "failed to delete file record", http.StatusInternalServerError)
		return
	}

	// 同步清理 Redis 冲突检查集合中的文件名，否则删除后再上传同名文件会误报 409 冲突
	if h.redis != nil {
		if err := h.redis.UnmarkFileExists(r.Context(), f.Filename); err != nil {
			log.Printf("redis unmark file %s error (non-fatal): %v", f.Filename, err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted":  true,
		"id":       fileID,
		"filename": f.Filename,
		"trashed":  true, // 标识已移入回收站
	})
}

// DeleteDir 删除目录下所有文件（DELETE /api/files?prefix=xxx）。
// 软删除：递归匹配 prefix 下所有文件，标记 deleted_at 移入回收站，保留存储文件以便恢复。
func (h *FileHandler) DeleteDir(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	// 空间隔离：与列表同源，通过 space_id query 指定（admin 默认全部空间，普通用户默认「我的空间」）
	spaceID := resolveSpaceID(spaceIDFromRequest(r, role), username, role)
	// 规范化目录前缀：补全末尾斜杠。修复：若 prefix 不带尾斜杠（如 "docs"），
	// LIKE 'docs%' 会误匹配根目录下的 "docs.txt" 等文件，导致误删。
	prefix := normalizeDirPath(fastQueryParam(r.URL.RawQuery, "prefix"))
	if prefix == "" {
		http.Error(w, "prefix required", http.StatusBadRequest)
		return
	}

	// admin 可删除所有；普通用户仅删自己的
	owner := username
	if role == "admin" {
		owner = ""
	}
	files, err := h.db.DeleteFilesByPrefix(spaceID, prefix, owner)
	if err != nil {
		log.Printf("delete files by prefix %s error: %v", prefix, err)
		http.Error(w, "failed to delete directory", http.StatusInternalServerError)
		return
	}
	// 目录不存在（无任何文件）时返回 404，避免静默成功导致前端缓存未刷新时误以为删除失败
	if len(files) == 0 {
		http.Error(w, "directory not found", http.StatusNotFound)
		return
	}

	// 清理 Redis 冲突检查集合中的文件名（存储文件保留，回收站可恢复）
	for _, f := range files {
		if h.redis != nil {
			if err := h.redis.UnmarkFileExists(r.Context(), f.Filename); err != nil {
				log.Printf("redis unmark file %s error (non-fatal): %v", f.Filename, err)
			}
		}
	}

	log.Printf("trashed directory %s: %d files (soft delete)", prefix, len(files))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted":       true,
		"prefix":        prefix,
		"files_deleted": len(files),
		"trashed":       true,
	})
}

// BatchDelete 批量删除文件（POST /api/files/batch-delete）。
// 请求体：{"ids":["id1","id2",...]}
// 权限：仅 owner 或 admin 可删除；非 owner 文件跳过并计入失败。
// 软删除：标记 deleted_at 移入回收站，保留存储文件以便恢复。
func (h *FileHandler) BatchDelete(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	if len(req.IDs) == 0 {
		http.Error(w, "ids required", http.StatusBadRequest)
		return
	}

	var successCount, failCount int
	for _, fileID := range req.IDs {
		f, err := h.db.GetFile(fileID)
		if err != nil {
			log.Printf("batch-delete: file %s not found: %v", fileID, err)
			failCount++
			continue
		}
		// 权限校验：非 owner 且非 admin 跳过
		if f.Owner != username && role != "admin" {
			log.Printf("batch-delete: forbidden file %s (owner=%s, user=%s)", fileID, f.Owner, username)
			failCount++
			continue
		}
		// 软删除：仅标记 deleted_at，保留存储文件（回收站可恢复）
		if _, err := h.db.DeleteFile(fileID); err != nil {
			log.Printf("batch-delete: db delete %s error: %v", fileID, err)
			failCount++
			continue
		}
		if h.redis != nil {
			h.redis.UnmarkFileExists(r.Context(), f.Filename)
		}
		successCount++
	}

	log.Printf("batch-delete: %d success, %d fail (soft delete)", successCount, failCount)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted": true,
		"success": successCount,
		"fail":    failCount,
		"trashed": true,
	})
}
