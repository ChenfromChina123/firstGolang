package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/storage"
)

// CreateTextFile 创建文本文件（POST /api/files/create）。
// 在线编辑功能：直接写入文本内容到存储，创建文件记录。
// 请求体：{"filename": "notes.txt", "content": "文件内容"}
// 限制：内容最大 10MB；文件名冲突返回 409。
func (h *FileHandler) CreateTextFile(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	var req struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
		SpaceID  string `json:"space_id,omitempty"` // 目标空间 ID（空=「我的空间」）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	// 写操作空间归一化：空 → "default-<username>"（「我的空间」）
	req.SpaceID = normalizeWriteSpace(req.SpaceID, username)

	if err := validateFilePath(req.Filename); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 限制文本文件大小（10MB），避免在线编辑大文件
	if len(req.Content) > 10*1024*1024 {
		http.Error(w, "content too large (max 10MB)", http.StatusBadRequest)
		return
	}

	// PAT 写入配额校验
	if err := CheckPATQuota(r.Context(), h.db, int64(len(req.Content))); err != nil {
		http.Error(w, `{"error":"quota_exceeded","message":"token write quota exhausted"}`, http.StatusRequestEntityTooLarge)
		return
	}

	// 检查同名文件冲突（限定在目标空间内）
	existing, _ := h.db.FindFileByName(req.SpaceID, req.Filename, username)
	if existing != nil {
		http.Error(w, fmt.Sprintf("file '%s' already exists", req.Filename), http.StatusConflict)
		return
	}

	// 生成 fileID 和 storage_path（用 fileID 分片命名，与 filename 解耦）
	fileID := generateID()
	rawPath := h.storages["local"].StoragePathFor(fileID, req.Filename)

	// 写入文件内容到存储
	n, err := h.storages["local"].WriteFile(rawPath, strings.NewReader(req.Content))
	if err != nil {
		log.Printf("create text file %s write error: %v", req.Filename, err)
		http.Error(w, "failed to write file", http.StatusInternalServerError)
		return
	}

	// 计算 hash（用于去重校验）
	hash, _ := h.storages["local"].HashFile(rawPath)
	fullStoragePath := storage.PrefixStoragePath("local", rawPath)

	now := time.Now()
	fileRecord := &model.FileRecord{
		ID:          fileID,
		Filename:    req.Filename,
		Size:        n,
		Hash:        hash,
		StoragePath: fullStoragePath,
		StorageType: "local",
		ChunkSize:   0,
		TotalChunks: 0,
		Status:      "completed",
		Owner:       username,
		SpaceID:     req.SpaceID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := h.db.CreateFile(fileRecord); err != nil {
		log.Printf("create text file %s db error: %v", req.Filename, err)
		h.storages["local"].DeleteFile(rawPath)
		http.Error(w, "failed to create file record", http.StatusInternalServerError)
		return
	}
	AccountPATQuota(r.Context(), h.db, n)

	// 标记 Redis 冲突检查集合
	if h.redis != nil {
		if err := h.redis.MarkFileExists(r.Context(), req.Filename); err != nil {
			log.Printf("redis mark file %s error (non-fatal): %v", req.Filename, err)
		}
	}

	log.Printf("create text file: %s (size=%d) by %s", req.Filename, n, username)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"created":  true,
		"id":       fileID,
		"filename": req.Filename,
		"size":     n,
	})
}

// GetFileContent 获取文件内容（GET /api/files/{id}/content）。
// 在线编辑功能：流式返回文件内容，前端用于加载到编辑器。
func (h *FileHandler) GetFileContent(w http.ResponseWriter, r *http.Request) {
	// 提取 fileID：/api/files/{id}/content -> {id}
	p := strings.TrimSuffix(r.URL.Path, "/")
	fileID := strings.TrimSuffix(strings.TrimPrefix(p, "/api/files/"), "/content")
	if fileID == "" {
		http.Error(w, "file_id required", http.StatusBadRequest)
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

	// 读取文件内容并流式返回
	reader, err := h.storage.ReadFile(f.StoragePath, 0)
	if err != nil {
		log.Printf("get content: file %s read error: %v", fileID, err)
		http.Error(w, "failed to read file", http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, f.Filename))
	io.Copy(w, reader)
}

// UpdateFileContent 更新文件内容（PUT /api/files/{id}/content）。
// 在线编辑功能：覆盖写入新内容，更新文件记录的 size/hash。
// 请求体：{"content": "新的文件内容"}
func (h *FileHandler) UpdateFileContent(w http.ResponseWriter, r *http.Request) {
	// 提取 fileID：/api/files/{id}/content -> {id}
	p := strings.TrimSuffix(r.URL.Path, "/")
	fileID := strings.TrimSuffix(strings.TrimPrefix(p, "/api/files/"), "/content")
	if fileID == "" {
		http.Error(w, "file_id required", http.StatusBadRequest)
		return
	}

	f, err := h.db.GetFile(fileID)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// 权限校验：仅 owner 或 admin 可修改
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	if f.Owner != username && role != "admin" {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}

	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if len(req.Content) > 10*1024*1024 {
		http.Error(w, "content too large (max 10MB)", http.StatusBadRequest)
		return
	}

	// 解析 storage_path 去掉 local: 前缀，获取原始磁盘路径
	rawPath := f.StoragePath
	if strings.HasPrefix(rawPath, storage.LocalPrefix) {
		rawPath = strings.TrimPrefix(rawPath, storage.LocalPrefix)
	}

	// 覆盖写入新内容
	n, err := h.storages["local"].WriteFile(rawPath, strings.NewReader(req.Content))
	if err != nil {
		log.Printf("update content: file %s write error: %v", fileID, err)
		http.Error(w, "failed to write file", http.StatusInternalServerError)
		return
	}

	// 重新计算 hash
	hash, _ := h.storages["local"].HashFile(rawPath)

	// 更新 DB 记录（size/hash/updated_at）
	ownerForUpdate := username
	if role == "admin" {
		ownerForUpdate = ""
	}
	if err := h.db.UpdateFileMeta(fileID, n, hash, ownerForUpdate); err != nil {
		log.Printf("update content: file %s db error: %v", fileID, err)
		http.Error(w, "failed to update file record", http.StatusInternalServerError)
		return
	}

	log.Printf("update content: file %s (%s) size=%d by %s", fileID, f.Filename, n, username)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"updated": true,
		"id":      fileID,
		"size":    n,
		"hash":    hash,
	})
}
