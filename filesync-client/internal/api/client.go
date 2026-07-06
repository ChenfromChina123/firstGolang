package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"filesync-client/internal/auth"
)

// ErrNotImplemented 表示该方法尚未实现（Phase 2.3-2.4 填充）
var ErrNotImplemented = errors.New("该方法尚未实现")

// Client 封装 filesync 服务器 API 调用。
// 复用 auth.AuthManager 的 http.Client（带 cookiejar），自动携带认证 Cookie。
type Client struct {
	auth *auth.AuthManager
}

// New 创建 API Client 实例。
func New(a *auth.AuthManager) *Client {
	return &Client{auth: a}
}

// doGET 发送 GET 请求并解析 JSON 响应。
func (c *Client) doGET(path string, result interface{}) error {
	resp, err := c.auth.HTTPClient().Get(c.auth.ServerURL() + path)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("服务器返回 HTTP %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(result)
}

// ListFiles 列出服务器上的文件。
// GET /api/files?prefix=xxx，prefix 为空时列出所有文件。
// Phase 2.2 已实现，用于验证认证是否生效。
func (c *Client) ListFiles(prefix string) ([]FileRecord, error) {
	path := "/api/files"
	if prefix != "" {
		path += "?prefix=" + url.QueryEscape(prefix)
	}

	var files []FileRecord
	if err := c.doGET(path, &files); err != nil {
		return nil, err
	}
	return files, nil
}

// GetFile 获取文件详情。
// GET /api/files/{id}
// Phase 2.3-2.4 实现。
func (c *Client) GetFile(id string) (*FileRecord, error) {
	return nil, ErrNotImplemented
}

// DeleteFile 删除文件（软删除，移入回收站）。
// DELETE /api/files/{id}
// Phase 2.3-2.4 实现。
func (c *Client) DeleteFile(id string) error {
	return ErrNotImplemented
}

// InitUpload 初始化上传会话。
// POST /api/upload/init
// Phase 2.3 实现。
func (c *Client) InitUpload(req InitUploadRequest) (*InitUploadResponse, error) {
	return nil, ErrNotImplemented
}

// UploadChunk 上传分片。
// POST /api/upload/chunk（multipart）
// Phase 2.3 实现。
func (c *Client) UploadChunk(sessionID string, chunkIndex int, data []byte) error {
	return ErrNotImplemented
}

// CompleteUpload 完成上传。
// POST /api/upload/complete
// Phase 2.3 实现。
func (c *Client) CompleteUpload(sessionID string) (*CompleteUploadResponse, error) {
	return nil, ErrNotImplemented
}

// CheckUpload 秒传检查。
// POST /api/upload/check
// Phase 2.3 实现。
func (c *Client) CheckUpload(req CheckUploadRequest) (*CheckUploadResponse, error) {
	return nil, ErrNotImplemented
}

// DownloadFile 下载文件（支持 Range 断点续传）。
// GET /api/download/{id}
// Phase 2.4 实现。
func (c *Client) DownloadFile(fileID string, offset int64) (io.ReadCloser, error) {
	return nil, ErrNotImplemented
}

// Mkdir 创建目录。
// POST /api/files/mkdir?path=xxx
// Phase 2.3 实现。
func (c *Client) Mkdir(path string) error {
	return ErrNotImplemented
}

// Rename 重命名/移动文件。
// POST /api/files/rename
// Phase 2.3 实现。
func (c *Client) Rename(fileID, newFilename string) error {
	return ErrNotImplemented
}
