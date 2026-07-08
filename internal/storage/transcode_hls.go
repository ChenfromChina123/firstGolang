package storage

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HLSTempDir 返回指定画质的 HLS 转码本地临时目录路径。
// 规则：{basePath}/_transcoded_hls/{fileID}/{quality}/
// 转码期间存放 ffmpeg 输出的 m3u8 + ts 切片，转码完成上传 OSS 后删除。
func HLSTempDir(basePath, fileID, quality string) string {
	return filepath.Join(basePath, "_transcoded_hls", fileID, quality)
}

// HLSLocalPlaylistPath 返回本地临时 m3u8 文件路径（转码期间前端从此读取）。
func HLSLocalPlaylistPath(basePath, fileID, quality string) string {
	return filepath.Join(HLSTempDir(basePath, fileID, quality), "playlist.m3u8")
}

// HLSLocalSegmentPath 返回指定序号的本地 ts 切片路径。
// segName 形如 "seg_00001.ts"。
func HLSLocalSegmentPath(basePath, fileID, quality, segName string) string {
	return filepath.Join(HLSTempDir(basePath, fileID, quality), segName)
}

// HLSTranscodeLocalExists 检查本地临时 HLS 产物是否存在（转码中或已完成）。
// 通过检查 playlist.m3u8 文件是否存在判断。
func HLSTranscodeLocalExists(basePath, fileID, quality string) bool {
	_, err := os.Stat(HLSLocalPlaylistPath(basePath, fileID, quality))
	return err == nil
}

// HLSTranscodeLocalComplete 检查本地 HLS 转码是否已完成（m3u8 包含 #EXT-X-ENDLIST）。
// 转码中 ffmpeg 持续追加 m3u8，完成后写入 ENDLIST 标记。
func HLSTranscodeLocalComplete(basePath, fileID, quality string) bool {
	data, err := os.ReadFile(HLSLocalPlaylistPath(basePath, fileID, quality))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "#EXT-X-ENDLIST")
}

// HLSTranscodeComplete 检查指定画质的 HLS 转码产物是否已完整可用。
// 优先检查 OSS 缓存（cacheStore != nil），其次检查本地临时文件是否已完成。
// 返回 (complete, location) — location 为 "oss" 或 "local" 或 ""。
func HLSTranscodeComplete(basePath, fileID, quality string, cacheStore TranscodeCacheStore) (bool, string) {
	if cacheStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		exists, err := cacheStore.TranscodeExists(ctx, fileID, quality)
		if err == nil && exists {
			return true, "oss"
		}
	}
	if HLSTranscodeLocalComplete(basePath, fileID, quality) {
		return true, "local"
	}
	return false, ""
}

