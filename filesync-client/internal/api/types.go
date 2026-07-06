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
