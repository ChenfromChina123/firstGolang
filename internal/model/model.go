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

// User 表示系统用户（登录认证用）
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email,omitempty"` // 可空（admin 历史 account 无邮箱）
	PasswordHash string    `json:"-"`               // bcrypt 哈希，不序列化到 JSON
	Role         string    `json:"role"`            // admin, user
	Status       string    `json:"status"`          // active, pending（注册未激活）
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ActivationToken 表示账号激活令牌（注册后通过邮件发送）
type ActivationToken struct {
	ID        int64     `json:"-"`
	UserID    string    `json:"user_id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// PasswordResetCode 表示密码重置验证码（忘记密码时通过邮件发送）
type PasswordResetCode struct {
	ID        int64     `json:"-"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Code      string    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
	CreatedAt time.Time `json:"created_at"`
}

// Share 表示一个分享链接（文件或目录）
type Share struct {
	ID            string     `json:"id"`                       // 8 字符短 ID
	FileID        string     `json:"file_id,omitempty"`        // 单文件分享：关联 files.id
	DirPrefix     string     `json:"dir_prefix,omitempty"`     // 目录分享：目录前缀
	ShareType     string     `json:"share_type"`               // "file" | "dir"
	CreatedBy     string     `json:"created_by"`               // 创建者用户名
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`     // nil=永久
	DownloadCount int        `json:"download_count"`
	MaxDownloads  *int       `json:"max_downloads,omitempty"`  // nil=无限
	IsActive      bool       `json:"is_active"`
}

// SharePublicInfo 是公开返回给访客的分享信息（不暴露 file_id/storage_path 等敏感字段）
type SharePublicInfo struct {
	ID            string     `json:"id"`
	ShareType     string     `json:"share_type"`               // "file" | "dir"
	Name          string     `json:"name"`                     // 文件名或目录名
	Size          int64      `json:"size"`                     // 文件大小或目录总大小
	FileCount     int        `json:"file_count"`               // 目录文件数（file 类型为 1）
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	DownloadCount int        `json:"download_count"`
	IsExpired     bool       `json:"is_expired"`
}

// UserSettings 表示用户的跨浏览器配置（分片大小、并发数）
type UserSettings struct {
	Username    string    `json:"username"`
	ChunkSize   int64     `json:"chunk_size"`
	Concurrency int       `json:"concurrency"`
	UpdatedAt   time.Time `json:"updated_at"`
}
