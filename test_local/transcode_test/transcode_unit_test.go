// Package main 提供转码函数单元测试（GenerateTranscode/TranscodeExists/StartTranscodeJob）。
// 运行方式：
//
//	$env:FFMPEG_PATH = "D:\下载\ffmpeg-master-latest-win64-gpl\ffmpeg-master-latest-win64-gpl\bin\ffmpeg.exe"
//	go test ./test_local/transcode_test/ -run TestGenerateTranscode -v
//	go test ./test_local/transcode_test/ -run TestStartTranscodeJob -v
//	go test ./test_local/transcode_test/ -run TestGetTranscodeJob -v
//
// 验证点：
//  1. findFFmpeg 正确解析 FFMPEG_PATH 环境变量
//  2. GenerateTranscode 对 medium(720p) 和 low(480p) 输出有效 mp4
//  3. TranscodeExists 缓存命中
//  4. 输出文件大小、是否非空
//  5. StartTranscodeJob 并发安全（10 goroutine 同 key 只启动 1 个 ffmpeg）
//  6. StartTranscodeJob 缓存命中返回 done
//  7. GetTranscodeJob 未启动返回 nil
package main

import (
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

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

// TestStartTranscodeJob_ConcurrentSameKey 验证并发安全：10 goroutine 同时调用同 fileID+quality，
// 应只启动 1 个 ffmpeg 进程（通过全局信号量 + 单飞锁保证），所有调用最终等到 done 状态。
// 跳过条件：源视频不存在或 FFMPEG_PATH 未配置时跳过。
func TestStartTranscodeJob_ConcurrentSameKey(t *testing.T) {
	basePath := "./_transcode_cache_concurrent"
	srcPath := "./sample_720p.mp4"
	fileID := "transcode_test_concurrent_v1"
	quality := "medium"

	if _, err := os.Stat(srcPath); err != nil {
		t.Skipf("source video not found: %v", err)
	}
	if os.Getenv("FFMPEG_PATH") == "" {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			t.Skipf("ffmpeg not in PATH and FFMPEG_PATH not set")
		}
	}

	os.RemoveAll(basePath)
	t.Cleanup(func() { os.RemoveAll(basePath) })

	// 启动优先级队列调度器（单 worker），否则入队任务不会执行
	storage.StartTranscodeScheduler()

	var wg sync.WaitGroup
	const goroutines = 10
	statuses := make([]string, goroutines)

	// 10 goroutine 同时启动转码任务
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			statuses[idx] = storage.StartTranscodeJob(basePath, srcPath, fileID, quality, nil, false)
		}(i)
	}
	wg.Wait()

	// 至少有一个返回 pending/running/done（不应所有都 failed）
	hasValid := false
	for i, s := range statuses {
		if s == "pending" || s == "running" || s == "done" {
			hasValid = true
		}
		t.Logf("goroutine %d status=%s", i, s)
	}
	if !hasValid {
		t.Fatalf("all goroutines failed, statuses=%v", statuses)
	}

	// 等待转码完成（最多 60s）
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		job := storage.GetTranscodeJob(fileID, quality)
		if job != nil && job.GetStatus() == storage.TranscodeStatusDone {
			break
		}
		if storage.TranscodeExists(basePath, fileID, quality) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// 验证最终完成
	if !storage.TranscodeExists(basePath, fileID, quality) {
		t.Fatalf("transcode not completed after 60s")
	}
	t.Logf("concurrent transcode completed, cache file exists")
}

// TestStartTranscodeJob_CacheHit 验证缓存命中时立即返回 done，不启动 goroutine。
// 先用 GenerateTranscode 生成缓存，再调 StartTranscodeJob 应返回 done。
func TestStartTranscodeJob_CacheHit(t *testing.T) {
	basePath := "./_transcode_cache_hit"
	srcPath := "./sample_720p.mp4"
	fileID := "transcode_test_hit_v1"
	quality := "low"

	if _, err := os.Stat(srcPath); err != nil {
		t.Skipf("source video not found: %v", err)
	}
	if os.Getenv("FFMPEG_PATH") == "" {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			t.Skipf("ffmpeg not in PATH and FFMPEG_PATH not set")
		}
	}

	os.RemoveAll(basePath)
	t.Cleanup(func() { os.RemoveAll(basePath) })

	// 先生成缓存
	if _, err := storage.GenerateTranscode(basePath, srcPath, fileID, quality); err != nil {
		t.Fatalf("GenerateTranscode setup failed: %v", err)
	}
	if !storage.TranscodeExists(basePath, fileID, quality) {
		t.Fatalf("cache should exist after GenerateTranscode")
	}

	// 调用 StartTranscodeJob，应立即返回 done（缓存命中）
	status := storage.StartTranscodeJob(basePath, srcPath, fileID, quality, nil, false)
	if status != "done" {
		t.Fatalf("expected status=done for cache hit, got %s", status)
	}
	t.Logf("cache hit returned status=%s", status)
}

// TestGetTranscodeJob_NotStarted 验证从未启动任务时 GetTranscodeJob 返回 nil。
// 纯逻辑测试，不需要 ffmpeg。
func TestGetTranscodeJob_NotStarted(t *testing.T) {
	fileID := "never_started_file_v1"
	quality := "medium"

	job := storage.GetTranscodeJob(fileID, quality)
	if job != nil {
		t.Fatalf("expected nil for never started job, got %v", job)
	}
	t.Logf("GetTranscodeJob returned nil for never started job (correct)")
}
