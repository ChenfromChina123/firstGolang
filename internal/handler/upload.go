package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"filesync/internal/model"
	"filesync/internal/store"
	"filesync/internal/storage"
)

// UploadHandler handles chunked upload operations
type UploadHandler struct {
	db      *store.DB
	storage storage.Storage
}

// NewUploadHandler creates a new upload handler
func NewUploadHandler(db *store.DB, s storage.Storage) *UploadHandler {
	return &UploadHandler{db: db, storage: s}
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// InitUpload initializes a new upload session
// POST /api/upload/init
func (h *UploadHandler) InitUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
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

	if req.ChunkSize <= 0 {
		req.ChunkSize = 512 * 1024 // default 512KB
	}
	if req.Storage == "" {
		req.Storage = "local"
	}

	totalChunks := int((req.FileSize + req.ChunkSize - 1) / req.ChunkSize)

	// Check for conflict
	existing, _ := h.db.FindFileByName(req.Filename)
	if existing != nil {
		// file exists - respond with conflict info
		resp := map[string]interface{}{
			"conflict": true,
			"message":  fmt.Sprintf("file '%s' already exists (size=%d, hash=%s)", existing.Filename, existing.Size, existing.Hash),
			"existing": model.FileInfoResponse{
				ID:          existing.ID,
				Filename:    existing.Filename,
				Size:        existing.Size,
				Hash:        existing.Hash,
				StoragePath: existing.StoragePath,
				StorageType: existing.StorageType,
				CreatedAt:   existing.CreatedAt.Format(time.RFC3339),
			},
			"strategies": []string{"skip", "overwrite", "rename"},
		}
		// Allow continuing with ?force=true to overwrite
		if r.URL.Query().Get("force") != "true" && r.URL.Query().Get("rename") != "true" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
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

	if err := h.db.CreateUploadSession(session); err != nil {
		log.Printf("create session error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
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

// UploadChunk receives a chunk of data
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

	// Verify session exists and is active
	session, err := h.db.GetUploadSession(sessionID)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if session.Status != "active" {
		http.Error(w, "session is not active", http.StatusConflict)
		return
	}

	// Read chunk data from multipart form
	file, _, err := r.FormFile("chunk_data")
	if err != nil {
		http.Error(w, fmt.Sprintf("chunk_data field required: %v", err), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Save chunk to storage
	size, err := h.storage.SaveChunk(sessionID, chunkIndex, file)
	if err != nil {
		log.Printf("save chunk error: %v", err)
		http.Error(w, "failed to save chunk", http.StatusInternalServerError)
		return
	}

	// Record in database
	if err := h.db.SaveChunk(sessionID, chunkIndex, size, ""); err != nil {
		log.Printf("db save chunk error: %v", err)
	}

	resp := model.UploadChunkResponse{
		SessionID:  sessionID,
		ChunkIndex: chunkIndex,
		Received:   true,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GetUploadStatus returns the current progress of an upload
// GET /api/upload/status?session_id=xxx
func (h *UploadHandler) GetUploadStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}

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
		Progress:       fmt.Sprintf("%.1f%%", progress),
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

	// Assemble file
	filePath, err := h.storage.AssembleFile(req.SessionID, session.Filename, session.TotalChunks)
	if err != nil {
		log.Printf("assemble error: %v", err)
		http.Error(w, "failed to assemble file", http.StatusInternalServerError)
		return
	}

	// Compute hash
	hash, _ := storage.ComputeFileHash(filePath)

	// Store file record
	fileID := generateID()
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

	if err := h.db.CreateFile(fileRecord); err != nil {
		log.Printf("create file record error: %v", err)
	}

	// Update session status
	h.db.UpdateUploadSessionStatus(req.SessionID, "completed")

	// Cleanup temp chunks
	h.storage.DeleteTemp(req.SessionID)

	resp := model.CompleteUploadResponse{
		FileID:      fileID,
		Filename:    session.Filename,
		Size:        fileSize,
		Hash:        hash,
		StoragePath: filePath,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
