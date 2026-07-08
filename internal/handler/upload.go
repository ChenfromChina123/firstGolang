package handler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/store"
	"filesync/internal/storage"
)

// Pre-defined conflict response types to avoid map[string]interface{} allocations.
type conflictResponse struct {
	Conflict   bool     `json:"conflict"`
	Message    string   `json:"message"`
	Strategies []string `json:"strategies"`
	Existing   *model.FileInfoResponse `json:"existing,omitempty"`
}

// fastQueryParam extracts a single query param from RawQuery without map allocation.
// Returns empty string if key is not present or value is empty.
func fastQueryParam(rawQuery, key string) string {
	if rawQuery == "" {
		return ""
	}
	prefix := key + "="
	idx := strings.Index(rawQuery, prefix)
	if idx < 0 {
		return ""
	}
	start := idx + len(prefix)
	end := strings.IndexByte(rawQuery[start:], '&')
	if end < 0 {
		end = len(rawQuery)
	} else {
		end = start + end
	}
	val := rawQuery[start:end]
	decoded, _ := url.QueryUnescape(val)
	return decoded
}

// progressStr formats a progress percentage without allocating via fmt.Sprintf.
func progressStr(progress float64) string {
	return strconv.FormatFloat(progress, 'f', 1, 64) + "%"
}

// sync.Pool for reusable []byte buffers (large chunk reads).
// Reduces GC pressure in UploadChunk handler.
var chunkBufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, 512*1024) // 512KB default chunk size
		return &b
	},
}

// UploadHandler handles chunked upload operations
type UploadHandler struct {
	db       *store.DB
	storage  storage.Storage                  // Router，用于读取（FileSize/HashFile 读取型）
	storages map[string]storage.Storage       // 写入型链路按 storageType 选具体后端
	redis    *store.RedisCache                // optional Redis cache for high concurrency
	initSem  chan struct{}                    // InitUpload 并发限流信号量（容量=最大并发数，nil=不限流）
}

// backendFor 返回指定 storageType 的具体写入后端（混合存储）；未知/未配置时回退 local。
func (h *UploadHandler) backendFor(t string) storage.Storage {
	if b, ok := h.storages[t]; ok && b != nil {
		return b
	}
	return h.storages["local"]
}

// NewUploadHandler creates a new upload handler (SQLite-only path)
func NewUploadHandler(db *store.DB, s storage.Storage, storages map[string]storage.Storage) *UploadHandler {
	return &UploadHandler{db: db, storage: s, storages: storages}
}

