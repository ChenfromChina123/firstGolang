package storage

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// sessionDirCache tracks which session chunk directories have been created.
// Avoids redundant os.MkdirAll syscalls (saves ~2 syscalls per chunk write).
var sessionDirCache sync.Map

// LocalStorage stores files on the local filesystem
type LocalStorage struct {
	basePath   string
	asyncTasks sync.WaitGroup
}

// chunkWriteJob is a single async chunk write.
type chunkWriteJob struct {
	basePath   string
	sessionID  string
	chunkIndex int
	data       []byte
	wg         *sync.WaitGroup
}

const asyncWorkerCount = 8
const asyncJobQueueSize = 5000

var asyncChunkQueue chan *chunkWriteJob
var asyncStartOnce sync.Once
var asyncDropCount int64

// startAsyncWorkers initializes the async disk writer goroutine pool.
func startAsyncWorkers() {
	asyncStartOnce.Do(func() {
		asyncChunkQueue = make(chan *chunkWriteJob, asyncJobQueueSize)
		for i := 0; i < asyncWorkerCount; i++ {
			go func(workerID int) {
				for job := range asyncChunkQueue {
					_, err := writeChunkToDisk(job.basePath, job.sessionID, job.chunkIndex, job.data)
					if err != nil {
						log.Printf("[AsyncDisk] Worker-%d write failed %s/%d: %v", workerID, job.sessionID, job.chunkIndex, err)
					}
					job.wg.Done()
				}
			}(i)
		}
		log.Printf("[AsyncDisk] Worker pool started (%d workers, queue=%d)", asyncWorkerCount, asyncJobQueueSize)
	})
}

func ensureChunkDir(basePath, sessionID string) (string, error) {
	chunkDir := filepath.Join(basePath, "_chunks", sessionID)
	_, loaded := sessionDirCache.LoadOrStore(chunkDir, struct{}{})
	if !loaded {
		// First time seeing this session — create directory
		if err := os.MkdirAll(chunkDir, 0755); err != nil {
			sessionDirCache.Delete(chunkDir)
			return "", fmt.Errorf("create chunk dir: %w", err)
		}
	}
	return chunkDir, nil
}

func writeChunkToDisk(basePath, sessionID string, chunkIndex int, data []byte) (int64, error) {
	chunkDir, err := ensureChunkDir(basePath, sessionID)
	if err != nil {
		return 0, err
	}
	chunkPath := filepath.Join(chunkDir, fmt.Sprintf("chunk_%06d", chunkIndex))
	// Use os.WriteFile for atomic write (no partial files)
	if err := os.WriteFile(chunkPath, data, 0644); err != nil {
		return 0, fmt.Errorf("write chunk file: %w", err)
	}
	return int64(len(data)), nil
}

// NewLocal creates a new local storage backend
func NewLocal(basePath string) (*LocalStorage, error) {
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}
	startAsyncWorkers()
	return &LocalStorage{basePath: basePath}, nil
}

func (s *LocalStorage) BasePath() string {
	return s.basePath
}

func (s *LocalStorage) SaveChunk(sessionID string, chunkIndex int, data io.Reader) (int64, error) {
	chunkDir := tempDir(s.basePath, sessionID)
	if err := os.MkdirAll(chunkDir, 0755); err != nil {
		return 0, fmt.Errorf("create chunk dir: %w", err)
	}

	chunkPath := filepath.Join(chunkDir, fmt.Sprintf("chunk_%06d", chunkIndex))
	f, err := os.Create(chunkPath)
	if err != nil {
		return 0, fmt.Errorf("create chunk file: %w", err)
	}
	defer f.Close()

	return io.Copy(f, data)
}

// SaveChunkAsync enqueues a chunk for async disk write via goroutine pool.
// Returns immediately; the chunk is written in the background.
func (s *LocalStorage) SaveChunkAsync(sessionID string, chunkIndex int, data []byte) {
	s.asyncTasks.Add(1)
	select {
	case asyncChunkQueue <- &chunkWriteJob{
		basePath:   s.basePath,
		sessionID:  sessionID,
		chunkIndex: chunkIndex,
		data:       data,
		wg:         &s.asyncTasks,
	}:
		// queued successfully
	default:
		s.asyncTasks.Done()
		dropped := atomic.AddInt64(&asyncDropCount, 1)
		if dropped%100 == 1 {
			log.Printf("[AsyncDisk] Queue full! Dropped %d chunks total", dropped)
		}
		// Fall back to sync write
		writeChunkToDisk(s.basePath, sessionID, chunkIndex, data)
	}
}

