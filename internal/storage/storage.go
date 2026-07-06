package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// ShardPath 根据 fileID 生成分片存储相对路径。
// 格式：<fileID前2字符>/<fileID第3-4字符>/<fileID>.<ext>
// 例：fileID="abcdef1234567890", filename="report.pdf" → "ab/cd/abcdef1234567890.pdf"
// 这种两级分片避免单目录文件过多（每目录最多 256*256=65536 个子目录），
// 是 S3/OSS/七牛等对象存储的主流做法（content-addressable storage 的简化版）。
func ShardPath(fileID, filename string) string {
	if len(fileID) < 4 {
		// 兼容异常 fileID：直接用 fileID 作为文件名，不分片
		ext := filepath.Ext(filename)
		return fileID + ext
	}
	ext := filepath.Ext(filename)
	return filepath.Join(fileID[:2], fileID[2:4], fileID+ext)
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
