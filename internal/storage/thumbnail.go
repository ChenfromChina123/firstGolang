package storage

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/disintegration/imaging"
)

// transcodeInFlight 单飞锁，防止同一 fileID+quality 的转码请求并行执行。
// 场景：浏览器因连接超时重试或并发 Range 请求，会触发多次 transcode API 调用，
// 若不串行化则会在低内存服务器（如 1.8GB）上同时启动多个 ffmpeg 进程导致 OOM。
// key: "fileID:quality"，value: *sync.Mutex
var transcodeInFlight sync.Map

// transcodeGlobalSem 全局并发信号量，限制服务器同时运行的 ffmpeg 进程总数。
// 低内存服务器（1.8GB RAM）并行运行 2+ 个 ffmpeg 会触发 OOM kill。
// 缓冲大小=1：同一时刻最多 1 个 ffmpeg 转码，串行执行。
var transcodeGlobalSem = make(chan struct{}, 1)

// findFFmpeg 返回 ffmpeg 可执行文件路径。
// 优先用 FFMPEG_PATH 环境变量（部署时 ffmpeg 不在 PATH 也能用），
// 回退到 exec.LookPath 在 PATH 中查找。
// 找不到时返回 error，调用方应友好降级。
func findFFmpeg() (string, error) {
	if p := os.Getenv("FFMPEG_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return exec.LookPath("ffmpeg")
}

// 缩略图尺寸规格：小/中/大三档，保持宽高比，最长边受限。
var thumbSizeMap = map[string]struct{ w, h int }{
	"small":  {320, 240},
	"medium": {800, 600},
	"large":  {1600, 1200},
}

// ThumbnailPath 返回缩略图缓存文件的绝对路径。
// 格式：{basePath}/_thumbnails/{fileID}/{size}.jpg
// basePath 由 LocalStorage.BasePath() 提供。
func ThumbnailPath(basePath, fileID, size string) string {
	return filepath.Join(basePath, "_thumbnails", fileID, size+".jpg")
}

// PosterPath 返回视频海报缓存文件的绝对路径。
// 格式：{basePath}/_posters/{fileID}.jpg
// 第二阶段实现时启用。
func PosterPath(basePath, fileID string) string {
	return filepath.Join(basePath, "_posters", fileID+".jpg")
}

// TranscodePath 返回视频转码缓存文件的绝对路径。
// 格式：{basePath}/_transcoded/{fileID}/{quality}.mp4
// 第三阶段实现时启用。
func TranscodePath(basePath, fileID, quality string) string {
	return filepath.Join(basePath, "_transcoded", fileID, quality+".mp4")
}

// GenerateThumbnail 用 disintegration/imaging 生成指定尺寸的缩略图。
// srcPath 为源文件绝对路径；输出 JPEG 质量 85，保持宽高比缩放至规格内。
// 生成成功返回缩略图绝对路径。已存在缓存时直接返回（调用方应先检查）。
//
// 参数：
//   - basePath: 存储根目录（LocalStorage.BasePath()）
//   - srcPath: 源图片绝对路径
//   - fileID: 文件 UUID（用于构造缓存子目录）
//   - size: "small" | "medium" | "large"
func GenerateThumbnail(basePath, srcPath, fileID, size string) (string, error) {
	spec, ok := thumbSizeMap[size]
	if !ok {
		return "", fmt.Errorf("invalid thumb size: %s", size)
	}

	// 打开源图片（自动识别 jpeg/png/gif/webp/bmp/tiff）
	src, err := imaging.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("open source image: %w", err)
	}

	// 缩放：保持宽高比，缩到 spec.w x spec.h 内（不裁剪、不拉伸）
	thumb := imaging.Fit(src, spec.w, spec.h, imaging.Lanczos)

	// 确保缓存目录存在
	dstDir := filepath.Join(basePath, "_thumbnails", fileID)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return "", fmt.Errorf("create thumb dir: %w", err)
	}

	dstPath := ThumbnailPath(basePath, fileID, size)
	// JPEG 质量 85，体积与画质平衡
	if err := imaging.Save(thumb, dstPath, imaging.JPEGQuality(85)); err != nil {
		return "", fmt.Errorf("save thumb: %w", err)
	}
	return dstPath, nil
}

// ThumbnailExists 检查指定缩略图缓存是否已存在。
// 调用方在生成前应先调用此函数避免重复生成。
func ThumbnailExists(basePath, fileID, size string) bool {
	if _, err := os.Stat(ThumbnailPath(basePath, fileID, size)); err == nil {
		return true
	}
	return false
}

// PosterExists 检查视频海报缓存是否已存在。
// 调用方在生成前应先调用此函数避免重复生成。
func PosterExists(basePath, fileID string) bool {
	if _, err := os.Stat(PosterPath(basePath, fileID)); err == nil {
		return true
	}
	return false
}

