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
	"filesync/internal/storage"
	"filesync/internal/store"
)

// FileHandler handles file listing, deletion, rename, mkdir
type FileHandler struct {
	db      *store.DB
	storage storage.Storage
	redis   *store.RedisCache // 可选 Redis 缓存（用于删除文件时同步清理冲突检查集合）
}

// NewFileHandler creates a new file handler. rc 可为 nil（无 Redis 时）。
func NewFileHandler(db *store.DB, s storage.Storage, rc *store.RedisCache) *FileHandler {
	return &FileHandler{db: db, storage: s, redis: rc}
}

// ListFiles returns all completed files
// GET /api/files
// ListFiles 列出已完成文件。支持 prefix 查询参数按虚拟目录过滤（路径枚举）。
// 例：GET /api/files?prefix=docs/ 返回 docs/ 目录下所有文件（递归）。
// 注：必须用 fastQueryParam 解码（encodeURIComponent 编码 / 为 %2F）。
func (h *FileHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	prefix := fastQueryParam(r.URL.RawQuery, "prefix")
	// admin 可见所有文件；普通用户仅可见自己的文件
	owner := username
	if role == "admin" {
		owner = ""
	}
	files, err := h.db.ListFiles(prefix, owner)
	if err != nil {
		http.Error(w, "failed to list files", http.StatusInternalServerError)
		return
	}

	var resp []model.FileInfoResponse
	for _, f := range files {
		resp = append(resp, model.FileInfoResponse{
			ID:          f.ID,
			Filename:    f.Filename,
			Size:        f.Size,
			Hash:        f.Hash,
			StoragePath: f.StoragePath,
			StorageType: f.StorageType,
			CreatedAt:   f.CreatedAt.Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GetFileInfo returns details of a single file
// GET /api/files/{fileID}
func (h *FileHandler) GetFileInfo(w http.ResponseWriter, r *http.Request) {
	// If path is exactly /api/files or /api/files/, list all files
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/files" {
		h.ListFiles(w, r)
		return
	}

	fileID := strings.TrimPrefix(r.URL.Path, "/api/files/")
	fileID = strings.TrimSuffix(fileID, "/")

	if fileID == "" {
		h.ListFiles(w, r)
		return
	}

	f, err := h.db.GetFile(fileID)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// 权限校验：仅 owner 或 admin 可访问
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	if f.Owner != username && role != "admin" {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}

	resp := model.FileInfoResponse {
		ID:          f.ID,
		Filename:    f.Filename,
		Size:        f.Size,
		Hash:        f.Hash,
		StoragePath: f.StoragePath,
		StorageType: f.StorageType,
		CreatedAt:   f.CreatedAt.Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ServeHTTP routes file-related requests
func (h *FileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	path := strings.TrimSuffix(r.URL.Path, "/")

	switch {
	case path == "/api/files/mkdir" && r.Method == http.MethodPost:
		h.Mkdir(w, r)
		return
	case path == "/api/files/rename" && r.Method == http.MethodPost:
		h.Rename(w, r)
		return
	case path == "/api/files/move-dir" && r.Method == http.MethodPost:
		h.MoveDir(w, r)
		return
	case path == "/api/files/batch-delete" && r.Method == http.MethodPost:
		h.BatchDelete(w, r)
		return
	case path == "/api/files" && r.Method == http.MethodDelete:
		h.DeleteDir(w, r) // DELETE /api/files?prefix=xxx 删除目录
		return
	case strings.HasPrefix(path, "/api/files/") && r.Method == http.MethodDelete:
		h.DeleteFileByID(w, r) // DELETE /api/files/{id} 删除单个文件
		return
	}

	// 默认：GET /api/files（列表）或 /api/files/{id}（详情）
	h.GetFileInfo(w, r)
}

// DeleteFileByID 删除单个文件（DELETE /api/files/{id}）。
// 同时删除存储文件和数据库记录。
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

	// 先删存储文件，再删数据库记录
	if err := h.storage.DeleteFile(f.StoragePath); err != nil {
		log.Printf("delete storage file %s error: %v", f.StoragePath, err)
		// 存储删除失败不阻断，继续删数据库记录（避免残留记录）
	}

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
		"deleted": true,
		"id":      fileID,
		"filename": f.Filename,
	})
}

// DeleteDir 删除目录下所有文件（DELETE /api/files?prefix=xxx）。
// 递归匹配 prefix 下所有文件（含子目录），逐个删除存储文件 + 数据库记录。
func (h *FileHandler) DeleteDir(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	prefix := fastQueryParam(r.URL.RawQuery, "prefix")
	if prefix == "" {
		http.Error(w, "prefix required", http.StatusBadRequest)
		return
	}

	// admin 可删除所有；普通用户仅删自己的
	owner := username
	if role == "admin" {
		owner = ""
	}
	files, err := h.db.DeleteFilesByPrefix(prefix, owner)
	if err != nil {
		log.Printf("delete files by prefix %s error: %v", prefix, err)
		http.Error(w, "failed to delete directory", http.StatusInternalServerError)
		return
	}

	// 逐个删除存储文件 + 清理 Redis 冲突检查集合
	var storageErrors int
	for _, f := range files {
		if err := h.storage.DeleteFile(f.StoragePath); err != nil {
			log.Printf("delete storage file %s error: %v", f.StoragePath, err)
			storageErrors++
		}
		// 同步清理 Redis 集合中的文件名
		if h.redis != nil {
			if err := h.redis.UnmarkFileExists(r.Context(), f.Filename); err != nil {
				log.Printf("redis unmark file %s error (non-fatal): %v", f.Filename, err)
			}
		}
	}

	log.Printf("deleted directory %s: %d files (%d storage errors)", prefix, len(files), storageErrors)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted":        true,
		"prefix":         prefix,
		"files_deleted":  len(files),
		"storage_errors": storageErrors,
	})
}