// GenerateHLSTranscode 调用 ffmpeg 输出 HLS（m3u8 + ts 切片）到本地临时目录，
// 转码期间后台 goroutine 将已完成的 ts 切片实时上传到 OSS，转码完成后上传最终 m3u8 并清理本地临时文件。
//
// 边转边播原理：
//   - ffmpeg 使用 -hls_playlist_type event 输出 EVENT 类型 m3u8，切片完成后追加到 m3u8
//   - 前端 hls.js 周期性重新请求 m3u8 获取新切片列表（无需轮询 API）
//   - 转码中：preview handler 从本地临时目录读取 m3u8 和 ts 文件
//   - 转码完成：preview handler 从 OSS 读取或返回 presigned URL
//
// 并发安全：复用 transcodeInFlight 单飞锁防止同一 fileID+quality 并行转码。
// 内存保护：复用 transcodeGlobalSem 限制全局 ffmpeg 进程数（Step④ 将替换为优先级队列）。
//
// 参数：
//   - basePath: 存储根目录（LocalStorage.BasePath()）
//   - srcPath: 源视频绝对路径
//   - fileID: 文件 UUID
//   - quality: "high" | "medium" | "low"
//   - cacheStore: OSS 缓存后端（nil 时仅保留本地文件，不上传）
func GenerateHLSTranscode(basePath, srcPath, fileID, quality string, cacheStore TranscodeCacheStore) error {
	spec, ok := transcodeSpecMap[quality]
	if !ok {
		return fmt.Errorf("invalid transcode quality: %s", quality)
	}

	ffmpegPath, err := findFFmpeg()
	if err != nil {
		return fmt.Errorf("ffmpeg not installed (set FFMPEG_PATH or add to PATH): %w", err)
	}

	tempDir := HLSTempDir(basePath, fileID, quality)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return fmt.Errorf("create hls temp dir: %w", err)
	}

	playlistPath := filepath.Join(tempDir, "playlist.m3u8")
	segPattern := filepath.Join(tempDir, "seg_%05d.ts")

	// 单飞锁：同一 fileID+quality 的转码请求串行化
	key := fileID + ":" + quality
	muIface, _ := transcodeInFlight.LoadOrStore(key, &sync.Mutex{})
	mu := muIface.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	// 二次检查：持锁后可能已有其他请求完成转码
	if complete, _ := HLSTranscodeComplete(basePath, fileID, quality, cacheStore); complete {
		return nil
	}

	// 全局信号量：限制同时运行的 ffmpeg 进程总数（防 OOM）
	// 注意：必须在单飞锁内获取，避免死锁
	transcodeGlobalSem <- struct{}{}
	defer func() { <-transcodeGlobalSem }()

	// 三次检查：获取信号量后再次确认（可能在等待信号量期间其他请求完成了转码）
	if complete, _ := HLSTranscodeComplete(basePath, fileID, quality, cacheStore); complete {
		return nil
	}

	// 清理可能残留的旧临时文件（上次转码失败残留）
	os.RemoveAll(tempDir)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return fmt.Errorf("recreate hls temp dir: %w", err)
	}

	// ffmpeg HLS 参数：
	//   -f hls                    输出 HLS 格式
	//   -hls_time N               每段时长（秒），low 画质用 4s 减少 OSS 请求数
	//   -hls_playlist_type event  EVENT 类型（只追加不删除，支持边转边播）
	//   -hls_segment_filename     切片文件名模式（%05d 补零便于排序）
	//   -hls_flags independent_segments  每段以 IDR 帧开始，支持随机访问
	// 视频/音频编码参数与 GenerateTranscode 保持一致
	audioBitrate := "128k"
	if quality == "low" {
		audioBitrate = "96k"
	}
	hlsTime := 2
	if quality == "low" {
		hlsTime = 4
	}

	cmd := exec.Command(ffmpegPath,
		"-i", srcPath,
		"-vf", fmt.Sprintf("scale=-2:%d", spec.maxHeight),
		"-c:v", "libx264",
		"-preset", spec.preset,
		"-crf", strconv.Itoa(spec.crf),
		"-threads", "1",
		"-c:a", "aac",
		"-b:a", audioBitrate,
		"-f", "hls",
		"-hls_time", strconv.Itoa(hlsTime),
		"-hls_playlist_type", "event",
		"-hls_segment_filename", segPattern,
		"-hls_flags", "independent_segments",
		"-y",
		playlistPath,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// 如果有 OSS 缓存后端，启动后台 goroutine 实时上传已完成的 ts 切片
	// 上传不阻塞 ffmpeg，失败不影响转码（后续可从本地恢复）
	var uploadWg sync.WaitGroup
	var stopWatcher chan struct{}
	if cacheStore != nil {
		stopWatcher = make(chan struct{})
		uploadWg.Add(1)
		go func() {
			defer uploadWg.Done()
			hlsSegmentUploader(tempDir, fileID, quality, cacheStore, stopWatcher)
		}()
	}

	if err := cmd.Run(); err != nil {
		// ffmpeg 失败：停止 watcher 并等待上传完成，再返回错误
		if stopWatcher != nil {
			close(stopWatcher)
			uploadWg.Wait()
		}
		return fmt.Errorf("ffmpeg hls transcode failed: %w, stderr: %s", err, stderr.String())
	}

	// ffmpeg 成功完成：先停止 watcher 并等待所有切片上传完成，再上传 m3u8 和清理本地
	// 顺序很重要：必须确保所有 ts 切片已上传到 OSS 后才能删除本地文件
	if cacheStore != nil {
		close(stopWatcher)
		uploadWg.Wait()

		// 上传最终 m3u8（含 #EXT-X-ENDLIST 标记，标志转码完整可用）
		m3u8Data, err := os.ReadFile(playlistPath)
		if err != nil {
			return fmt.Errorf("read final m3u8: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := cacheStore.UploadTranscodePlaylist(ctx, fileID, quality, m3u8Data); err != nil {
			return fmt.Errorf("upload m3u8 to oss: %w", err)
		}

		// m3u8 + 所有 ts 切片均已上传到 OSS，安全清理本地临时文件
		os.RemoveAll(tempDir)
	}

	return nil
}

// hlsSegmentUploader 轮询本地临时目录，将已完成的 ts 切片上传到 OSS。
// 完成判断：ffmpeg 按序号写入切片，当 seg_N+1 出现时 seg_N 必已完成。
// 最后一个切片仅在收到 stop 信号（ffmpeg 退出）后上传。
//
// 参数：
//   - dir: 本地临时目录
//   - fileID/quality: 用于构造 OSS key
//   - cacheStore: OSS 缓存后端
//   - stop: 停止信号（ffmpeg 退出后关闭）
func hlsSegmentUploader(dir, fileID, quality string, cacheStore TranscodeCacheStore, stop <-chan struct{}) {
	uploaded := make(map[string]bool)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			// ffmpeg 已退出，上传所有剩余切片（含最后一个）
			uploadPendingSegments(dir, fileID, quality, cacheStore, uploaded, true)
			return
		case <-ticker.C:
			// 定期扫描，跳过最后一个（可能仍在写入）
			uploadPendingSegments(dir, fileID, quality, cacheStore, uploaded, false)
		}
	}
}

// uploadPendingSegments 扫描目录，上传未上传的 ts 切片到 OSS。
// isFinal=true 时上传所有剩余切片（含最后一个）；否则跳过序号最大的切片（可能仍在写入）。
func uploadPendingSegments(dir, fileID, quality string, cacheStore TranscodeCacheStore, uploaded map[string]bool, isFinal bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var segNames []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".ts") && !uploaded[name] {
			segNames = append(segNames, name)
		}
	}
	if len(segNames) == 0 {
		return
	}

	// 按文件名排序确保有序上传
	sort.Strings(segNames)

	// 非最终扫描时跳过最后一个切片（可能 ffmpeg 正在写入）
	limit := len(segNames)
	if !isFinal && limit > 0 {
		limit--
	}

	ctx := context.Background()
	for i := 0; i < limit; i++ {
		name := segNames[i]
		path := filepath.Join(dir, name)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		err = cacheStore.UploadTranscodeSegment(ctx, fileID, quality, name, f, -1)
		f.Close()
		if err == nil {
			uploaded[name] = true
		}
	}
}

// ListHLSSegmentsLocal 列举本地临时目录中的已完成 ts 切片文件名。
// 用于 preview handler 在转码中时列出可用的切片。
// 返回排序后的切片文件名列表（如 ["seg_00000.ts", "seg_00001.ts"]）。
func ListHLSSegmentsLocal(basePath, fileID, quality string) ([]string, error) {
	dir := HLSTempDir(basePath, fileID, quality)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var segs []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".ts") {
			segs = append(segs, name)
		}
	}
	sort.Strings(segs)
	return segs, nil
}