// WaitAsync blocks until all pending async chunk writes complete.
func (s *LocalStorage) WaitAsync() {
	s.asyncTasks.Wait()
}

func (s *LocalStorage) ReadChunk(sessionID string, chunkIndex int) (io.ReadCloser, error) {
	chunkPath := filepath.Join(tempDir(s.basePath, sessionID), fmt.Sprintf("chunk_%06d", chunkIndex))
	f, err := os.Open(chunkPath)
	if err != nil {
		return nil, fmt.Errorf("open chunk: %w", err)
	}
	return f, nil
}

func (s *LocalStorage) AssembleFile(sessionID string, fileID string, filename string, totalChunks int) (string, error) {
	return s.assembleFile(sessionID, fileID, filename, totalChunks, nil)
}

// AssembleFileWithHash merges all chunks and computes SHA256 simultaneously.
// Uses io.MultiWriter to write file + hash in a single pass (saves 1x full file read).
func (s *LocalStorage) AssembleFileWithHash(sessionID string, fileID string, filename string, totalChunks int) (string, string, error) {
	hasher := sha256.New()
	path, err := s.assembleFile(sessionID, fileID, filename, totalChunks, hasher)
	if err != nil {
		return "", "", err
	}
	return path, hex.EncodeToString(hasher.Sum(nil)), nil
}

func (s *LocalStorage) assembleFile(sessionID, fileID, filename string, totalChunks int, hasher io.Writer) (string, error) {
	// 存储键用 fileID 生成分片路径，与 filename 解耦：
	// filename 仅用于取扩展名，磁盘文件名 = fileID（UUID）
	relPath := ShardPath(fileID, filename)
	destPath := filepath.Join(s.basePath, relPath)
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", fmt.Errorf("create parent dir: %w", err)
	}

	dst, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("create dest file: %w", err)
	}
	defer dst.Close()

	var writer io.Writer = dst
	if hasher != nil {
		writer = io.MultiWriter(dst, hasher)
	}
	bw := bufio.NewWriterSize(writer, 64*1024) // 64KB buffer

	for i := 0; i < totalChunks; i++ {
		chunkPath := filepath.Join(tempDir(s.basePath, sessionID), fmt.Sprintf("chunk_%06d", i))
		src, err := os.Open(chunkPath)
		if err != nil {
			return "", fmt.Errorf("open chunk %d: %w", i, err)
		}
		if _, err := io.Copy(bw, src); err != nil {
			src.Close()
			return "", fmt.Errorf("copy chunk %d: %w", i, err)
		}
		src.Close()
	}

	if err := bw.Flush(); err != nil {
		return "", fmt.Errorf("flush buffer: %w", err)
	}
	return destPath, nil
}

func (s *LocalStorage) DeleteTemp(sessionID string) error {
	chunkDir := tempDir(s.basePath, sessionID)
	sessionDirCache.Delete(chunkDir)
	return os.RemoveAll(chunkDir)
}

func (s *LocalStorage) ReadFile(path string, offset int64) (io.ReadCloser, error) {
	return readFile(path, offset)
}

func (s *LocalStorage) FileSize(path string) (int64, error) {
	return fileSize(path)
}

// HashFile returns the SHA256 hex hash of a file
func (s *LocalStorage) HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("hash open file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash compute: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DeleteFile removes a completed file from local storage.
// 递归删除空的父目录（路径枚举方案下保持目录整洁）。
func (s *LocalStorage) DeleteFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete file: %w", err)
	}
	// 递归清理空的父目录（直到 basePath）
	baseAbs := s.basePath
	for dir := filepath.Dir(path); dir != "" && dir != "." && dir != baseAbs && dir != "/"; dir = filepath.Dir(dir) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(dir); err != nil {
			break
		}
	}
	return nil
}

// CopyFile 复制本地文件到新路径（用于转存功能）。
// 使用 io.Copy 流式复制，避免大文件占用内存；失败时清理部分文件保证原子性。
func (s *LocalStorage) CopyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open src file: %w", err)
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("create dst dir: %w", err)
	}
	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create dst file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		os.Remove(dstPath) // 清理部分文件
		return fmt.Errorf("copy: %w", err)
	}
	return nil
}

// StoragePathFor 返回 Local 存储的完整绝对路径：basePath + ShardPath(fileID, filename)。
func (s *LocalStorage) StoragePathFor(fileID, filename string) string {
	return filepath.Join(s.basePath, ShardPath(fileID, filename))
}

// ComputeFileHash returns the SHA256 hex of a file
// Deprecated: Use Storage.HashFile() method instead
func ComputeFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
