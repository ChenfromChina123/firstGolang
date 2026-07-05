package model

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"time"
)

// UploadSession represents a chunked upload in progress
type UploadSession struct {
	ID             string    `json:"id"`
	Filename       string    `json:"filename"`
	FileSize       int64     `json:"file_size"`
	FileHash       string    `json:"file_hash,omitempty"`
	ChunkSize      int64     `json:"chunk_size"`
	TotalChunks    int       `json:"total_chunks"`
	ReceivedChunks []int     `json:"received_chunks"`
	Status         string    `json:"status"` // active, completed, cancelled
	StorageType    string    `json:"storage_type"` // local, s3
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// FileRecord represents a completed file
type FileRecord struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	Size        int64     `json:"size"`
	Hash        string    `json:"hash"`
	StoragePath string    `json:"storage_path"`
	StorageType string    `json:"storage_type"`
	ChunkSize   int64     `json:"chunk_size"`
	TotalChunks int       `json:"total_chunks"`
	Status      string    `json:"status"` // completed, failed
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ChunkRecord represents a single uploaded chunk
type ChunkRecord struct {
	ID         int64     `json:"id"`
	SessionID  string    `json:"session_id"`
	ChunkIndex int       `json:"chunk_index"`
	Size       int64     `json:"size"`
	Hash       string    `json:"hash"`
	CreatedAt  time.Time `json:"created_at"`
}

// InitUploadRequest is the request body to initialize an upload
type InitUploadRequest struct {
	Filename   string `json:"filename"`
	FileSize   int64  `json:"file_size"`
	ChunkSize  int64  `json:"chunk_size"`
	FileHash   string `json:"file_hash,omitempty"`
	Storage    string `json:"storage"` // local, s3
}

// InitUploadResponse is the response for upload initialization
type InitUploadResponse struct {
	SessionID    string `json:"session_id"`
	Filename     string `json:"filename"`
	ChunkSize    int64  `json:"chunk_size"`
	TotalChunks  int    `json:"total_chunks"`
	StorageType  string `json:"storage_type"`
}

// UploadChunkRequest carries chunk upload data (multipart)
type UploadChunkResponse struct {
	SessionID  string `json:"session_id"`
	ChunkIndex int    `json:"chunk_index"`
	Received   bool   `json:"received"`
}

// UploadStatusResponse shows progress of an upload session
type UploadStatusResponse struct {
	SessionID      string `json:"session_id"`
	Filename       string `json:"filename"`
	FileSize       int64  `json:"file_size"`
	ChunkSize      int64  `json:"chunk_size"`
	TotalChunks    int    `json:"total_chunks"`
	ReceivedChunks []int  `json:"received_chunks"`
	MissingChunks  []int  `json:"missing_chunks"`
	Progress       string `json:"progress"` // e.g. "45.2%"
	Status         string `json:"status"`
}

// CompleteUploadResponse is returned after assembly
type CompleteUploadResponse struct {
	FileID      string `json:"file_id"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	Hash        string `json:"hash"`
	StoragePath string `json:"storage_path"`
}

// FileInfoResponse is returned when querying a file
type FileInfoResponse struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	Hash        string `json:"hash"`
	StoragePath string `json:"storage_path"`
	StorageType string `json:"storage_type"`
	CreatedAt   string `json:"created_at"`
}

// ConflictInfo describes a file conflict
type ConflictInfo struct {
	HasConflict   bool              `json:"has_conflict"`
	ExistingFile  *FileInfoResponse `json:"existing_file,omitempty"`
	Strategy      string            `json:"strategy,omitempty"` // skip, overwrite, rename
}

// DownloadRequest initiates a download
type DownloadRequest struct {
	FileID string `json:"file_id"`
}

// ComputeHash computes SHA256 of a reader
func ComputeHash(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
