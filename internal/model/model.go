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
	UploadMode     string    `json:"upload_mode,omitempty"` // "presigned" | "relay"（空=relay，兼容旧客户端）
	ObjectKey      string    `json:"object_key,omitempty"` // presigned 模式下的最终对象键（合并后的目标）
	SpaceID        string    `json:"space_id,omitempty"`   // 目标空间 ID（空=默认空间）
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// FileRecord represents a completed file
type FileRecord struct {
	ID          string     `json:"id"`
	Filename    string     `json:"filename"`
	Size        int64      `json:"size"`
	Hash        string     `json:"hash"`
	StoragePath string     `json:"storage_path"`
	StorageType string     `json:"storage_type"`
	ChunkSize   int64      `json:"chunk_size"`
	TotalChunks int        `json:"total_chunks"`
	Status      string     `json:"status"` // completed, failed
	Owner       string     `json:"owner"`  // 文件归属用户名（空=历史数据/公共）
	SpaceID     string     `json:"space_id"` // 所属空间 ID（空=默认空间）
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"` // NULL=正常，非NULL=已移入回收站（删除时间）
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
	Storage    string `json:"storage"`         // local, s3
	SpaceID    string `json:"space_id,omitempty"` // 目标空间 ID（空=默认空间）
}

// Space 表示用户的存储空间（相当于一个独立磁盘 / 文件树根）。
// 每个用户可创建多个空间，文件通过 files.space_id 关联所属空间。
// 空间间文件树相互隔离：切换空间即切换文件列表根。
type Space struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Owner       string    `json:"owner"`         // 归属用户名（空=历史/公共，admin 可操作）
	StorageType string    `json:"storage_type"`  // local | s3（空间默认存储后端）
	FileCount   int64     `json:"file_count"`    // 空间内文件数（含 .keep 占位，动态统计）
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PresignedPartInfo 描述一个分片的 presigned 上传信息（断点续传时客户端按此切片）。
type PresignedPartInfo struct {
	PartNumber int    `json:"part_number"` // 分片序号（0-based）
	URL       string `json:"url"`         // presigned PUT URL
	Offset    int64  `json:"offset"`      // 文件偏移量（字节）
	Size      int64  `json:"size"`        // 本片大小（字节，最后一片可能 < partSize）
}

// PresignedUploadInfo 是 InitUpload 返回的 presigned 上传信息。
// 客户端据此决定上传模式：
//   - Mode="single": 用 UploadURL 直接 PUT 单个对象（小文件 < 5MB）
//   - Mode="multipart": 按 Parts 切片并行上传，全部完成后调 /api/upload/complete 触发合并
// CompletedParts 用于断点续传，客户端应跳过已完成的分片。
type PresignedUploadInfo struct {
	Mode           string              `json:"mode"`            // "single" | "multipart"
	ObjectKey      string              `json:"object_key"`      // 最终对象键（合并后的目标）
	UploadURL      string              `json:"upload_url,omitempty"`         // Mode=single 时使用
	Parts          []PresignedPartInfo `json:"parts,omitempty"`               // Mode=multipart 时使用
	CompletedParts []int               `json:"completed_parts,omitempty"`     // 断点续传：已上传的分片编号
	PartSize       int64               `json:"part_size,omitempty"`           // 每片大小（字节）
	TotalParts     int                 `json:"total_parts,omitempty"`        // 总片数
}

// InitUploadResponse is the response for upload initialization
type InitUploadResponse struct {
	SessionID    string                 `json:"session_id"`
	Filename     string                 `json:"filename"`
	ChunkSize    int64                  `json:"chunk_size"`
	TotalChunks  int                    `json:"total_chunks"`
	StorageType  string                 `json:"storage_type"`
	Presigned    *PresignedUploadInfo   `json:"presigned,omitempty"` // 非 nil 时客户端应走 presigned 直连
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
	Owner       string `json:"owner"`     // 文件归属用户名（admin 视图用）
	Status      string `json:"status"`    // completed/failed
	CreatedAt   string `json:"created_at"`
}

// DirEntry 子目录项（分层列表用）
type DirEntry struct {
	Name  string `json:"name"`  // 目录名（不含路径前缀，如 "docs"）
	Count int    `json:"count"` // 该目录下所有文件数（递归，排除 .keep 占位文件）
}

