package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// LocalStorage stores files on the local filesystem
type LocalStorage struct {
	basePath string
}

// NewLocal creates a new local storage backend
func NewLocal(basePath string) (*LocalStorage, error) {
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}
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

func (s *LocalStorage) ReadChunk(sessionID string, chunkIndex int) (io.ReadCloser, error) {
	chunkPath := filepath.Join(tempDir(s.basePath, sessionID), fmt.Sprintf("chunk_%06d", chunkIndex))
	f, err := os.Open(chunkPath)
	if err != nil {
		return nil, fmt.Errorf("open chunk: %w", err)
	}
	return f, nil
}

func (s *LocalStorage) AssembleFile(sessionID string, filename string, totalChunks int) (string, error) {
	destPath := filepath.Join(s.basePath, filename)

	// ensure parent dir exists
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", fmt.Errorf("create parent dir: %w", err)
	}

	dst, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("create dest file: %w", err)
	}
	defer dst.Close()

	for i := 0; i < totalChunks; i++ {
		chunkPath := filepath.Join(tempDir(s.basePath, sessionID), fmt.Sprintf("chunk_%06d", i))
		src, err := os.Open(chunkPath)
		if err != nil {
			return "", fmt.Errorf("open chunk %d: %w", i, err)
		}
		if _, err := io.Copy(dst, src); err != nil {
			src.Close()
			return "", fmt.Errorf("copy chunk %d: %w", i, err)
		}
		src.Close()
	}

	return destPath, nil
}

func (s *LocalStorage) DeleteTemp(sessionID string) error {
	chunkDir := tempDir(s.basePath, sessionID)
	return os.RemoveAll(chunkDir)
}

func (s *LocalStorage) ReadFile(path string, offset int64) (io.ReadCloser, error) {
	return readFile(path, offset)
}

func (s *LocalStorage) FileSize(path string) (int64, error) {
	return fileSize(path)
}

// ComputeFileHash returns the SHA256 hex of a file
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