// NewUploadHandlerWithRedis creates a new upload handler with Redis caching enabled.
// initMaxConcurrency 控制 InitUpload 最大并发数（bench 实测最优区间 500-1000，默认 1000），
// 超过时返回 429 避免高并发下 P99 飙升。
func NewUploadHandlerWithRedis(db *store.DB, s storage.Storage, storages map[string]storage.Storage, rc *store.RedisCache) *UploadHandler {
	return &UploadHandler{
		db:       db,
		storage:  s,
		storages: storages,
		redis:    rc,
		initSem:  make(chan struct{}, 1000),
	}
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// InitUpload initializes a new upload session (高并发优化版)
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
		existing, _ := h.db.FindFileByName(req.Filename, ownerForCheck)
		if existing != nil {
			conflictFile = existing
		} else {
			// Redis 有但 SQLite 没有（孤儿数据），用最小记录
			conflictFile = &model.FileRecord{Filename: req.Filename}
		}
	} else {
		// Redis miss 或无 Redis：查 SQLite
		existing, _ := h.db.FindFileByName(req.Filename, ownerForCheck)
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
		existing, err := h.db.FindActiveSessionByHash(req.FileHash, req.Filename)
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
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// === Async SQLite write — don't block HTTP response ===
	h.db.AsyncCreateSession(session)

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

// UploadChunk receives a chunk of data (高并发优化版)
// POST /api/upload/chunk
func (h *UploadHandler) UploadChunk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.FormValue("session_id")
	chunkIndexStr := r.FormValue("chunk_index")

	if sessionID == "" || chunkIndexStr == "" {
		http.Error(w, "session_id and chunk_index are required", http.StatusBadRequest)
		return
	}

	chunkIndex, err := strconv.Atoi(chunkIndexStr)
	if err != nil || chunkIndex < 0 {
		http.Error(w, "invalid chunk_index", http.StatusBadRequest)
		return
	}

	// === Redis fast path: Lua atomic verify + mark (1 Redis call) ===
	if h.redis != nil {
		var session *model.UploadSession
		// Lua script: GET session + GETBIT check + SETBIT in ONE atomic call
		result, err := h.redis.VerifyAndMarkChunk(r.Context(), sessionID, chunkIndex)
		if err == nil && result == -2 {
			// Chunk already received — return 200 immediately
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(model.UploadChunkResponse{
				SessionID:  sessionID,
				ChunkIndex: chunkIndex,
				Received:   true,
			})
			return
		}
		if err != nil || result == -1 {
			// Session not found — fallback to SQLite
			s2, dbErr := h.db.GetUploadSession(sessionID)
			if dbErr != nil {
				http.Error(w, "session not found", http.StatusNotFound)
				return
			}
			session = s2
			if session.Status != "active" {
				http.Error(w, "session is not active", http.StatusConflict)
				return
			}
			// Re-cache + mark chunk
			h.redis.CacheSession(r.Context(), session)
			h.redis.MarkChunkReceived(r.Context(), sessionID, chunkIndex)
		}

		// 确保拿到 session 以确定存储后端（Redis 命中路径从缓存/DB 兜底）
		if session == nil {
			if s2, dbErr := h.db.GetUploadSession(sessionID); dbErr == nil {
				session = s2
			}
		}
		if session == nil {
			session = &model.UploadSession{StorageType: "local"}
		}

		// Read chunk data from multipart form into memory
		file, _, err := r.FormFile("chunk_data")
		if err != nil {
			http.Error(w, fmt.Sprintf("chunk_data field required: %v", err), http.StatusBadRequest)
			return
		}
		chunkData, err := io.ReadAll(file)
		file.Close()
		if err != nil {
			http.Error(w, "failed to read chunk data", http.StatusInternalServerError)
			return
		}

		// === Async disk write — don't block HTTP response ===
		backend := h.backendFor(session.StorageType)
		if asyncSt, ok := backend.(storage.AsyncStorager); ok {
			asyncSt.SaveChunkAsync(sessionID, chunkIndex, chunkData)
		} else {
			backend.SaveChunk(sessionID, chunkIndex, strings.NewReader(string(chunkData)))
		}

		// === Async SQLite chunk record ===
		h.db.AsyncSaveChunk(sessionID, chunkIndex, int64(len(chunkData)), "")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(model.UploadChunkResponse{
			SessionID:  sessionID,
			ChunkIndex: chunkIndex,
			Received:   true,
		})
		return
	}

	// === SQLite path (no Redis) ===
	session, err := h.db.GetUploadSession(sessionID)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if session.Status != "active" {
		http.Error(w, "session is not active", http.StatusConflict)
		return
	}

	file, _, err := r.FormFile("chunk_data")
	if err != nil {
		http.Error(w, fmt.Sprintf("chunk_data field required: %v", err), http.StatusBadRequest)
		return
	}
	defer file.Close()

	size, err := h.backendFor(session.StorageType).SaveChunk(sessionID, chunkIndex, file)
	if err != nil {
		log.Printf("save chunk error: %v", err)
		http.Error(w, "failed to save chunk", http.StatusInternalServerError)
		return
	}

	h.db.SaveChunk(sessionID, chunkIndex, size, "")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(model.UploadChunkResponse{
		SessionID:  sessionID,
		ChunkIndex: chunkIndex,
		Received:   true,
	})
}