// transcodeSpec 定义视频转码规格。
// maxHeight 配合 -vf scale=-2:H 保持宽高比（宽度自动取偶数）；
// crf 控制 H.264 质量（18≈无损，23=视觉无损，28=有损压缩）；
// preset 控制 x264 编码速度与压缩率平衡（ultrafast→veryslow）。
type transcodeSpec struct {
	maxHeight int
	crf       int
	preset    string
}

// transcodeSpecMap 三档画质规格。
// high=1080p CRF20（接近原画），medium=720p CRF23（默认），low=480p CRF26（省带宽）。
var transcodeSpecMap = map[string]transcodeSpec{
	"high":   {1080, 20, "fast"},
	"medium": {720, 23, "fast"},
	"low":    {480, 26, "veryfast"},
}

// TranscodeExists 检查指定画质的转码缓存是否已存在。
// 调用方在生成前应先调用此函数避免重复转码。
func TranscodeExists(basePath, fileID, quality string) bool {
	if _, ok := transcodeSpecMap[quality]; !ok {
		return false
	}
	if _, err := os.Stat(TranscodePath(basePath, fileID, quality)); err == nil {
		return true
	}
	return false
}

// GenerateTranscode 调用 ffmpeg 将源视频转码为指定画质 mp4（H.264 + AAC）。
// 输出统一 mp4 容器，浏览器原生兼容；scale 滤镜保留宽高比，源分辨率低于目标时不会上采样。
// 生成成功返回转码文件绝对路径。已存在缓存时调用方应先检查 TranscodeExists。
//
// 并发安全：内部使用单飞锁（fileID+quality），防止浏览器重试或并发请求触发多个 ffmpeg 进程。
// 内存保护：-threads 1 限制单线程，避免在低内存服务器（1.8GB）触发 OOM。
// 失败清理：ffmpeg 异常退出时删除部分写入的文件，防止下次请求读到损坏文件。
//
// 参数：
//   - basePath: 存储根目录（LocalStorage.BasePath()）
//   - srcPath: 源视频绝对路径
//   - fileID: 文件 UUID（用于构造缓存子目录）
//   - quality: "high" | "medium" | "low"
func GenerateTranscode(basePath, srcPath, fileID, quality string) (string, error) {
	spec, ok := transcodeSpecMap[quality]
	if !ok {
		return "", fmt.Errorf("invalid transcode quality: %s", quality)
	}
	ffmpegPath, err := findFFmpeg()
	if err != nil {
		return "", fmt.Errorf("ffmpeg not installed (set FFMPEG_PATH or add to PATH): %w", err)
	}

	dstDir := filepath.Join(basePath, "_transcoded", fileID)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return "", fmt.Errorf("create transcode dir: %w", err)
	}

	dstPath := TranscodePath(basePath, fileID, quality)

	// 单飞锁：同一 fileID+quality 的转码请求串行化，避免并行 ffmpeg 触发 OOM
	key := fileID + ":" + quality
	muIface, _ := transcodeInFlight.LoadOrStore(key, &sync.Mutex{})
	mu := muIface.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	// 二次检查：持锁后可能已有其他请求完成转码，避免重复执行
	if _, err := os.Stat(dstPath); err == nil {
		return dstPath, nil
	}

	// 全局信号量：限制服务器同时运行的 ffmpeg 进程总数为 1（防 OOM）
	// 注意：必须在单飞锁内获取信号量，避免死锁（先抢信号量再抢单飞锁会导致后续请求阻塞）
	transcodeGlobalSem <- struct{}{}
	defer func() { <-transcodeGlobalSem }()

	// ffmpeg 关键参数：
	//   -i srcPath                源视频
	//   -vf scale=-2:H            缩放：高度=H，宽度自动（-2 保证偶数，x264 要求）
	//   -c:v libx264              H.264 视频编码
	//   -preset fast/veryfast     编码速度（快→文件大，慢→文件小）
	//   -crf N                    质量因子（小=高质量大文件）
	//   -threads 1                单线程编码，限制内存峰值（防止 OOM kill）
	//   -c:a aac                  音频 AAC
	//   -b:a 128k                 音频码率 128kbps（low 档用 96k 节省带宽）
	//   -movflags +faststart      moov atom 前置，支持浏览器边下边播
	//   -y                        覆盖已存在文件
	audioBitrate := "128k"
	if quality == "low" {
		audioBitrate = "96k"
	}

	// 使用临时文件写入，转码成功后原子 rename 到目标路径
	// 避免浏览器在文件写入过程中请求到不完整数据触发 DEMUXER_ERROR
	// 临时文件名保留 .mp4 后缀，否则 ffmpeg 无法通过扩展名推断输出格式
	tmpPath := dstPath + ".part.mp4"
	cmd := exec.Command(ffmpegPath,
		"-i", srcPath,
		"-vf", fmt.Sprintf("scale=-2:%d", spec.maxHeight),
		"-c:v", "libx264",
		"-preset", spec.preset,
		"-crf", strconv.Itoa(spec.crf),
		"-threads", "1",
		"-c:a", "aac",
		"-b:a", audioBitrate,
		"-movflags", "+faststart",
		"-y",
		tmpPath,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// 失败时清理临时文件，防止磁盘残留
		os.Remove(tmpPath)
		return "", fmt.Errorf("ffmpeg transcode failed: %w, stderr: %s", err, stderr.String())
	}

	// 原子 rename：目标文件要么不存在（转码中），要么完整可用（转码后）
	// 浏览器不会读到部分写入的数据
	if err := os.Rename(tmpPath, dstPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("rename transcode file: %w", err)
	}
	return dstPath, nil
}

