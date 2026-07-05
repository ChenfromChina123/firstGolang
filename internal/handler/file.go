package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"filesync/internal/model"
	"filesync/internal/store"
)

// FileHandler handles file listing and info queries
type FileHandler struct {
	db *store.DB
}

// NewFileHandler creates a new file handler
func NewFileHandler(db *store.DB) *FileHandler {
	return &FileHandler{db: db}
}

// ListFiles returns all completed files
// GET /api/files
func (h *FileHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	files, err := h.db.ListFiles()
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

	resp := model.FileInfoResponse{
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
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	h.GetFileInfo(w, r)
}
