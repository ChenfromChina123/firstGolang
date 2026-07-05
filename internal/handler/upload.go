package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

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
	db      *store.DB
	storage storage.Storage
	redis   *store.RedisCache // optional Redis cache for high concurrency
	initSem chan struct{}     // InitUpload 并发限流信号量（容量=最大并发数，nil=不限流）
}

// NewUploadHandler creates a new upload handler (SQLite-only path)
func NewUploadHandler(db *store.DB, s storage.Storage) *UploadHandler {
	return &UploadHandler{db: db, storage: s}
}

// NewUploadHandlerWithRedis creates a new upload handler with Redis caching enabled.
// initMaxConcurrency 控制 InitUpload 最大并发数（bench 实测最优区间 500-1000，默认 1000），
// 超过时返回 429 避免高并发下 P99 飙升。
func NewUploadHandlerWithRedis(db *store.DB, s storage.Storage, rc *store.RedisCache) *UploadHandler {
	return &UploadHandler{
		db:      db,
		storage: s,
		redis:   rc,
		initSem: make(chan struct{}, 1000),
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
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if req.Filename == "" || req.FileSize <= 0 {
		http.Error(w, "filename and file_size are required", http.StatusBadRequest)
		return
	}

	// 文件名路径校验（路径枚举方案：允许 / 作为虚拟目录分隔符，但禁止危险路径）
	// 参考 S3/OSS 命名规范：不允许开头 /、连续 //、.. 路径段、反斜杠
	if err := validateFilePath(req.Filename); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.ChunkSize <= 0 {
		req.ChunkSize = 512 * 1024 // default 512KB
	}
	if req.Storage == "" {
		req.Storage = "local"
	}

	totalChunks := int((req.FileSize + req.ChunkSize - 1) / req.ChunkSize)

	// === Conflict check: Redis first, fallback to SQLite ===
	// Redis 集合可能不完整（Redis 重启、文件通过 SQLite path 上传、历史数据残留），
	// 所以 Redis miss 时必须 fallback 到 SQLite 检查，并补标记 Redis 修复集合不一致。
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
		existing, _ := h.db.FindFileByName(req.Filename)
		if existing != nil {
			conflictFile = existing
		} else {
			// Redis 有但 SQLite 没有（孤儿数据），用最小记录
			conflictFile = &model.FileRecord{Filename: req.Filename}
		}
	} else {
		// Redis miss 或无 Redis：查 SQLite
		existing, _ := h.db.FindFileByName(req.Filename)
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
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
	}

	sessionID := generateID()
	session := &model.UploadSession{
		ID:          sessionID,
		Filename:    req.Filename,
		FileSize:    req.FileSize,
		FileHash:    req.FileHash,
		ChunkSize:   req.ChunkSize,
		TotalChunks: totalChunks,
		Status:      "active",
		StorageType: req.Storage,
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
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
			session, dbErr := h.db.GetUploadSession(sessionID)
			if dbErr != nil {
				http.Error(w, "session not found", http.StatusNotFound)
				return
			}
			if session.Status != "active" {
				http.Error(w, "session is not active", http.StatusConflict)
				return
			}
			// Re-cache + mark chunk
			h.redis.CacheSession(r.Context(), session)
			h.redis.MarkChunkReceived(r.Context(), sessionID, chunkIndex)
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
		if asyncSt, ok := h.storage.(storage.AsyncStorager); ok {
			asyncSt.SaveChunkAsync(sessionID, chunkIndex, chunkData)
		} else {
			h.storage.SaveChunk(sessionID, chunkIndex, strings.NewReader(string(chunkData)))
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

	size, err := h.storage.SaveChunk(sessionID, chunkIndex, file)
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
		if asyncSt, ok := h.storage.(storage.AsyncStorager); ok {
			asyncSt.WaitAsync()
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
		fileID := generateID()
		if assembler, ok := h.storage.(storage.HashAssembler); ok {
			filePath, hash, err = assembler.AssembleFileWithHash(req.SessionID, fileID, session.Filename, session.TotalChunks)
		} else {
			filePath, err = h.storage.AssembleFile(req.SessionID, fileID, session.Filename, session.TotalChunks)
			if err == nil {
				hash, _ = h.storage.HashFile(filePath)
			}
		}
		if err != nil {
			log.Printf("assemble error: %v", err)
			http.Error(w, "failed to assemble file", http.StatusInternalServerError)
			return
		}

		fileSize, _ := h.storage.FileSize(filePath)

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
		h.storage.DeleteTemp(req.SessionID)

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

	if len(session.ReceivedChunks) != session.TotalChunks {
		http.Error(w, fmt.Sprintf("not all chunks received: %d/%d",
			len(session.ReceivedChunks), session.TotalChunks), http.StatusBadRequest)
		return
	}

	fileID := generateID()
	filePath, err := h.storage.AssembleFile(req.SessionID, fileID, session.Filename, session.TotalChunks)
	if err != nil {
		log.Printf("assemble error: %v", err)
		http.Error(w, "failed to assemble file", http.StatusInternalServerError)
		return
	}

	hash, _ := h.storage.HashFile(filePath)
	fileSize, _ := h.storage.FileSize(filePath)

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
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	h.db.CreateFile(fileRecord)
	h.db.UpdateUploadSessionStatus(req.SessionID, "completed")
	h.storage.DeleteTemp(req.SessionID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(model.CompleteUploadResponse{
		FileID:      fileID,
		Filename:    session.Filename,
		Size:        fileSize,
		Hash:        hash,
		StoragePath: filePath,
	})
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
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
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
