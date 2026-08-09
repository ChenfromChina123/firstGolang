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

// POST /api/upload/init
func (h *UploadHandler) InitUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 限流保护：并发超过阈值时返回 429，避免高并发下 P99 飙升（bench 实测 c>1000 后 P99 急升）
	if h.initSem != nil {
		select {
		case h.initSem <- struct{}{}:
			defer func() { <-h.initSem }()
		default:
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "too many concurrent init uploads"})
			return
		}
	}

	var req model.InitUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[Upload] init 400 invalid request: err=%v", err)
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if req.Filename == "" {
		log.Printf("[Upload] init 400: filename empty, file_size=%d", req.FileSize)
		http.Error(w, "filename is required", http.StatusBadRequest)
		return
	}
	if req.FileSize <= 0 {
		log.Printf("[Upload] init 400: file_size<=0, filename=%q, file_size=%d", req.Filename, req.FileSize)
		http.Error(w, "file_size must be greater than 0 (empty file not allowed)", http.StatusBadRequest)
		return
	}

	// PAT 写入配额校验（G3：agent 不可无限写入；仅 PAT 请求生效）
	if err := CheckPATQuota(r.Context(), h.db, req.FileSize); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		json.NewEncoder(w).Encode(map[string]string{"error": "quota_exceeded", "message": "token write quota exhausted"})
		return
	}

	// 文件名路径校验（路径枚举方案：允许 / 作为虚拟目录分隔符，但禁止危险路径）
	// 参考 S3/OSS 命名规范：不允许开头 /、连续 //、.. 路径段、反斜杠
	if err := validateFilePath(req.Filename); err != nil {
		log.Printf("[Upload] init 400 invalid path: filename=%q, err=%v", req.Filename, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.ChunkSize <= 0 {
		req.ChunkSize = 512 * 1024 // default 512KB
	}
	if req.Storage == "" {
		req.Storage = "local"
	}
	// 混合存储：若请求 OSS 但后端未配置，安全降级为本地
	if req.Storage == "s3" && h.storages["s3"] == nil {
		req.Storage = "local"
	}

	totalChunks := int((req.FileSize + req.ChunkSize - 1) / req.ChunkSize)

	// === Conflict check: Redis first, fallback to SQLite ===
	// Redis 集合可能不完整（Redis 重启、文件通过 SQLite path 上传、历史数据残留），
	// 所以 Redis miss 时必须 fallback 到 SQLite 检查，并补标记 Redis 修复集合不一致。
	// owner 过滤：admin 可见所有（owner=""），普通用户仅自己的（避免与其他用户同名文件误判冲突）
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	ownerForCheck := username
	if role == "admin" {
		ownerForCheck = ""
	}
	// 写操作空间归一化：空 → "default-<username>"（「我的空间」），与迁移后历史数据一致
	req.SpaceID = normalizeWriteSpace(req.SpaceID, username)
	var conflictFile *model.FileRecord
	redisHit := false
	if h.redis != nil {
		exists, err := h.redis.FileExists(r.Context(), req.Filename)
		if err == nil && exists {
			redisHit = true
		}
	}
	if redisHit {
		// Redis 命中：查 SQLite 获取完整记录（附带 Existing 字段供前端展示）
		// 空间隔离：冲突检测限定在目标空间内（Redis 为全局集合，需以 SQLite 空间过滤为准）
		existing, _ := h.db.FindFileByName(req.SpaceID, req.Filename, ownerForCheck)
		if existing != nil {
			conflictFile = existing
		} else {
			// Redis 有但 SQLite 没有（孤儿数据），用最小记录
			conflictFile = &model.FileRecord{Filename: req.Filename}
		}
	} else {
		// Redis miss 或无 Redis：查 SQLite
		existing, _ := h.db.FindFileByName(req.SpaceID, req.Filename, ownerForCheck)
		if existing != nil {
			conflictFile = existing
			// 补标记 Redis（修复集合不一致，下次可命中 Redis 快速路径）
			if h.redis != nil {
				h.redis.MarkFileExists(r.Context(), req.Filename)
			}
		}
	}

	if conflictFile != nil {
		force := fastQueryParam(r.URL.RawQuery, "force") == "true"
		rename := fastQueryParam(r.URL.RawQuery, "rename") == "true"
		if !force && !rename {
			resp := conflictResponse{
				Conflict:   true,
				Message:    fmt.Sprintf("file '%s' already exists", conflictFile.Filename),
				Strategies: []string{"skip", "overwrite", "rename"},
			}
			// 有完整记录时附带 Existing 字段（来自 SQLite）
			if conflictFile.ID != "" {
				resp.Existing = &model.FileInfoResponse{
					ID:          conflictFile.ID,
					Filename:    conflictFile.Filename,
					Size:        conflictFile.Size,
					Hash:        conflictFile.Hash,
					StoragePath: conflictFile.StoragePath,
					StorageType: conflictFile.StorageType,
					CreatedAt:   conflictFile.CreatedAt.Format(time.RFC3339),
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(resp)
			return
		}
	}

	// === 断点续传：按 file_hash 查找已有 active session ===
	// 同一文件（file_hash + filename 匹配）若已有 active session，直接复用，
	// 让前端继续上传未完成的 chunks，避免之前已上传的 chunks 作废。
	// 前端会随后调用 /api/upload/status 获取 received_chunks，实现断点续传。
	if req.FileHash != "" {
		existing, err := h.db.FindActiveSessionByHash(req.SpaceID, req.FileHash, req.Filename)
		if err != nil {
			log.Printf("find active session by hash error (non-fatal): %v", err)
		} else if existing != nil {
			// 复用已有 session，重缓存到 Redis（可能已过期）
			if h.redis != nil {
				if err := h.redis.CacheSession(r.Context(), existing); err != nil {
					log.Printf("redis re-cache session error (non-fatal): %v", err)
				}
			}
			log.Printf("resumable upload: reuse session %s for file %s (hash=%s)", existing.ID, req.Filename, req.FileHash)
			resp := model.InitUploadResponse{
				SessionID:   existing.ID,
				Filename:    existing.Filename,
				ChunkSize:   existing.ChunkSize,
				TotalChunks: existing.TotalChunks,
				StorageType: existing.StorageType,
			}
			// presigned 模式断点续传：presigned URL 可能已过期，需重新生成
			if existing.UploadMode == "presigned" && existing.StorageType == "s3" {
				backend := h.backendFor("s3")
				if ps, ok := backend.(storage.PresignedStorage); ok {
					base := filepath.Base(existing.ObjectKey)
					ext := filepath.Ext(existing.Filename)
					fileID := strings.TrimSuffix(base, ext)
					info, err := h.buildPresignedUploadInfo(r.Context(), ps, existing.ID, fileID, existing.Filename, existing.FileSize)
					if err != nil {
						log.Printf("[Upload] presigned resume re-gen error (fallback): %v", err)
					} else {
						resp.Presigned = info
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
	}

	sessionID := generateID()
	// 预先生成 fileID（presigned 模式下用于构造最终对象键，ComposeFile 需要）
	fileID := generateID()

	// === Presigned URL 直连模式：仅 S3 后端支持 ===
	// 检测后端是否实现 PresignedStorage 接口，是则走 presigned 路径（数据不经应用服务器）
	var presignedInfo *model.PresignedUploadInfo
	uploadMode := "relay" // 默认中转模式（兼容旧客户端）
	if req.Storage == "s3" {
		backend := h.backendFor("s3")
		if ps, ok := backend.(storage.PresignedStorage); ok {
			info, err := h.buildPresignedUploadInfo(r.Context(), ps, sessionID, fileID, req.Filename, req.FileSize)
			if err != nil {
				log.Printf("[Upload] presigned init error (fallback to relay): %v", err)
				uploadMode = "relay"
			} else {
				presignedInfo = info
				uploadMode = "presigned"
			}
		}
	}

	// 最终对象键（presigned 模式下记录到 session，CompleteUpload 时复用）
	objectKey := ""
	if uploadMode == "presigned" {
		objectKey = storage.ShardPath(fileID, req.Filename)
	}

	session := &model.UploadSession{
		ID:          sessionID,
		Filename:    req.Filename,
		FileSize:    req.FileSize,
		FileHash:    req.FileHash,
		ChunkSize:   req.ChunkSize,
		TotalChunks: totalChunks,
		Status:      "active",
		StorageType: req.Storage,
		UploadMode:  uploadMode,
		ObjectKey:   objectKey,
		SpaceID:     req.SpaceID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// === Sync SQLite write — ensure session exists before client queries it ===
	// 修复：原 AsyncCreateSession 会导致竞态条件：session 写入异步队列后，
	// 前端立即调用 GetUploadStatus/UploadChunk 时可能因 session 未刷入 DB 而返回 404。
	// 改用同步写入，InitUpload 不是高频路径，P99 < 2ms，无性能影响。
	if err := h.db.CreateUploadSession(session); err != nil {
		log.Printf("[Upload] init create session error: %v", err)
		http.Error(w, "failed to create upload session", http.StatusInternalServerError)
		return
	}
	log.Printf("[Upload] session %s created sync for %d chunks", sessionID, totalChunks)

	// Cache session in Redis for fast chunk verification
	if h.redis != nil {
		// Pipeline: Set session + Expire chunks → 1 RTT
		// 注意：不在此处标记文件名到已完成集合，避免断点续传场景下误报 409 冲突。
		// 文件名标记在 CompleteUpload 成功后通过 MarkFileExists 完成。
		if err := h.redis.CacheSessionPipeline(r.Context(), session); err != nil {
			log.Printf("redis cache session error (non-fatal): %v", err)
		}
	}

	resp := model.InitUploadResponse{
		SessionID:   sessionID,
		Filename:    req.Filename,
		ChunkSize:   req.ChunkSize,
		TotalChunks: totalChunks,
		StorageType: req.Storage,
		Presigned:   presignedInfo,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// buildPresignedUploadInfo 构造 presigned 上传信息（小文件单 URL，大文件分片 URL 列表）。
// 断点续传：复用 sessionID 查询已上传分片，只为缺失分片生成 URL。
// 返回的 PresignedUploadInfo 中 CompletedParts 包含已上传分片编号（前端应跳过）。
func (h *UploadHandler) buildPresignedUploadInfo(
	ctx context.Context,
	ps storage.PresignedStorage,
	sessionID, fileID, filename string,
	fileSize int64,
) (*model.PresignedUploadInfo, error) {
	partSize, partCount := storage.CalculatePartSize(fileSize)
	objectKey := storage.ShardPath(fileID, filename)

	// 小文件（<5MB）：生成单个 presigned PUT URL，客户端直接 PUT 整个文件
	if partCount == 1 {
		uploadURL, err := ps.GeneratePresignedPutURL(ctx, objectKey, storage.PresignedExpiry)
		if err != nil {
			return nil, fmt.Errorf("generate single presigned put url: %w", err)
		}
		return &model.PresignedUploadInfo{
			Mode:       "single",
			ObjectKey:  objectKey,
			UploadURL:  uploadURL,
			PartSize:   fileSize,
			TotalParts: 1,
		}, nil
	}

	// 大文件（≥5MB）：为每个分片生成 presigned PUT URL
	// 断点续传：先查询已上传分片，前端跳过这些分片
	completedParts, err := ps.ListParts(sessionID)
	if err != nil {
		log.Printf("[Upload] ListParts error (non-fatal, treating as fresh upload): %v", err)
		completedParts = nil
	}
	completedSet := make(map[int]bool, len(completedParts))
	for _, p := range completedParts {
		completedSet[p] = true
	}

	parts := make([]model.PresignedPartInfo, 0, partCount)
	for i := 0; i < partCount; i++ {
		partKey := fmt.Sprintf("_parts/%s/part_%06d", sessionID, i)
		uploadURL, err := ps.GeneratePresignedPutURL(ctx, partKey, storage.PresignedExpiry)
		if err != nil {
			return nil, fmt.Errorf("generate part %d presigned put url: %w", i, err)
		}
		offset := int64(i) * partSize
		size := partSize
		if i == partCount-1 {
			// 最后一片可能小于 partSize
			size = fileSize - offset
			if size < 0 {
				size = 0
			}
		}
		parts = append(parts, model.PresignedPartInfo{
			PartNumber: i,
			URL:        uploadURL,
			Offset:     offset,
			Size:       size,
		})
	}

	return &model.PresignedUploadInfo{
		Mode:           "multipart",
		ObjectKey:      objectKey,
		Parts:          parts,
		CompletedParts: completedParts,
		PartSize:       partSize,
		TotalParts:     partCount,
	}, nil
}
