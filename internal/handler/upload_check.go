package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
)

// CheckUpload 检查文件哈希是否已存在，命中则秒传（全局存储，共享 storage_path）。
// POST /api/upload/check
// 权限：需认证。秒传范围全局（跨用户），相同 hash+size 命中即秒传。
// 命中条件：hash + file_size 双重匹配，任意 owner 的已完成文件。
// 命中后流程：生成新 fileID → 共享 srcFile.StoragePath（不复制物理文件）→ CreateFile 创建记录 → 返回 instant_upload=true
// 全局存储说明：多个用户秒传同一文件时，DB 记录各自独立（owner 不同），但 storage_path 共享同一物理文件。
// 永久删除时通过引用计数（CountByStoragePath）判断是否删除物理文件。
func (h *UploadHandler) CheckUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req model.CheckUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}

	// 参数校验
	if err := validateFilePath(req.Filename); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid_filename","message":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}
	if req.FileSize <= 0 {
		http.Error(w, `{"error":"invalid_file_size"}`, http.StatusBadRequest)
		return
	}
	if len(req.FileHash) != 64 {
		http.Error(w, `{"error":"invalid_file_hash","message":"file_hash must be 64 hex chars (SHA256)"}`, http.StatusBadRequest)
		return
	}

	username := auth.UsernameFromContext(r.Context())
	if username == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	// PAT 写入配额校验（秒传同样占用配额，防止 agent 借秒传绕过写入限制）
	if err := CheckPATQuota(r.Context(), h.db, req.FileSize); err != nil {
		http.Error(w, `{"error":"quota_exceeded","message":"token write quota exhausted"}`, http.StatusRequestEntityTooLarge)
		return
	}
	// 写操作空间归一化：空 → "default-<username>"（「我的空间」），秒传文件归入目标空间
	req.SpaceID = normalizeWriteSpace(req.SpaceID, username)

	// 全局秒传：owner="" 跨用户查找已存在的相同 hash+size 文件
	srcFile, err := h.db.GetFileByHash(req.FileHash, "", req.FileSize)
	if err != nil {
		if err == sql.ErrNoRows {
			// 未命中，需正常上传
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(model.CheckUploadResponse{InstantUpload: false})
			return
		}
		log.Printf("[Upload] check instant upload error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	// 命中：检查文件名冲突（同 owner + 目标空间已有同名活跃文件则不秒传，让前端走冲突处理流程）
	existing, _ := h.db.FindFileByName(req.SpaceID, req.Filename, username)
	if existing != nil && existing.ID != srcFile.ID {
		http.Error(w, `{"error":"filename_conflict","message":"目标文件名已存在"}`, http.StatusConflict)
		return
	}

	// 全局存储：共享 srcFile.StoragePath，不复制物理文件
	newFileID := generateID()
	now := time.Now()
	newRecord := &model.FileRecord{
		ID:          newFileID,
		Filename:    req.Filename,
		Size:        srcFile.Size,
		Hash:        srcFile.Hash,
		StoragePath: srcFile.StoragePath, // 共享源文件物理路径
		StorageType: srcFile.StorageType,
		ChunkSize:   srcFile.ChunkSize,
		TotalChunks: srcFile.TotalChunks,
		Status:      "completed",
		Owner:       username,
		SpaceID:     req.SpaceID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := h.db.CreateFile(newRecord); err != nil {
		log.Printf("[Upload] instant upload create file record error: %v", err)
		http.Error(w, `{"error":"db_create_failed"}`, http.StatusInternalServerError)
		return
	}
	AccountPATQuota(r.Context(), h.db, srcFile.Size)

	// Redis 标记文件存在（若启用 Redis，保持冲突检查集合一致）
	if h.redis != nil {
		h.redis.MarkFileExists(r.Context(), req.Filename)
	}

	log.Printf("[Upload] instant upload success (global): user=%s file=%s hash=%s src=%s shared_path=%s",
		username, req.Filename, req.FileHash[:16], srcFile.ID, srcFile.StoragePath)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(model.CheckUploadResponse{
		InstantUpload: true,
		FileID:        newFileID,
		Filename:      req.Filename,
		Size:          srcFile.Size,
		Hash:          srcFile.Hash,
	})
}