// DirListingResponse 分层目录列表响应（shallow 模式）
type DirListingResponse struct {
	Dirs  []DirEntry         `json:"dirs"`
	Files []FileInfoResponse `json:"files"`
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
	SpaceID       string     `json:"space_id,omitempty"`       // 分享所在空间 ID（空=默认空间，旧分享兼容）
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`     // nil=永久
	DownloadCount int        `json:"download_count"`
	MaxDownloads  *int       `json:"max_downloads,omitempty"`  // nil=无限
	IsActive      bool       `json:"is_active"`
	PasswordHash  string     `json:"-"`                        // bcrypt 哈希，空=无密码，不序列化到 JSON
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
	DownloadToken string     `json:"download_token"`           // 下载签名 token（防盗链，30 分钟有效）
	HasPassword   bool       `json:"has_password"`             // 是否设置了密码保护
}

// UserSettings 表示用户的跨浏览器配置（分片大小、并发数）
type UserSettings struct {
	Username    string    `json:"username"`
	ChunkSize   int64     `json:"chunk_size"`
	Concurrency int       `json:"concurrency"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CheckUploadRequest 秒传检查请求（上传前检查哈希是否已存在）。
// 客户端计算完整文件 SHA256 后调用 /api/upload/check，命中则跳过整个上传流程。
type CheckUploadRequest struct {
	Filename string `json:"filename"` // 目标文件名（含路径前缀，如 "docs/report.pdf"）
	FileSize int64  `json:"file_size"` // 文件大小（字节）
	FileHash string `json:"file_hash"` // 完整 SHA256 hex（64 字符）
	SpaceID  string `json:"space_id,omitempty"` // 目标空间 ID（空=默认空间）
}

// CheckUploadResponse 秒传检查响应。
// InstantUpload=true 表示秒传成功，文件已创建；false 表示未命中，需正常上传。
type CheckUploadResponse struct {
	InstantUpload bool   `json:"instant_upload"`           // true=秒传成功，false=需正常上传
	FileID        string `json:"file_id,omitempty"`        // 秒传成功时的新文件 ID
	Filename      string `json:"filename,omitempty"`       // 实际文件名
	Size          int64  `json:"size,omitempty"`           // 文件大小
	Hash          string `json:"hash,omitempty"`           // 文件 SHA256
}

// APIToken 表示个人访问令牌（PAT，Personal Access Token）。
// 用于 MCP/外部 Agent 认证，取代「账号密码 + RSA 加密登录 + 每 15 分钟刷新」流程。
// 明文只在创建时返回一次（fsk_ 前缀），库内仅存 SHA-256 哈希。
// Scopes 为空格分隔的 scope 列表（如 "filesync:read filesync:write"）。
// SpaceID / PathPrefix 提供令牌级沙箱边界；QuotaBytes 提供令牌级写入配额。
type APIToken struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Username   string     `json:"username,omitempty"` // 冗余存储，便于审计
	Name       string     `json:"name"`               // 用户可读名称（如 "claude-desktop"）
	TokenHash  string     `json:"-"`                  // SHA-256 hex，不序列化
	Scopes     string     `json:"scopes"`             // 空格分隔
	SpaceID    string     `json:"space_id,omitempty"` // 限定单空间；空=不限定
	PathPrefix string     `json:"path_prefix,omitempty"` // 限定目录前缀；空=不限定
	QuotaBytes int64      `json:"quota_bytes"`        // 单 token 写入配额；0=不限制
	QuotaUsed  int64      `json:"quota_used"`         // 已用写入字节
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"` // nil=不过期
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"` // 非 nil=已吊销
}

// TrashItem 表示回收站中的文件项（已软删除，可恢复或永久删除）。
type TrashItem struct {
	ID          string `json:"id"`           // 文件 ID
	Filename    string `json:"filename"`     // 文件名（含路径前缀）
	Size        int64  `json:"size"`         // 文件大小（字节）
	Hash        string `json:"hash"`         // SHA256 校验值
	Owner       string `json:"owner"`        // 归属用户名
	CreatedAt   string `json:"created_at"`   // 文件创建时间（ISO 8601）
	DeletedAt   string `json:"deleted_at"`   // 移入回收站时间（ISO 8601）
	ExpiresAt   string `json:"expires_at"`   // 过期时间（超过此时间将被自动清理，ISO 8601）
	IsExpired   bool   `json:"is_expired"`   // 是否已过期（当前时间 > expires_at）
}

// UploadEvent 表示一次上传事件记录（MCP / 外部上传，供传输中心历史回溯）。
type UploadEvent struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	SpaceID   string `json:"space_id,omitempty"`
	Tool      string `json:"tool,omitempty"` // fs_write / fs_upload / ...
	CreatedAt string `json:"created_at"`     // ISO 8601
}