// GeneratePoster 调用 ffmpeg 截取视频第一帧生成海报。
// 输出 JPEG，宽度 800px（保持比例），质量 q:v=5（中等画质，预览画质低一点）。
// 生成成功返回海报绝对路径。已存在缓存时调用方应先检查 PosterExists。
//
// 参数：
//   - basePath: 存储根目录（LocalStorage.BasePath()）
//   - srcPath: 源视频绝对路径
//   - fileID: 文件 UUID（用于构造缓存文件名）
func GeneratePoster(basePath, srcPath, fileID string) (string, error) {
	// 检查 ffmpeg 是否可用（优先 FFMPEG_PATH 环境变量）
	ffmpegPath, err := findFFmpeg()
	if err != nil {
		return "", fmt.Errorf("ffmpeg not installed (set FFMPEG_PATH or add to PATH): %w", err)
	}

	// 确保缓存目录存在
	dstDir := filepath.Join(basePath, "_posters")
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return "", fmt.Errorf("create poster dir: %w", err)
	}

	dstPath := PosterPath(basePath, fileID)

	// ffmpeg 命令：
	//   -ss 0:00:01 跳过第 1 秒（避免黑屏片头）
	//   -i srcPath  源视频
	//   -vframes 1  只输出一帧
	//   -vf scale=800:-1  缩放到宽度 800，高度按比例
	//   -q:v 5      JPEG 质量（2=最高，31=最低，5=中等画质）
	//   -y          覆盖已存在文件
	cmd := exec.Command(ffmpegPath,
		"-ss", "0:00:01",
		"-i", srcPath,
		"-vframes", "1",
		"-vf", "scale=800:-1",
		"-q:v", "5",
		"-y",
		dstPath,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg failed: %w, stderr: %s", err, stderr.String())
	}

	return dstPath, nil
}

// === 异步转码任务管理 ===

// TranscodeJobStatus 转码任务状态枚举。
type TranscodeJobStatus string

const (
	TranscodeStatusPending TranscodeJobStatus = "pending"  // 已创建任务，等待 goroutine 启动
	TranscodeStatusRunning TranscodeJobStatus = "running"  // goroutine 正在执行 ffmpeg
	TranscodeStatusDone    TranscodeJobStatus = "done"     // 转码完成，缓存文件可用
	TranscodeStatusFailed  TranscodeJobStatus = "failed"   // 转码失败（可能重试）
)

// transcodeJobMaxRetry 失败任务的最大重试次数，超过则不再重启。
const transcodeJobMaxRetry = 3

// TranscodeJob 描述一个异步转码任务的状态。
// Status/Error 字段受 mu 保护，支持并发读写（goroutine 写、API 读）。
type TranscodeJob struct {
	FileID     string
	Quality    string
	Status     TranscodeJobStatus
	Error      string
	RetryCount int
	Started    time.Time
	mu         sync.RWMutex
}

// GetStatus 原子读取任务状态。
func (j *TranscodeJob) GetStatus() TranscodeJobStatus {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.Status
}

// GetError 原子读取错误信息。
func (j *TranscodeJob) GetError() string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.Error
}

// setStatus 原子更新任务状态。
func (j *TranscodeJob) setStatus(s TranscodeJobStatus) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = s
}

// setStatusAndError 原子更新状态与错误信息。
func (j *TranscodeJob) setStatusAndError(s TranscodeJobStatus, e string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = s
	j.Error = e
}

// transcodeJobs 全局任务表，key="fileID:quality" → *TranscodeJob。
// 任务完成后由 time.AfterFunc 延迟清理，避免长期运行内存泄漏。
var transcodeJobs sync.Map

