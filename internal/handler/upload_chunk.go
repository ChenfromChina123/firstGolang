package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"filesync/internal/model"
	"filesync/internal/storage"
)

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
