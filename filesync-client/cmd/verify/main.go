// Package main 命令行验证程序：直接调用 api 和 auth 包测试上传全流程。
// 不依赖 Wails GUI，用于在无 GUI 环境下验证客户端与服务器端的端到端连通性。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filesync-client/internal/api"
	"filesync-client/internal/auth"
)

// verifyConfig 验证程序配置
type verifyConfig struct {
	ServerURL string
	Username  string
	Password  string
	TestDir   string
}

// parseArgs 解析命令行参数：verify <username> <password> [server_url]
func parseArgs() *verifyConfig {
	if len(os.Args) < 3 {
		fmt.Println("用法: verify <username> <password> [server_url]")
		fmt.Println("示例: verify testuser testpass https://aistudy.icu")
		os.Exit(1)
	}
	cfg := &verifyConfig{
		Username: os.Args[1],
		Password: os.Args[2],
		TestDir:  filepath.Join(os.TempDir(), "filesync-verify"),
	}
	if len(os.Args) >= 4 {
		cfg.ServerURL = os.Args[3]
	} else {
		cfg.ServerURL = "https://aistudy.icu"
	}
	return cfg
}

// step1Login 登录服务器，返回 AuthManager
func step1Login(cfg *verifyConfig) *auth.AuthManager {
	fmt.Printf("[步骤1] 登录服务器 %s (用户: %s)\n", cfg.ServerURL, cfg.Username)
	authMgr := auth.New(cfg.ServerURL)
	if err := authMgr.Login(cfg.Username, cfg.Password); err != nil {
		fmt.Printf("登录失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("  登录成功")
	return authMgr
}

// step2VerifyAuth 验证认证：调用 ListFiles
func step2VerifyAuth(client *api.Client) {
	fmt.Println("[步骤2] 验证认证（ListFiles）")
	files, err := client.ListFiles("")
	if err != nil {
		fmt.Printf("  获取文件列表失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  认证成功，当前文件数: %d\n", len(files))
}

// step3CreateTestFile 创建测试文件
func step3CreateTestFile(cfg *verifyConfig) (string, []byte) {
	fmt.Println("[步骤3] 创建测试文件")
	if err := os.MkdirAll(cfg.TestDir, 0755); err != nil {
		fmt.Printf("  创建测试目录失败: %v\n", err)
		os.Exit(1)
	}
	// 文件名加时间戳避免冲突
	filename := fmt.Sprintf("verify-test-%d.txt", time.Now().Unix())
	testFile := filepath.Join(cfg.TestDir, filename)
	// 内容包含时间戳和标识，确保唯一性
	content := []byte(fmt.Sprintf("filesync verify test\ntimestamp: %s\nuser: %s\n",
		time.Now().Format(time.RFC3339), cfg.Username))
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		fmt.Printf("  创建测试文件失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  测试文件: %s (%d bytes)\n", testFile, len(content))
	return testFile, content
}

// computeSHA256 计算文件 SHA256 哈希
func computeSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// step4CheckUpload 秒传检查
func step4CheckUpload(client *api.Client, filename string, size int64, hash string) bool {
	fmt.Println("[步骤4] 秒传检查（CheckUpload）")
	resp, err := client.CheckUpload(api.CheckUploadRequest{
		Filename: filename,
		FileSize: size,
		FileHash: hash,
	})
	if err != nil {
		fmt.Printf("  秒传检查返回错误（继续上传）: %v\n", err)
		return false
	}
	if resp.InstantUpload {
		fmt.Printf("  秒传成功! file_id=%s\n", resp.FileID)
		return true
	}
	fmt.Println("  秒传未命中，继续上传")
	return false
}

// step5InitUpload 初始化上传会话
func step5InitUpload(client *api.Client, filename string, size int64, hash string) *api.InitUploadResponse {
	fmt.Println("[步骤5] 初始化上传（InitUpload, force=true）")
	resp, err := client.InitUpload(api.InitUploadRequest{
		Filename:  filename,
		FileSize:  size,
		ChunkSize: 512 * 1024,
		FileHash:  hash,
		Storage:   "local",
	}, true, false) // force=true 覆盖同名文件
	if err != nil {
		fmt.Printf("  初始化上传失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  会话ID: %s, 总分片: %d\n", resp.SessionID, resp.TotalChunks)
	return resp
}

// step6UploadChunks 上传所有分片
func step6UploadChunks(client *api.Client, sessionID string, filePath string, totalChunks int) {
	fmt.Printf("[步骤6] 上传分片（共 %d 片）\n", totalChunks)
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Printf("  打开文件失败: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	chunkSize := int64(512 * 1024)
	for i := 0; i < totalChunks; i++ {
		offset := int64(i) * chunkSize
		chunkData := make([]byte, chunkSize)
		n, err := file.ReadAt(chunkData, offset)
		if err != nil && err != io.EOF {
			fmt.Printf("  读取分片 %d 失败: %v\n", i, err)
			os.Exit(1)
		}
		if n == 0 {
			break
		}
		chunkData = chunkData[:n]
		if err := client.UploadChunk(sessionID, i, chunkData); err != nil {
			fmt.Printf("  上传分片 %d 失败: %v\n", i, err)
			os.Exit(1)
		}
		fmt.Printf("  分片 %d 上传成功 (%d bytes)\n", i, n)
	}
}

// step7CompleteUpload 完成上传
func step7CompleteUpload(client *api.Client, sessionID string) {
	fmt.Println("[步骤7] 完成上传（CompleteUpload）")
	resp, err := client.CompleteUpload(sessionID)
	if err != nil {
		fmt.Printf("  完成上传失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  上传完成! file_id=%s, filename=%s, size=%d\n", resp.FileID, resp.Filename, resp.Size)
}

// step8VerifyUpload 验证文件已上传
func step8VerifyUpload(client *api.Client, beforeCount int) {
	fmt.Println("[步骤8] 验证上传结果（ListFiles）")
	files, err := client.ListFiles("")
	if err != nil {
		fmt.Printf("  获取文件列表失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  上传后文件数: %d (之前: %d)\n", len(files), beforeCount)
	if len(files) <= beforeCount {
		fmt.Println("  警告: 文件数未增加，可能上传未成功")
		return
	}
	fmt.Println("  文件数增加，上传验证成功!")
}

// main 验证程序入口
func main() {
	cfg := parseArgs()
	fmt.Printf("=== filesync 客户端上传验证 ===\n")
	fmt.Printf("服务器: %s\n用户: %s\n\n", cfg.ServerURL, cfg.Username)

	// 步骤1：登录
	authMgr := step1Login(cfg)

	// 创建 API Client
	client := api.New(authMgr)

	// 步骤2：验证认证
	step2VerifyAuth(client)
	filesBefore, _ := client.ListFiles("")
	beforeCount := len(filesBefore)

	// 步骤3：创建测试文件
	testFile, content := step3CreateTestFile(cfg)
	defer os.RemoveAll(cfg.TestDir) // 清理本地测试文件

	// 步骤4：计算 SHA256
	fmt.Println("[步骤3.5] 计算 SHA256")
	hash, err := computeSHA256(testFile)
	if err != nil {
		fmt.Printf("  计算哈希失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  SHA256: %s\n", hash)

	filename := filepath.Base(testFile)
	size := int64(len(content))

	// 步骤4：秒传检查
	if step4CheckUpload(client, filename, size, hash) {
		fmt.Println("\n=== 验证完成（秒传）===")
		return
	}

	// 步骤5：初始化上传
	initResp := step5InitUpload(client, filename, size, hash)

	// 步骤6：上传分片
	step6UploadChunks(client, initResp.SessionID, testFile, initResp.TotalChunks)

	// 步骤7：完成上传
	step7CompleteUpload(client, initResp.SessionID)

	// 步骤8：验证上传结果
	step8VerifyUpload(client, beforeCount)

	fmt.Println("\n=== 验证完成 ===")
}
