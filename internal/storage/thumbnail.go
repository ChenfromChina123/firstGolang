package storage

import (
	"fmt"
	"os"
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