// GetUploadStatus returns the current progress of an upload (BITFIELD 优化版)
// GET /api/upload/status?session_id=xxx
func (h *UploadHandler) GetUploadStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := fastQueryParam(r.URL.RawQuery, "session_id")
	if sessionID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}

	// === Redis fast path with BITFIELD (one command, no pipeline overhead) ===
	if h.redis != nil {
		// 注：读写分离在此场景退化为 fallback 模式（主从复制延迟导致副本读 miss），
		// 反而增加 RTT，因此 GetUploadStatus 仍走 master。
		session, err := h.redis.GetSession(r.Context(), sessionID)
		if err != nil {
			session, err = h.db.GetUploadSession(sessionID)
			if err != nil {
				http.Error(w, "session not found", http.StatusNotFound)
				return
			}
		}

		// BITFIELD: N GETBIT in a single command (CPU-efficient)
		received, err := h.redis.GetReceivedChunksBitfield(r.Context(), sessionID, session.TotalChunks)
		if err != nil {
			// Fallback to Pipeline if BITFIELD fails (older Redis versions)
			received, err = h.redis.GetReceivedChunksPipeline(r.Context(), sessionID, session.TotalChunks)
			if err != nil {
				http.Error(w, "failed to get chunk status", http.StatusInternalServerError)
				return
			}
		}

		// Build missing chunks list
		receivedSet := make(map[int]bool, len(received))
		for _, idx := range received {
			receivedSet[idx] = true
		}

		var missing []int
		for i := 0; i < session.TotalChunks; i++ {
			if !receivedSet[i] {
				missing = append(missing, i)
			}
		}

		progress := 0.0
		if session.TotalChunks > 0 {
			progress = float64(len(received)) / float64(session.TotalChunks) * 100
		}

		resp := model.UploadStatusResponse{
			SessionID:      sessionID,
			Filename:       session.Filename,
			FileSize:       session.FileSize,
			ChunkSize:      session.ChunkSize,
			TotalChunks:    session.TotalChunks,
			ReceivedChunks: received,
			MissingChunks:  missing,
			Progress:       progressStr(progress),
			Status:         session.Status,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// === SQLite path ===
	session, err := h.db.GetUploadSession(sessionID)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	received := session.ReceivedChunks
	receivedSet := make(map[int]bool)
	for _, idx := range received {
		receivedSet[idx] = true
	}

	var missing []int
	for i := 0; i < session.TotalChunks; i++ {
		if !receivedSet[i] {
			missing = append(missing, i)
		}
	}

	progress := 0.0
	if session.TotalChunks > 0 {
		progress = float64(len(received)) / float64(session.TotalChunks) * 100
	}

	resp := model.UploadStatusResponse{
		SessionID:      sessionID,
		Filename:       session.Filename,
		FileSize:       session.FileSize,
		ChunkSize:      session.ChunkSize,
		TotalChunks:    session.TotalChunks,
		ReceivedChunks: received,
		MissingChunks:  missing,
		Progress:       progressStr(progress),
		Status:         session.Status,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

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
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}

			h.db.AsyncCreateFileAndStatus(fileRecord, req.SessionID)

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
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		// Async SQLite write (atomic: file + status in same transaction)
		h.db.AsyncCreateFileAndStatus(fileRecord, req.SessionID)

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
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		h.db.CreateFile(fileRecord)
		h.db.UpdateUploadSessionStatus(req.SessionID, "completed")

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
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	h.db.CreateFile(fileRecord)
	h.db.UpdateUploadSessionStatus(req.SessionID, "completed")
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

// ServeHTTP routes upload-related requests
func (h *UploadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/upload")
	path = strings.TrimSuffix(path, "/")

	switch path {
	case "/init":
		h.InitUpload(w, r)
	case "/chunk":
		h.UploadChunk(w, r)
	case "/status":
		h.GetUploadStatus(w, r)
	case "/complete":
		h.CompleteUpload(w, r)
	case "/check":
		h.CheckUpload(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

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

	// 命中：检查文件名冲突（同 owner 已有同名活跃文件则不秒传，让前端走冲突处理流程）
	existing, _ := h.db.FindFileByName(req.Filename, username)
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
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := h.db.CreateFile(newRecord); err != nil {
		log.Printf("[Upload] instant upload create file record error: %v", err)
		http.Error(w, `{"error":"db_create_failed"}`, http.StatusInternalServerError)
		return
	}

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

// validateFilePath 校验文件名路径（路径枚举方案）。
// 允许 / 作为虚拟目录分隔符，但禁止危险路径，防止目录穿越和存储异常。
// 规则：非空、不以 / 开头、无连续 //、无 .. 段、无反斜杠、长度 1-1024。
func validateFilePath(filename string) error {
	if filename == "" {
		return fmt.Errorf("filename is empty")
	}
	if len(filename) > 1024 {
		return fmt.Errorf("filename too long (max 1024 bytes)")
	}
	if strings.HasPrefix(filename, "/") {
		return fmt.Errorf("filename must not start with '/'")
	}
	if strings.Contains(filename, "\\") {
		return fmt.Errorf("filename must not contain backslash")
	}
	if strings.Contains(filename, "//") {
		return fmt.Errorf("filename must not contain consecutive '/'")
	}
	segments := strings.Split(filename, "/")
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid path segment: '%s'", seg)
		}
	}
	return nil
}
