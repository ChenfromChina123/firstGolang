package storage

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/disintegration/imaging"
)

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

// GeneratePoster 调用 ffmpeg 截取视频第一帧生成海报。
// 输出 JPEG，宽度 800px（保持比例），质量 q:v=5（中等画质，预览画质低一点）。
// 生成成功返回海报绝对路径。已存在缓存时调用方应先检查 PosterExists。
//
// 参数：
//   - basePath: 存储根目录（LocalStorage.BasePath()）
//   - srcPath: 源视频绝对路径
//   - fileID: 文件 UUID（用于构造缓存文件名）
func GeneratePoster(basePath, srcPath, fileID string) (string, error) {
	// 检查 ffmpeg 是否可用
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", fmt.Errorf("ffmpeg not installed: %w", err)
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
	cmd := exec.Command("ffmpeg",
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
