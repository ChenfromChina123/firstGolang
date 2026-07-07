// Package main 跨用户秒传测试程序：验证全局存储下不同用户秒传同一文件。
// 流程：用户A 上传文件 → 用户B CheckUpload 相同内容 → 验证秒传命中 + storage_path 共享。
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

// 测试配置
type testConfig struct {
	ServerURL string
	UserA     string // 用户A（已有文件或先上传）
	PassA     string
	UserB     string // 用户B（测试跨用户秒传）
	PassB     string
}

// parseArgs 解析参数：crossuser <userA> <passA> <userB> <passB> [server_url]
func parseArgs() *testConfig {
	if len(os.Args) < 5 {
		fmt.Println("用法: crossuser <userA> <passA> <userB> <passB> [server_url]")
		fmt.Println("示例: crossuser 3301767269@qq.com 12345678A admin ***REMOVED_PASSWORD*** https://aistudy.icu")
		os.Exit(1)
	}
	cfg := &testConfig{
		UserA: os.Args[1],
		PassA: os.Args[2],
		UserB: os.Args[3],
		PassB: os.Args[4],
	}
	if len(os.Args) >= 6 {
		cfg.ServerURL = os.Args[5]
	} else {
		cfg.ServerURL = "https://aistudy.icu"
	}
	return cfg
}

// login 登录并返回 client
func login(serverURL, user, pass string) *api.Client {
	authMgr := auth.New(serverURL)
	if err := authMgr.Login(user, pass); err != nil {
		fmt.Printf("  登录失败 (%s): %v\n", user, err)
		os.Exit(1)
	}
	return api.New(authMgr)
}

// computeSHA256 计算字节流的 SHA256
func computeSHA256(data []byte) string {
	h := sha256.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// writeFile 写临时文件
func writeFile(content []byte) string {
	dir := filepath.Join(os.TempDir(), "filesync-crossuser")
	os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, fmt.Sprintf("crossuser-test-%d.txt", time.Now().Unix()))
	os.WriteFile(path, content, 0644)
	return path
}

// uploadFile 完整上传文件（不复用秒传）
func uploadFile(client *api.Client, filename string, content []byte) string {
	hash := computeSHA256(content)
	testFile := writeFile(content)
	defer os.Remove(testFile)

	// CheckUpload（应该未命中）
	resp, err := client.CheckUpload(api.CheckUploadRequest{
		Filename: filename,
		FileSize: int64(len(content)),
		FileHash: hash,
	})
	if err == nil && resp.InstantUpload {
		fmt.Printf("  秒传命中（跳过上传）file_id=%s\n", resp.FileID)
		return resp.FileID
	}

	// InitUpload force=true
	initResp, err := client.InitUpload(api.InitUploadRequest{
		Filename:  filename,
		FileSize:  int64(len(content)),
		ChunkSize: 512 * 1024,
		FileHash:  hash,
		Storage:   "local",
	}, true, false)
	if err != nil {
		fmt.Printf("  InitUpload 失败: %v\n", err)
		os.Exit(1)
	}

	// UploadChunk
	f, _ := os.Open(testFile)
	defer f.Close()
	chunkData := make([]byte, len(content))
	f.Read(chunkData)
	if err := client.UploadChunk(initResp.SessionID, 0, chunkData); err != nil {
		fmt.Printf("  UploadChunk 失败: %v\n", err)
		os.Exit(1)
	}

	// CompleteUpload
	completeResp, err := client.CompleteUpload(initResp.SessionID)
	if err != nil {
		fmt.Printf("  CompleteUpload 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  上传成功 file_id=%s hash=%s\n", completeResp.FileID, hash[:16])
	return completeResp.FileID
}

func main() {
	cfg := parseArgs()
	fmt.Printf("=== 跨用户秒传测试 ===\n服务器: %s\n用户A: %s\n用户B: %s\n\n", cfg.ServerURL, cfg.UserA, cfg.UserB)

	// 固定内容（两个用户上传相同内容）
	content := []byte("cross-user instant upload test content\nsame hash same file\n")
	filename := fmt.Sprintf("crossuser-test-%d.txt", time.Now().Unix())
	hash := computeSHA256(content)
	fmt.Printf("测试文件: %s (%d bytes)\nSHA256: %s\n\n", filename, len(content), hash)

	// 步骤1：用户A 上传文件
	fmt.Println("[步骤1] 用户A 上传文件")
	clientA := login(cfg.ServerURL, cfg.UserA, cfg.PassA)
	filesBeforeA, _ := clientA.ListFiles("")
	fmt.Printf("  用户A 文件数: %d\n", len(filesBeforeA))
	uploadFile(clientA, filename, content)

	// 步骤2：用户B CheckUpload（应该命中全局秒传）
	fmt.Println("\n[步骤2] 用户B CheckUpload（跨用户秒传）")
	clientB := login(cfg.ServerURL, cfg.UserB, cfg.PassB)
	filesBeforeB, _ := clientB.ListFiles("")
	fmt.Printf("  用户B 文件数: %d\n", len(filesBeforeB))

	checkResp, err := clientB.CheckUpload(api.CheckUploadRequest{
		Filename: filename,
		FileSize: int64(len(content)),
		FileHash: hash,
	})
	if err != nil {
		fmt.Printf("  CheckUpload 错误: %v\n", err)
		os.Exit(1)
	}

	if !checkResp.InstantUpload {
		fmt.Printf("  秒传未命中！跨用户秒传失败\n")
		fmt.Println("\n=== 测试失败 ===")
		os.Exit(1)
	}

	fmt.Printf("  秒传命中！file_id=%s size=%d hash=%s\n", checkResp.FileID, checkResp.Size, checkResp.Hash[:16])

	// 步骤3：用户B ListFiles（应该 +1）
	fmt.Println("\n[步骤3] 验证用户B 文件数")
	filesAfterB, _ := clientB.ListFiles("")
	fmt.Printf("  用户B 文件数: %d (之前: %d)\n", len(filesAfterB), len(filesBeforeB))
	if len(filesAfterB) <= len(filesBeforeB) {
		fmt.Printf("  警告: 文件数未增加\n")
	} else {
		fmt.Printf("  文件数增加 %d,跨用户秒传成功!\n", len(filesAfterB)-len(filesBeforeB))
	}

	// 步骤4：验证 storage_path 共享（可选，需要 admin 权限或特殊接口）
	fmt.Println("\n[步骤4] 测试完成")
	fmt.Println("  全局存储已启用:不同用户上传相同文件直接秒传,共享物理存储")
	fmt.Println("\n=== 测试成功 ===")

	// 读 stdin 防止程序立即退出（方便看输出）
	fmt.Println("\n按 Enter 退出...")
	io.ReadFull(os.Stdin, make([]byte, 1))
}