// Mkdir 新建虚拟目录（POST /api/files/mkdir）。
// 路径枚举方案下，目录通过创建 .keep 占位文件实现存在。
// 前端和 ZIP 打包会过滤 .keep 文件，用户不可见。
func (h *FileHandler) Mkdir(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	// 规范化目录路径：去开头 /、合并 //、末尾补 /
	dirPath := normalizeDirPath(req.Path)
	if dirPath == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	// 校验目录路径（路径枚举规则）
	if err := validateFilePath(dirPath + ".keep"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 检查目录是否已存在（有文件即存在）
	existing, _ := h.db.ListFiles(dirPath, username)
	if len(existing) > 0 {
		http.Error(w, "directory already exists", http.StatusConflict)
		return
	}

	// 创建 .keep 占位文件（存储键用 fileID 分片命名，与 filename 解耦）
	filename := dirPath + ".keep"
	sessionID := generateID()
	fileID := generateID()
	storagePath, err := h.storage.AssembleFile(sessionID, fileID, filename, 0)
	if err != nil {
		log.Printf("mkdir assemble file %s error: %v", filename, err)
		http.Error(w, "failed to create directory", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	fileRecord := &model.FileRecord{
		ID:          fileID,
		Filename:    filename,
		Size:        0,
		Hash:        "",
		StoragePath: storagePath,
		StorageType: "local",
		ChunkSize:   512,
		TotalChunks: 0,
		Status:      "completed",
		Owner:       username,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := h.db.CreateFile(fileRecord); err != nil {
		log.Printf("mkdir create file record %s error: %v", filename, err)
		// 回滚：删除已创建的存储文件
		h.storage.DeleteFile(storagePath)
		http.Error(w, "failed to create directory record", http.StatusInternalServerError)
		return
	}

	log.Printf("mkdir: created directory %s (.keep placeholder)", dirPath)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"created": true,
		"path":    dirPath,
	})
}

// Rename 重命名/移动文件（POST /api/files/rename）。
// 修改 filename 字段（含路径前缀），实现文件移动到其他目录。
// 存储文件不移动（storage_path 不变），仅改 filename 虚拟路径。
func (h *FileHandler) Rename(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	var req struct {
		ID          string `json:"id"`
		NewFilename string `json:"new_filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	if req.ID == "" || req.NewFilename == "" {
		http.Error(w, "id and new_filename required", http.StatusBadRequest)
		return
	}

	// 校验新文件名路径
	if err := validateFilePath(req.NewFilename); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 检查文件是否存在
	f, err := h.db.GetFile(req.ID)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// 权限校验：仅 owner 或 admin 可重命名
	if f.Owner != username && role != "admin" {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}

	// 检查新文件名是否冲突（同名校验，按当前用户过滤）
	if f.Filename != req.NewFilename {
		existing, _ := h.db.FindFileByName(req.NewFilename, username)
		if existing != nil {
			http.Error(w, fmt.Sprintf("file '%s' already exists", req.NewFilename), http.StatusConflict)
			return
		}
	}

	// 更新文件名（storage_path 不变，仅改虚拟路径）
	ownerForUpdate := username
	if role == "admin" {
		ownerForUpdate = ""
	}
	if err := h.db.UpdateFilename(req.ID, req.NewFilename, ownerForUpdate); err != nil {
		log.Printf("rename file %s error: %v", req.ID, err)
		http.Error(w, "failed to rename file", http.StatusInternalServerError)
		return
	}

	// 同步更新 Redis 冲突检查集合：旧文件名移除，新文件名加入
	if h.redis != nil {
		if err := h.redis.UnmarkFileExists(r.Context(), f.Filename); err != nil {
			log.Printf("redis unmark old file %s error (non-fatal): %v", f.Filename, err)
		}
		if err := h.redis.MarkFileExists(r.Context(), req.NewFilename); err != nil {
			log.Printf("redis mark new file %s error (non-fatal): %v", req.NewFilename, err)
		}
	}

	log.Printf("rename: %s -> %s", f.Filename, req.NewFilename)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"renamed":      true,
		"id":           req.ID,
		"old_filename": f.Filename,
		"new_filename": req.NewFilename,
	})
}

// MoveDir 移动目录（POST /api/files/move-dir）。
// 批量更新目录下所有文件的 filename 前缀，实现目录移动。
// 存储文件不移动（storage_path 不变），仅改 filename 虚拟路径。
// 权限：admin 可移动任意目录，普通用户仅可移动自己的目录。
// 请求体：{"old_prefix":"old_dir/","new_prefix":"new_dir/"}
func (h *FileHandler) MoveDir(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	var req struct {
		OldPrefix string `json:"old_prefix"`
		NewPrefix string `json:"new_prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	req.OldPrefix = normalizeDirPath(req.OldPrefix)
	req.NewPrefix = normalizeDirPath(req.NewPrefix)
	if req.OldPrefix == "" || req.NewPrefix == "" {
		http.Error(w, "old_prefix and new_prefix required", http.StatusBadRequest)
		return
	}
	if req.OldPrefix == req.NewPrefix {
		http.Error(w, "old_prefix and new_prefix are the same", http.StatusBadRequest)
		return
	}

	// admin 可见所有；普通用户仅自己的（owner 过滤）
	owner := username
	if role == "admin" {
		owner = ""
	}

	// 获取源目录下所有文件
	files, err := h.db.ListFiles(req.OldPrefix, owner)
	if err != nil {
		http.Error(w, "failed to list files", http.StatusInternalServerError)
		return
	}
	if len(files) == 0 {
		http.Error(w, "source directory is empty or not found", http.StatusNotFound)
		return
	}

	// 检查目标目录是否已有同名文件（冲突检查，按当前用户过滤）
	targetFiles, _ := h.db.ListFiles(req.NewPrefix, owner)
	if len(targetFiles) > 0 {
		http.Error(w, "target directory already has files", http.StatusConflict)
		return
	}

	// 批量更新 filename 前缀（ownerForUpdate 与查询保持一致）
	ownerForUpdate := owner
	var successCount, failCount int
	for _, f := range files {
		newFilename := req.NewPrefix + strings.TrimPrefix(f.Filename, req.OldPrefix)
		if err := h.db.UpdateFilename(f.ID, newFilename, ownerForUpdate); err != nil {
			log.Printf("move file %s error: %v", f.ID, err)
			failCount++
			continue
		}
		// 同步更新 Redis 冲突检查集合
		if h.redis != nil {
			h.redis.UnmarkFileExists(r.Context(), f.Filename)
			h.redis.MarkFileExists(r.Context(), newFilename)
		}
		successCount++
	}

	log.Printf("move-dir: %s -> %s (%d success, %d fail)", req.OldPrefix, req.NewPrefix, successCount, failCount)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"moved":      true,
		"old_prefix": req.OldPrefix,
		"new_prefix": req.NewPrefix,
		"success":    successCount,
		"fail":       failCount,
	})
}

// BatchDelete 批量删除文件（POST /api/files/batch-delete）。
// 请求体：{"ids":["id1","id2",...]}
// 权限：仅 owner 或 admin 可删除；非 owner 文件跳过并计入失败。
// 逐个删除文件（存储 + 数据库 + Redis），返回成功/失败计数。
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
		if err := h.storage.DeleteFile(f.StoragePath); err != nil {
			log.Printf("batch-delete: storage delete %s error: %v", f.StoragePath, err)
		}
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

	log.Printf("batch-delete: %d success, %d fail", successCount, failCount)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted": true,
		"success": successCount,
		"fail":    failCount,
	})
}

// normalizeDirPath 规范化目录路径：去开头 /、合并 //、末尾补 /。
func normalizeDirPath(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "\\", "/")
	s = strings.TrimPrefix(s, "/")
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	if s != "" && !strings.HasSuffix(s, "/") {
		s += "/"
	}
	return s
}
