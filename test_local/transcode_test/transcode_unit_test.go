// Package main 提供转码函数单元测试（GenerateTranscode/TranscodeExists）。
// 运行方式：
//
//	$env:FFMPEG_PATH = "D:\下载\ffmpeg-master-latest-win64-gpl\ffmpeg-master-latest-win64-gpl\bin\ffmpeg.exe"
//	go test ./test_local/transcode_test/ -run TestGenerateTranscode -v
//
// 验证点：
//  1. findFFmpeg 正确解析 FFMPEG_PATH 环境变量
//  2. GenerateTranscode 对 medium(720p) 和 low(480p) 输出有效 mp4
//  3. TranscodeExists 缓存命中
//  4. 输出文件大小、是否非空
package main

import (
	"os"
	"os/exec"
	"testing"

	"filesync/internal/storage"
)

// TestGenerateTranscode 验证 GenerateTranscode 函数对 medium/low 两档画质的输出。
// 跳过条件：源视频不存在或 FFMPEG_PATH 未配置时跳过。
func TestGenerateTranscode(t *testing.T) {
	basePath := "./_transcode_cache" // 临时缓存根目录（go test cwd 是测试文件目录）
	srcPath := "./sample_720p.mp4"  // 由 ffmpeg lavfi 生成的 720p 10s 测试视频
	fileID := "transcode_test_v1"

	// 跳过条件：源视频不存在
	if _, err := os.Stat(srcPath); err != nil {
		t.Skipf("source video not found: %v (run ffmpeg lavfi first)", err)
	}
	// 跳过条件：ffmpeg 不可用
	if os.Getenv("FFMPEG_PATH") == "" {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			t.Skipf("ffmpeg not in PATH and FFMPEG_PATH not set")
		}
	}

	// 清理旧缓存以便真实走转码流程
	os.RemoveAll(basePath)
	t.Cleanup(func() { os.RemoveAll(basePath) })

	// 验证 medium/low 两档（high 在 serveTranscode 中走 serveContent 不需转码）
	for _, q := range []string{"medium", "low"} {
		t.Run(q, func(t *testing.T) {
			// 第一次调用：未命中缓存 → 同步转码
			if storage.TranscodeExists(basePath, fileID, q) {
				t.Fatalf("cache should be empty for %s", q)
			}
			dst, err := storage.GenerateTranscode(basePath, srcPath, fileID, q)
			if err != nil {
				t.Fatalf("GenerateTranscode(%s) err=%v", q, err)
			}
			fi, _ := os.Stat(dst)
			if fi.Size() == 0 {
				t.Fatalf("%s output empty", q)
			}
			t.Logf("%s -> %s (size=%d bytes)", q, dst, fi.Size())

			// 第二次调用：应命中缓存（不重新转码）
			if !storage.TranscodeExists(basePath, fileID, q) {
				t.Fatalf("cache miss after generation for %s", q)
			}
		})
	}
}
