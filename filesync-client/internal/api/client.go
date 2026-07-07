package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"

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

// doPOST 发送 JSON POST 请求并解析响应。
// 状态码非 200 时返回包含响应体的错误。
func (c *Client) doPOST(path string, body interface{}, result interface{}) error {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("序列化请求体失败: %w", err)
	}
	resp, err := c.auth.HTTPClient().Post(
		c.auth.ServerURL()+path,
		"application/json",
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("服务器返回 HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	if result == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(result)
}

// doPOSTRaw 发送 JSON POST 请求，返回状态码和响应体（不要求 200）。
// 用于需要区分 409 冲突等非 200 状态码的场景。
func (c *Client) doPOSTRaw(path string, body interface{}) (int, []byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return 0, nil, fmt.Errorf("序列化请求体失败: %w", err)
	}
	resp, err := c.auth.HTTPClient().Post(
		c.auth.ServerURL()+path,
		"application/json",
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		return 0, nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, nil
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
// POST /api/upload/init，可选 ?force=true（覆盖）/ ?rename=true（重命名）。
// 冲突时返回 *ConflictError（含 Existing 信息）。断点续传时服务器可能返回已有 session。
func (c *Client) InitUpload(req InitUploadRequest, force, rename bool) (*InitUploadResponse, error) {
	path := "/api/upload/init"
	params := []string{}
	if force {
		params = append(params, "force=true")
	}
	if rename {
		params = append(params, "rename=true")
	}
	if len(params) > 0 {
		path += "?" + params[0]
		for i := 1; i < len(params); i++ {
			path += "&" + params[i]
		}
	}

	statusCode, respBody, err := c.doPOSTRaw(path, req)
	if err != nil {
		return nil, err
	}

	if statusCode == http.StatusOK {
		var resp InitUploadResponse
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return nil, fmt.Errorf("解析响应失败: %w", err)
		}
		return &resp, nil
	}

	if statusCode == http.StatusConflict {
		var cr ConflictResponse
		if err := json.Unmarshal(respBody, &cr); err == nil && cr.Conflict {
			return nil, &ConflictError{
				Type:    "session_conflict",
				Message: cr.Message,
				Resp:    &cr,
			}
		}
		return nil, &ConflictError{
			Type:    "session_conflict",
			Message: fmt.Sprintf("文件名冲突 (HTTP 409): %s", string(respBody)),
		}
	}

	return nil, fmt.Errorf("初始化上传失败 (HTTP %d): %s", statusCode, string(respBody))
}

// UploadChunk 上传单个分片（multipart/form-data）。
// POST /api/upload/chunk，字段：session_id、chunk_index、chunk_data（文件）。
func (c *Client) UploadChunk(sessionID string, chunkIndex int, data []byte) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	if err := writer.WriteField("session_id", sessionID); err != nil {
		return fmt.Errorf("写入 session_id 失败: %w", err)
	}
	if err := writer.WriteField("chunk_index", strconv.Itoa(chunkIndex)); err != nil {
		return fmt.Errorf("写入 chunk_index 失败: %w", err)
	}
	part, err := writer.CreateFormFile("chunk_data", "chunk.bin")
	if err != nil {
		return fmt.Errorf("创建 chunk_data 表单字段失败: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("写入分片数据失败: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("关闭 multipart writer 失败: %w", err)
	}

	req, err := http.NewRequest("POST", c.auth.ServerURL()+"/api/upload/chunk", body)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.auth.HTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("上传分片失败 (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var chunkResp UploadChunkResponse
	if err := json.NewDecoder(resp.Body).Decode(&chunkResp); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	if !chunkResp.Received {
		return fmt.Errorf("服务器未确认接收分片 %d", chunkIndex)
	}
	return nil
}

// GetUploadStatus 查询上传进度（用于断点续传，获取 received_chunks）。
// GET /api/upload/status?session_id=xxx
func (c *Client) GetUploadStatus(sessionID string) (*GetUploadStatusResponse, error) {
	path := "/api/upload/status?session_id=" + url.QueryEscape(sessionID)
	var resp GetUploadStatusResponse
	if err := c.doGET(path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CompleteUpload 完成上传，触发服务器分片合并。
// POST /api/upload/complete，body: {"session_id":"..."}
func (c *Client) CompleteUpload(sessionID string) (*CompleteUploadResponse, error) {
	body := map[string]string{"session_id": sessionID}
	var resp CompleteUploadResponse
	if err := c.doPOST("/api/upload/complete", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CheckUpload 秒传检查。
// POST /api/upload/check，命中返回 InstantUpload=true，未命中 false，文件名冲突返回 *ConflictError。
func (c *Client) CheckUpload(req CheckUploadRequest) (*CheckUploadResponse, error) {
	statusCode, respBody, err := c.doPOSTRaw("/api/upload/check", req)
	if err != nil {
		return nil, err
	}

	if statusCode == http.StatusOK {
		var resp CheckUploadResponse
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return nil, fmt.Errorf("解析响应失败: %w", err)
		}
		return &resp, nil
	}

	if statusCode == http.StatusConflict {
		return nil, &ConflictError{
			Type:    "filename_conflict",
			Message: "目标文件名已存在",
		}
	}

	return nil, fmt.Errorf("秒传检查失败 (HTTP %d): %s", statusCode, string(respBody))
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