// StartTranscodeJob 启动异步转码任务（若已存在则返回当前状态）。
//
// 流程（修正 TOCTOU 竞态）：
//  1. LoadOrStore 占位 job（status=pending）—— 并发请求必然命中此占位
//  2. 占位后检查缓存完整性，命中则原子改 status=done，不入队
//  3. 若 job 已存在且 status=failed 且 RetryCount<3，CAS 重置为 pending 重新入队
//  4. 若 job 已存在且 status=running/pending，直接返回当前状态
//  5. 入队到优先级队列（priorityHigh），由单 worker 调度执行
//  6. worker 执行完毕后注册 time.AfterFunc：done 5min / failed 30min 后清理
//
// cacheStore 参数决定转码路径：
//   - nil：走 MP4 fallback（GenerateTranscode，本地缓存）
//   - 非 nil：走 HLS（GenerateHLSTranscode，边转边播 + OSS 缓存）
//
// 返回值：当前任务状态字符串（pending/running/done/failed）
func StartTranscodeJob(basePath, srcPath, fileID, quality string, cacheStore TranscodeCacheStore) string {
	key := fileID + ":" + quality

	// 占位 job：LoadOrStore 保证并发请求只有一个创建成功
	placeholder := &TranscodeJob{
		FileID:  fileID,
		Quality: quality,
		Status:  TranscodeStatusPending,
		Started: time.Now(),
	}
	actual, loaded := transcodeJobs.LoadOrStore(key, placeholder)
	job := actual.(*TranscodeJob)

	// 已存在的 job：判断状态
	if loaded {
		status := job.GetStatus()
		switch status {
		case TranscodeStatusRunning, TranscodeStatusPending:
			// 任务进行中，直接返回状态（前端轮询即可）
			return string(status)
		case TranscodeStatusDone:
			// 二次检查缓存（可能被外部清理）
			if transcodeCacheComplete(basePath, fileID, quality, cacheStore) {
				return string(TranscodeStatusDone)
			}
			// 缓存丢失，删除旧 job 重新启动（落到下面的占位逻辑）
			transcodeJobs.Delete(key)
			return StartTranscodeJob(basePath, srcPath, fileID, quality, cacheStore)
		case TranscodeStatusFailed:
			// 失败重试：RetryCount < 上限时重置为 pending 重新入队
			if job.RetryCount >= transcodeJobMaxRetry {
				return string(TranscodeStatusFailed)
			}
			job.RetryCount++
			job.setStatusAndError(TranscodeStatusPending, "")
			// 落到下面的入队逻辑
		}
	}

	// 持有占位 job（新创建或失败重试），检查缓存完整性
	if transcodeCacheComplete(basePath, fileID, quality, cacheStore) {
		job.setStatus(TranscodeStatusDone)
		scheduleJobCleanup(key, TranscodeStatusDone)
		return string(TranscodeStatusDone)
	}

	// 入队到优先级队列（用户请求为 high 优先级），由单 worker 调度执行
	// worker 内部调用 GenerateHLSTranscode 或 GenerateTranscode，自带单飞锁和信号量
	EnqueueTranscode(transcodeRequest{
		FileID:      fileID,
		Quality:     quality,
		SrcPath:     srcPath,
		BasePath:    basePath,
		Priority:    priorityHigh,
		EnqueueTime: time.Now(),
		CacheStore:  cacheStore,
	})

	return string(TranscodeStatusPending)
}

// transcodeCacheComplete 检查转码缓存是否已完整可用。
// cacheStore != nil 时检查 OSS + 本地 HLS 产物；否则检查本地 MP4 产物。
func transcodeCacheComplete(basePath, fileID, quality string, cacheStore TranscodeCacheStore) bool {
	if cacheStore != nil {
		complete, _ := HLSTranscodeComplete(basePath, fileID, quality, cacheStore)
		return complete
	}
	return TranscodeExists(basePath, fileID, quality)
}

// GetTranscodeJob 查询转码任务状态（只读）。
// 返回 nil 时调用方应 fallback 到 TranscodeExists 检查磁盘缓存。
func GetTranscodeJob(fileID, quality string) *TranscodeJob {
	key := fileID + ":" + quality
	if v, ok := transcodeJobs.Load(key); ok {
		return v.(*TranscodeJob)
	}
	return nil
}

// scheduleJobCleanup 延迟清理已完成的任务，避免 transcodeJobs 长期运行内存泄漏。
// done 状态 5 分钟后清理（前端轮询窗口足够）；failed 状态 30 分钟后清理（保留重试窗口）。
func scheduleJobCleanup(key string, status TranscodeJobStatus) {
	var delay time.Duration
	switch status {
	case TranscodeStatusDone:
		delay = 5 * time.Minute
	case TranscodeStatusFailed:
		delay = 30 * time.Minute
	default:
		return
	}
	time.AfterFunc(delay, func() {
		transcodeJobs.Delete(key)
	})
}
