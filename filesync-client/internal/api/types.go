// Package api 封装 filesync 服务器 API 调用。
// 复用 auth.AuthManager 的 http.Client（带 cookiejar），自动携带认证 Cookie。
package api

// FileRecord 文件记录（与服务器 model.FileRecord 对齐，仅保留客户端需要的字段）
type FileRecord struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	Hash        string `json:"hash"`
	StorageType string `json:"storage_type"`
	Status      string `json:"status"`
	Owner       string `json:"owner"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// InitUploadRequest 初始化上传会话请求体
type InitUploadRequest struct {
	Filename  string `json:"filename"`
	FileSize  int64  `json:"file_size"`
	ChunkSize int64  `json:"chunk_size"`
	FileHash  string `json:"file_hash,omitempty"`
	Storage   string `json:"storage"`
}

// InitUploadResponse 初始化上传会话响应
type InitUploadResponse struct {
	SessionID   string `json:"session_id"`
	Filename    string `json:"filename"`
	ChunkSize   int64  `json:"chunk_size"`
	TotalChunks int    `json:"total_chunks"`
	StorageType string `json:"storage_type"`
}

// CheckUploadRequest 秒传检查请求体
type CheckUploadRequest struct {
	Filename string `json:"filename"`
	FileSize int64  `json:"file_size"`
	FileHash string `json:"file_hash"`
}

// CheckUploadResponse 秒传检查响应
type CheckUploadResponse struct {
	InstantUpload bool   `json:"instant_upload"`
	FileID        string `json:"file_id,omitempty"`
	Filename      string `json:"filename,omitempty"`
	Size          int64  `json:"size,omitempty"`
	Hash          string `json:"hash,omitempty"`
}

// CompleteUploadResponse 完成上传响应
type CompleteUploadResponse struct {
	FileID      string `json:"file_id"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	Hash        string `json:"hash"`
	StoragePath string `json:"storage_path"`
}

// GetUploadStatusResponse 上传进度查询响应（对应服务器 UploadStatusResponse）
type GetUploadStatusResponse struct {
	SessionID      string `json:"session_id"`
	Filename       string `json:"filename"`
	FileSize       int64  `json:"file_size"`
	ChunkSize      int64  `json:"chunk_size"`
	TotalChunks    int    `json:"total_chunks"`
	ReceivedChunks []int  `json:"received_chunks"`
	MissingChunks  []int  `json:"missing_chunks"`
	Progress       string `json:"progress"`
	Status         string `json:"status"`
}

// UploadChunkResponse 分片上传响应
type UploadChunkResponse struct {
	SessionID  string `json:"session_id"`
	ChunkIndex int    `json:"chunk_index"`
	Received   bool   `json:"received"`
}

// ConflictFile 冲突时服务器返回的已存在文件信息
type ConflictFile struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	Hash        string `json:"hash"`
	StoragePath string `json:"storage_path"`
	StorageType string `json:"storage_type"`
	CreatedAt   string `json:"created_at"`
}

// ConflictResponse InitUpload 冲突响应（409）
type ConflictResponse struct {
	Conflict   bool          `json:"conflict"`
	Message    string        `json:"message"`
	Strategies []string      `json:"strategies"`
	Existing   *ConflictFile `json:"existing,omitempty"`
}

// ConflictError 包装 409 冲突响应，供 uploader 判断处理策略。
// Type 为 "filename_conflict"（CheckUpload 409）或 "session_conflict"（InitUpload 409）。
type ConflictError struct {
	Type    string
	Message string
	Resp    *ConflictResponse // InitUpload 409 时填充
}

// Error 实现 error 接口
func (e *ConflictError) Error() string { return e.Message }
