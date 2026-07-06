// Package main 端到端验证视频多画质预览链路（HTTP API 层）。
// 步骤：登录 → 分片上传视频 → complete → 调预览 meta → 调 transcode（medium/low）→ 验证返回字节。
//
// 运行方式：
//
//	$env:FFMPEG_PATH = "D:\下载\ffmpeg-master-latest-win64-gpl\ffmpeg-master-latest-win64-gpl\bin\ffmpeg.exe"
//	go run ./test_local/transcode_test/
//
// 注意：需要 server 已在 8090 端口运行且创建了 admin 账号（FILESYNC_INITIAL_USERNAME/PASSWORD）。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"os"
	"time"
)

const (
	baseURL  = "http://localhost:8090"
	srcVideo = "./test_local/transcode_test/sample_720p.mp4"
)

// loginResp 登录响应结构
type loginResp struct {
	Success  bool   `json:"success"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// previewMeta 预览元数据
type previewMeta struct {
	Type     string            `json:"type"`
	Filename string            `json:"filename"`
	Size     int64             `json:"size"`
	URLs     map[string]string `json:"urls"`
}

func main() {
	// 1. 创建 cookie jar + HTTP client（自动管理 cookie）
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:     jar,
		Timeout: 300 * time.Second, // 转码可能耗时，给足超时
	}

	// 2. 登录
	loginBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "Admin12345",
	})
	resp, err := client.Post(baseURL+"/api/login", "application/json", bytes.NewReader(loginBody))
	must("login", err, resp)
	var lr loginResp
	json.NewDecoder(resp.Body).Decode(&lr)
	resp.Body.Close()
	if !lr.Success {
		log.Fatalf("[E2E] FAIL: login not successful: %+v", lr)
	}
	log.Printf("[E2E] 1/6 login OK: user=%s role=%s", lr.Username, lr.Role)

	// 3. 读取源视频并分片上传
	videoBytes, err := os.ReadFile(srcVideo)
	if err != nil {
		log.Fatalf("[E2E] read source video: %v", err)
	}
	fileSize := int64(len(videoBytes))
	const chunkSize = 65536
	totalChunks := (fileSize + chunkSize - 1) / chunkSize

	// 初始化上传会话
	initBody, _ := json.Marshal(map[string]interface{}{
		"filename":  "sample_720p.mp4",
		"file_size": fileSize,
		"chunk_size": chunkSize,
	})
	resp, err = client.Post(baseURL+"/api/upload/init", "application/json", bytes.NewReader(initBody))
	must("upload init", err, resp)
	var initResp struct {
		SessionID  string `json:"session_id"`
		TotalChunks int    `json:"total_chunks"`
		ChunkSize   int64  `json:"chunk_size"`
	}
	json.NewDecoder(resp.Body).Decode(&initResp)
	resp.Body.Close()
	sessionID := initResp.SessionID
	log.Printf("[E2E] 2/6 upload init: session=%s total_chunks=%d", sessionID, initResp.TotalChunks)

	// 上传所有分片
	for i := int64(0); i < totalChunks; i++ {
		offset := i * chunkSize
		end := offset + chunkSize
		if end > fileSize {
			end = fileSize
		}
		chunkData := videoBytes[offset:end]
		if err := uploadChunkViaMultipart(client, baseURL+"/api/upload/chunk", sessionID, int(i), chunkData); err != nil {
			log.Fatalf("[E2E] upload chunk %d: %v", i, err)
		}
	}
	log.Printf("[E2E] 3/6 all %d chunks uploaded", totalChunks)

	// Complete 上传
	completeBody, _ := json.Marshal(map[string]string{
		"session_id": sessionID,
		"file_hash":  "",
	})
	resp, err = client.Post(baseURL+"/api/upload/complete", "application/json", bytes.NewReader(completeBody))
	must("upload complete", err, resp)
	var complete struct {
		FileID   string `json:"file_id"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}
	json.NewDecoder(resp.Body).Decode(&complete)
	resp.Body.Close()
	fileID := complete.FileID
	log.Printf("[E2E] 4/6 upload complete: file_id=%s size=%d", fileID, complete.Size)

	// 5. 调预览 meta API
	resp, err = client.Get(baseURL + "/api/preview/" + fileID)
	must("preview meta", err, resp)
	var meta previewMeta
	json.NewDecoder(resp.Body).Decode(&meta)
	resp.Body.Close()
	if meta.Type != "video" {
		log.Fatalf("[E2E] FAIL: meta.type=%s, expected video", meta.Type)
	}
	if meta.URLs["video_high"] == "" || meta.URLs["video_medium"] == "" || meta.URLs["video_low"] == "" {
		log.Fatalf("[E2E] FAIL: video URLs missing in meta: %+v", meta.URLs)
	}
	log.Printf("[E2E] 5/6 preview meta OK: type=%s filename=%s", meta.Type, meta.Filename)
	for k, v := range meta.URLs {
		log.Printf("       url %s: %s", k, v)
	}

	// 6. 调 transcode API（medium，首次触发转码）
	log.Printf("[E2E] 6/6 triggering medium transcode (first call)...")
	resp, err = client.Get(baseURL + "/api/preview/" + fileID + "/transcode?quality=medium")
	must("transcode medium", err, resp)
	mediumBytes, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Fatalf("[E2E] FAIL: medium transcode status=%d", resp.StatusCode)
	}
	if mediumBytes < 1000 {
		log.Fatalf("[E2E] FAIL: medium output too small: %d", mediumBytes)
	}
	log.Printf("[E2E]       medium OK: %d bytes (status=%d)", mediumBytes, resp.StatusCode)

	// 7. 调 transcode API（low）
	log.Printf("[E2E] 6/6 triggering low transcode (first call)...")
	resp, err = client.Get(baseURL + "/api/preview/" + fileID + "/transcode?quality=low")
	must("transcode low", err, resp)
	lowBytes, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Fatalf("[E2E] FAIL: low transcode status=%d", resp.StatusCode)
	}
	if lowBytes < 1000 {
		log.Fatalf("[E2E] FAIL: low output too small: %d", lowBytes)
	}
	log.Printf("[E2E]       low OK: %d bytes (status=%d)", lowBytes, resp.StatusCode)

	// 输出对比
	log.Printf("[E2E] size comparison: source=%d medium=%d (%.1f%%) low=%d (%.1f%%)",
		fileSize, mediumBytes, float64(mediumBytes)/float64(fileSize)*100,
		lowBytes, float64(lowBytes)/float64(fileSize)*100)

	fmt.Println("\n=============================================")
	fmt.Println("[E2E] ALL PASSED: full preview pipeline works")
	fmt.Println("=============================================")
}

// must 检查 HTTP 响应，失败时 fatal
func must(step string, err error, resp *http.Response) {
	if err != nil {
		log.Fatalf("[E2E] FAIL %s: %v", step, err)
	}
	if resp != nil && resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		log.Fatalf("[E2E] FAIL %s: HTTP %d - %s", step, resp.StatusCode, string(body))
	}
}

// uploadChunkViaMultipart 上传单个分片（multipart/form-data 格式）
func uploadChunkViaMultipart(client *http.Client, url, sessionID string, chunkIndex int, data []byte) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("session_id", sessionID)
	w.WriteField("chunk_index", fmt.Sprintf("%d", chunkIndex))
	fw, _ := w.CreateFormFile("chunk_data", fmt.Sprintf("chunk_%d", chunkIndex))
	fw.Write(data)
	w.Close()
	resp, err := client.Post(url, w.FormDataContentType(), &buf)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
