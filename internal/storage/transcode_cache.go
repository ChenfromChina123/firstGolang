package storage

import (
	"context"
	"fmt"
	"io"
	"time"
)

// TranscodeCacheStore 抽象视频转码产物的缓存存储后端。
// 当前实现：S3Storage（OSS）。本地存储场景下传 nil，转码走本地磁盘缓存（旧逻辑）。
// 设计目的：解耦转码逻辑与存储后端，便于未来支持其他对象存储。
type TranscodeCacheStore interface {
	// UploadTranscodeSegment 上传单个 HLS ts 切片到缓存。
	// segName 为切片文件名（如 "seg_00001.ts"），size 为字节数（-1 表示未知）。
	UploadTranscodeSegment(ctx context.Context, fileID, quality, segName string, r io.Reader, size int64) error

	// UploadTranscodePlaylist 上传 m3u8 播放列表到缓存。
	// 转码完成后调用，标志该画质转码产物已完整可用。
	UploadTranscodePlaylist(ctx context.Context, fileID, quality string, m3u8Data []byte) error

	// GetTranscodeSegment 下载单个 ts 切片内容。
	// 调用方需 defer Close 返回的 ReadCloser。
	GetTranscodeSegment(ctx context.Context, fileID, quality, segName string) (io.ReadCloser, error)

	// GetTranscodePlaylist 下载 m3u8 播放列表内容。
	// 调用方需 defer Close 返回的 ReadCloser。
	GetTranscodePlaylist(ctx context.Context, fileID, quality string) (io.ReadCloser, error)

	// TranscodeExists 检查指定画质的转码产物是否已完整存在（以 m3u8 是否存在为准）。
	TranscodeExists(ctx context.Context, fileID, quality string) (bool, error)

	// DeleteTranscode 删除指定画质的全部转码产物（m3u8 + 所有 ts 切片）。
	DeleteTranscode(ctx context.Context, fileID, quality string) error

	// ListTranscodeByPrefix 按前缀列举转码产物对象（用于缓存清理 worker 的 LRU 策略）。
	ListTranscodeByPrefix(ctx context.Context, prefix string) ([]TranscodeObjectInfo, error)

	// PresignedTranscodeURL 生成转码产物的 presigned GET URL（前端直连 OSS 播放，省服务器流量）。
	// segName 为空时返回 m3u8 的 URL，否则返回对应 ts 切片的 URL。
	PresignedTranscodeURL(ctx context.Context, fileID, quality, segName string, expiry time.Duration) (string, error)
}

// TranscodeObjectInfo 描述转码产物对象的基本信息（用于 LRU 清理决策）。
type TranscodeObjectInfo struct {
	Key          string    // OSS 对象 key
	FileID       string    // 文件 ID（从 key 解析）
	Quality      string    // 画质（从 key 解析）
	LastModified time.Time // 最后修改时间（用于 LRU 判断）
	Size         int64     // 字节大小
}

// HLSPlaylistKey 返回 m3u8 播放列表的 OSS 对象 key。
// 规则：transcoded/{fileID}/{quality}/playlist.m3u8
func HLSPlaylistKey(fileID, quality string) string {
	return fmt.Sprintf("transcoded/%s/%s/playlist.m3u8", fileID, quality)
}

// HLSSegmentKey 返回单个 ts 切片的 OSS 对象 key。
// 规则：transcoded/{fileID}/{quality}/{segName}
// segName 形如 "seg_00001.ts"
func HLSSegmentKey(fileID, quality, segName string) string {
	return fmt.Sprintf("transcoded/%s/%s/%s", fileID, quality, segName)
}

// HLSTranscodePrefix 返回指定画质转码产物的 OSS 前缀（用于 ListObjects/DeleteObjects）。
// 规则：transcoded/{fileID}/{quality}/
func HLSTranscodePrefix(fileID, quality string) string {
	return fmt.Sprintf("transcoded/%s/%s/", fileID, quality)
}

// HLSTranscodeRootPrefix 返回所有转码产物的根前缀（用于清理 worker 全量扫描）。
func HLSTranscodeRootPrefix() string {
	return "transcoded/"
}
