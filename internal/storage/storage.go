package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"time"
)

// Storage defines the interface for file storage backends
type Storage interface {
	// SaveChunk stores a single chunk
	SaveChunk(sessionID string, chunkIndex int, data io.Reader) (int64, error)
	// ReadChunk reads a single chunk
	ReadChunk(sessionID string, chunkIndex int) (io.ReadCloser, error)
	// AssembleFile merges all chunks into the final file, returns the storage path.
	// fileID 用于生成 UUID 分片存储键（与 filename 解耦），filename 仅用于取扩展名。
	AssembleFile(sessionID string, fileID string, filename string, totalChunks int) (string, error)
	// DeleteTemp cleans up temporary chunk files
	DeleteTemp(sessionID string) error
	// ReadFile reads from the completed file at an optional offset
	ReadFile(path string, offset int64) (io.ReadCloser, error)
	// FileSize returns the size of a completed file
	FileSize(path string) (int64, error)
	// BasePath returns the storage base directory
	BasePath() string
	// HashFile returns the SHA256 hex hash of a file
	HashFile(path string) (string, error)
	// DeleteFile removes a completed file from storage
	DeleteFile(path string) error
	// CopyFile 复制已完成文件到新路径（用于转存功能）。
	// srcPath 为源文件的 storage_path，dstPath 为目标 storage 键。
	// 实现应保证原子性：失败时不留下部分文件。
	CopyFile(srcPath, dstPath string) error
	// StoragePathFor 根据 fileID 和 filename 构造完整的 storage_path。
	// Local 实现返回 basePath + ShardPath 的绝对路径；S3 实现返回相对对象键。
	// 用于转存功能生成新文件的 storage_path。
	StoragePathFor(fileID, filename string) string
}

// AsyncStorager is an optional interface for backends that support async writes.
type AsyncStorager interface {
	Storage
	// SaveChunkAsync enqueues a chunk for async background write. Returns immediately.
	SaveChunkAsync(sessionID string, chunkIndex int, data []byte)
	// WaitAsync blocks until all pending async writes complete.
	WaitAsync()
}

// HashAssembler is an optional interface for backends that can compute hash during assembly.
// Avoids reading the assembled file a second time for hash computation.
type HashAssembler interface {
	// AssembleFileWithHash merges all chunks and computes SHA256 simultaneously.
	// Returns (storagePath, hash, error). fileID 用于生成 UUID 分片存储键。
	AssembleFileWithHash(sessionID string, fileID string, filename string, totalChunks int) (string, string, error)
}

// PresignedStorage 是支持 presigned URL 直传/直连的可选接口。
// 后端实现此接口后，上传/下载链路自动启用客户端直连 OSS，数据不经过应用服务器。
type PresignedStorage interface {
	// GeneratePresignedPutURL 生成单个对象的 presigned PUT URL（小文件直传或大文件分片直传）。
	GeneratePresignedPutURL(ctx context.Context, objectKey string, expiry time.Duration) (string, error)
	// GeneratePresignedGetURL 生成 presigned GET URL（直连下载），可带 response-header 覆盖。
	GeneratePresignedGetURL(ctx context.Context, objectKey string, expiry time.Duration, reqParams url.Values) (string, error)
	// ComposeFile 将多个分片对象合并为最终文件对象（服务端 UploadPartCopy，数据不经应用服务器）。
	// 每个分片必须 >= 5MB（最后一片除外），调用方需保证 partSize 计算正确。
	ComposeFile(sessionID string, fileID string, filename string, totalParts int) (string, error)
	// ListParts 列举已上传的分片编号（断点续传用）。
	ListParts(sessionID string) ([]int, error)
	// DeleteParts 清理指定 session 的所有分片对象。
	DeleteParts(sessionID string) error
	// StatObject 获取对象元数据（大小等），用于验证上传完成。
	StatObject(objectKey string) (int64, error)
}

// Presigned URL 相关常量
const (
	// MinPartSize S3/OSS 最小分片大小（ComposeObject 要求每个源对象 >= 5MB，最后一片除外）
	MinPartSize = 5 * 1024 * 1024
	// MaxPartCount S3/OSS 最大分片数
	MaxPartCount = 10000
	// PresignedExpiry presigned URL 默认有效期
	PresignedExpiry = 1 * time.Hour
)

// CalculatePartSize 根据文件大小计算每片大小，确保每片 >= 5MB 且分片数 <= 10000。
// 小文件（<5MB）返回 (fileSize, 1)，用单片直传。
func CalculatePartSize(fileSize int64) (partSize int64, partCount int) {
	if fileSize < MinPartSize {
		return fileSize, 1
	}
	partSize = MinPartSize
	partCount = int((fileSize + partSize - 1) / partSize)
	if partCount > MaxPartCount {
		partSize = (fileSize + int64(MaxPartCount) - 1) / int64(MaxPartCount)
		partCount = int((fileSize + partSize - 1) / partSize)
	}
	return partSize, partCount
}

// ShardPath 根据 fileID 生成分片存储相对路径。
// 格式：<fileID前2字符>/<fileID第3-4字符>/<fileID>.<ext>
// 例：fileID="abcdef1234567890", filename="report.pdf" → "ab/cd/abcdef1234567890.pdf"
// 这种两级分片避免单目录文件过多（每目录最多 256*256=65536 个子目录），
// 是 S3/OSS/七牛等对象存储的主流做法（content-addressable storage 的简化版）。
// 注意：使用 path.Join（POSIX 风格）确保返回 `/` 分隔符，兼容 S3/OSS 对象键要求。
// LocalStorage 通过 filepath.Join 拼接时会自动转换为 OS 本地分隔符。
func ShardPath(fileID, filename string) string {
	if len(fileID) < 4 {
		// 兼容异常 fileID：直接用 fileID 作为文件名，不分片
		ext := filepath.Ext(filename)
		return fileID + ext
	}
	ext := filepath.Ext(filename)
	return path.Join(fileID[:2], fileID[2:4], fileID+ext)
}

// tempDir returns the temporary directory for chunk storage
func tempDir(basePath, sessionID string) string {
	return filepath.Join(basePath, "_chunks", sessionID)
}

func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat file: %w", err)
	}
	return fi.Size(), nil
}

func readFile(path string, offset int64) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, fmt.Errorf("seek: %w", err)
		}
	}
	return f, nil
}
