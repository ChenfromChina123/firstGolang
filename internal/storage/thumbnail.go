package storage

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"

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
		dstPath,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// 失败时清理部分写入的文件，防止下次请求读到损坏文件触发 DEMUXER_ERROR
		os.Remove(dstPath)
		return "", fmt.Errorf("ffmpeg transcode failed: %w, stderr: %s", err, stderr.String())
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
