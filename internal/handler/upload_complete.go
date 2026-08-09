package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/storage"
)

// CompleteUpload assembles all chunks into the final file
// POST /api/upload/complete
// 权限：文件归属为上传发起者（Owner = username），admin 上传时 Owner 仍为 admin。
func (h *UploadHandler) CompleteUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	// 文件归属：上传发起者（admin 上传的文件 Owner=admin）
	username := auth.UsernameFromContext(r.Context())

	// === Redis fast path ===
	if h.redis != nil {
		locked, err := h.redis.TryLock(r.Context(), req.SessionID)
		if err != nil {
			log.Printf("redis trylock error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !locked {
			http.Error(w, "assembly already in progress", http.StatusConflict)
			return
		}
		defer h.redis.Unlock(r.Context(), req.SessionID)

		session, err := h.redis.GetSession(r.Context(), req.SessionID)
		if err != nil {
			session, err = h.db.GetUploadSession(req.SessionID)
			if err != nil {
				http.Error(w, "session not found", http.StatusNotFound)
				return
			}
		}

		// Wait for pending async chunk writes to complete
		if asyncSt, ok := h.backendFor(session.StorageType).(storage.AsyncStorager); ok {
			asyncSt.WaitAsync()
		}

		// === Presigned 直连模式：跳过 AssembleFile，调用 ComposeFile 服务端合并 ===
		if session.UploadMode == "presigned" {
			fileSize, hash, fileID, filePath, err := h.completePresignedUpload(r.Context(), session, username)
			if err != nil {
				log.Printf("[Upload] presigned complete error: %v", err)
				http.Error(w, "failed to complete presigned upload", http.StatusInternalServerError)
				return
			}

			fileRecord := &model.FileRecord{
				ID:          fileID,
				Filename:    session.Filename,
				Size:        fileSize,
				Hash:        hash,
				StoragePath: filePath,
				StorageType: session.StorageType,
				ChunkSize:   session.ChunkSize,
				TotalChunks: session.TotalChunks,
				Status:      "completed",
				Owner:       username,
				SpaceID:     session.SpaceID,
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}

			// 修复：原 AsyncCreateFileAndStatus 异步写入导致文件记录在 API 返回"成功"后
			// 仍未写入 DB，前端立即刷新文件列表时找不到刚上传的文件。
			h.db.CreateFile(fileRecord)
			h.db.UpdateUploadSessionStatus(req.SessionID, "completed")
			AccountPATQuota(r.Context(), h.db, fileSize)

			if err := h.redis.MarkFileExists(r.Context(), session.Filename); err != nil {
				log.Printf("redis mark file exists error (non-fatal): %v", err)
			}

			h.redis.DeleteSession(r.Context(), req.SessionID)

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(model.CompleteUploadResponse{
				FileID:      fileID,
				Filename:    session.Filename,
				Size:        fileSize,
				Hash:        hash,
				StoragePath: filePath,
			})
			return
		}

		count, err := h.redis.CountReceivedChunks(r.Context(), req.SessionID)
		if err != nil {
			http.Error(w, "failed to verify chunks", http.StatusInternalServerError)
			return
		}
		if int(count) != session.TotalChunks {
			http.Error(w, fmt.Sprintf("not all chunks received: %d/%d",
				count, session.TotalChunks), http.StatusBadRequest)
			return
		}

		var filePath, hash string
		var rawPath string
		fileID := generateID()
		backend := h.backendFor(session.StorageType)
		if assembler, ok := backend.(storage.HashAssembler); ok {
			rawPath, hash, err = assembler.AssembleFileWithHash(req.SessionID, fileID, session.Filename, session.TotalChunks)
		} else {
			rawPath, err = backend.AssembleFile(req.SessionID, fileID, session.Filename, session.TotalChunks)
			if err == nil {
				hash, _ = backend.HashFile(rawPath)
			}
		}
		if err != nil {
			log.Printf("assemble error: %v", err)
			http.Error(w, "failed to assemble file", http.StatusInternalServerError)
			return
		}

		fileSize, _ := backend.FileSize(rawPath)
		filePath = storage.PrefixStoragePath(session.StorageType, rawPath)

		fileRecord := &model.FileRecord{
			ID:          fileID,
			Filename:    session.Filename,
			Size:        fileSize,
			Hash:        hash,
			StoragePath: filePath,
			StorageType: session.StorageType,
			ChunkSize:   session.ChunkSize,
			TotalChunks: session.TotalChunks,
			Status:      "completed",
			Owner:       username,
			SpaceID:     session.SpaceID,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		// 修复：原 AsyncCreateFileAndStatus 异步写入导致文件记录在 API 返回"成功"后
		// 仍未写入 DB，前端立即刷新文件列表时找不到刚上传的文件。
		// 改用同步写入，确保 HTTP 响应返回前文件记录已持久化。
		h.db.CreateFile(fileRecord)
		h.db.UpdateUploadSessionStatus(req.SessionID, "completed")
		AccountPATQuota(r.Context(), h.db, fileSize)

		// 标记文件名到已完成集合（用于 InitUpload 冲突检查）。
		// 必须在文件组装成功后调用，避免断点续传场景下未完成上传也触发 409 冲突。
		if err := h.redis.MarkFileExists(r.Context(), session.Filename); err != nil {
			log.Printf("redis mark file exists error (non-fatal): %v", err)
		}

		// Cleanup Redis + temp chunks
		h.redis.DeleteSession(r.Context(), req.SessionID)
		h.backendFor(session.StorageType).DeleteTemp(req.SessionID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(model.CompleteUploadResponse{
			FileID:      fileID,
			Filename:    session.Filename,
			Size:        fileSize,
			Hash:        hash,
			StoragePath: filePath,
		})
		return
	}

	// === SQLite path ===
	session, err := h.db.GetUploadSession(req.SessionID)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// === Presigned 直连模式（SQLite path）===
	if session.UploadMode == "presigned" {
		fileSize, hash, fileID, filePath, err := h.completePresignedUpload(r.Context(), session, username)
		if err != nil {
			log.Printf("[Upload] presigned complete error: %v", err)
			http.Error(w, "failed to complete presigned upload", http.StatusInternalServerError)
			return
		}

		fileRecord := &model.FileRecord{
			ID:          fileID,
			Filename:    session.Filename,
			Size:        fileSize,
			Hash:        hash,
			StoragePath: filePath,
			StorageType: session.StorageType,
			ChunkSize:   session.ChunkSize,
			TotalChunks: session.TotalChunks,
			Status:      "completed",
			Owner:       username,
			SpaceID:     session.SpaceID,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		h.db.CreateFile(fileRecord)
		h.db.UpdateUploadSessionStatus(req.SessionID, "completed")
		AccountPATQuota(r.Context(), h.db, fileSize)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(model.CompleteUploadResponse{
			FileID:      fileID,
			Filename:    session.Filename,
			Size:        fileSize,
			Hash:        hash,
			StoragePath: filePath,
		})
		return
	}

	if len(session.ReceivedChunks) != session.TotalChunks {
		http.Error(w, fmt.Sprintf("not all chunks received: %d/%d",
			len(session.ReceivedChunks), session.TotalChunks), http.StatusBadRequest)
		return
	}

	fileID := generateID()
	backend := h.backendFor(session.StorageType)
	rawPath, err := backend.AssembleFile(req.SessionID, fileID, session.Filename, session.TotalChunks)
	if err != nil {
		log.Printf("assemble error: %v", err)
		http.Error(w, "failed to assemble file", http.StatusInternalServerError)
		return
	}

	hash, _ := backend.HashFile(rawPath)
	fileSize, _ := backend.FileSize(rawPath)
	filePath := storage.PrefixStoragePath(session.StorageType, rawPath)

	fileRecord := &model.FileRecord{
		ID:          fileID,
		Filename:    session.Filename,
		Size:        fileSize,
		Hash:        hash,
		StoragePath: filePath,
		StorageType: session.StorageType,
		ChunkSize:   session.ChunkSize,
		TotalChunks: session.TotalChunks,
		Status:      "completed",
		Owner:       username,
		SpaceID:     session.SpaceID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	h.db.CreateFile(fileRecord)
	h.db.UpdateUploadSessionStatus(req.SessionID, "completed")
	AccountPATQuota(r.Context(), h.db, fileSize)
	h.backendFor(session.StorageType).DeleteTemp(req.SessionID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(model.CompleteUploadResponse{
		FileID:      fileID,
		Filename:    session.Filename,
		Size:        fileSize,
		Hash:        hash,
		StoragePath: filePath,
	})
}

// completePresignedUpload 处理 presigned 直连模式的完成逻辑：
//   - 小文件：StatObject 验证对象存在并获取大小
//   - 大文件：ComposeFile 服务端合并分片 → StatObject 获取大小 → DeleteParts 清理
//
// 信任客户端提供的 file_hash（与秒传逻辑一致，presigned 模式下服务器无法独立计算哈希）。
// 返回 (fileSize, hash, fileID, storagePath, error)。
func (h *UploadHandler) completePresignedUpload(
	ctx context.Context,
	session *model.UploadSession,
	username string,
) (fileSize int64, hash string, fileID string, storagePath string, err error) {
	backend := h.backendFor(session.StorageType)
	ps, ok := backend.(storage.PresignedStorage)
	if !ok {
		return 0, "", "", "", fmt.Errorf("storage backend does not support presigned (storage_type=%s)", session.StorageType)
	}

	// 从 objectKey 反推 fileID（objectKey = ShardPath(fileID, filename)）
	// 格式可能是 "ab/cd/fileID.ext" 或 "fileID.ext"（短 ID 兜底）
	base := filepath.Base(session.ObjectKey)
	ext := filepath.Ext(session.Filename)
	fileID = strings.TrimSuffix(base, ext)
	if fileID == "" {
		fileID = generateID()
	}

	// 计算分片数（与 InitUpload 一致）
	_, partCount := storage.CalculatePartSize(session.FileSize)

	if partCount == 1 {
		// 小文件：客户端已直接 PUT 到 objectKey，验证对象存在
		fileSize, err = ps.StatObject(session.ObjectKey)
		if err != nil {
			return 0, "", "", "", fmt.Errorf("stat single object %s: %w", session.ObjectKey, err)
		}
	} else {
		// 大文件：调用 ComposeFile 服务端合并分片（数据在 OSS 内部复制，不经过应用服务器）
		composedKey, composeErr := ps.ComposeFile(session.ID, fileID, session.Filename, partCount)
		if composeErr != nil {
			return 0, "", "", "", fmt.Errorf("compose parts: %w", composeErr)
		}
		if composedKey != session.ObjectKey {
			log.Printf("[Upload] WARN: composed key mismatch: expected=%s got=%s", session.ObjectKey, composedKey)
		}

		// 验证合并后的对象
		fileSize, err = ps.StatObject(session.ObjectKey)
		if err != nil {
			return 0, "", "", "", fmt.Errorf("stat composed object %s: %w", session.ObjectKey, err)
		}

		// 清理分片对象
		if delErr := ps.DeleteParts(session.ID); delErr != nil {
			log.Printf("[Upload] cleanup parts error (non-fatal): %v", delErr)
		}
	}

	// 信任客户端提供的 file_hash（与秒传逻辑一致）
	hash = session.FileHash
	storagePath = storage.PrefixStoragePath(session.StorageType, session.ObjectKey)
	return fileSize, hash, fileID, storagePath, nil
}
