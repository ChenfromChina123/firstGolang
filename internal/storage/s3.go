package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// S3Config holds S3 connection parameters
type S3Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// S3Storage stores files on S3-compatible object storage
// Stages chunks locally, then uploads assembled file to S3 via a pluggable hook
type S3Storage struct {
	localBase string
	config    S3Config
}

// NewS3 creates a new S3 storage backend
func NewS3(basePath string, config S3Config) (*S3Storage, error) {
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}
	return &S3Storage{localBase: basePath, config: config}, nil
}

func (s *S3Storage) BasePath() string {
	return s.localBase
}

func (s *S3Storage) SaveChunk(sessionID string, chunkIndex int, data io.Reader) (int64, error) {
	chunkDir := filepath.Join(s.localBase, "_chunks", sessionID)
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

func (s *S3Storage) ReadChunk(sessionID string, chunkIndex int) (io.ReadCloser, error) {
	chunkPath := filepath.Join(s.localBase, "_chunks", sessionID, fmt.Sprintf("chunk_%06d", chunkIndex))
	f, err := os.Open(chunkPath)
	if err != nil {
		return nil, fmt.Errorf("open chunk: %w", err)
	}
	return f, nil
}

func (s *S3Storage) AssembleFile(sessionID string, filename string, totalChunks int) (string, error) {
	destPath := filepath.Join(s.localBase, filename)
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", err
	}

	dst, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	defer dst.Close()

	for i := 0; i < totalChunks; i++ {
		chunkPath := filepath.Join(s.localBase, "_chunks", sessionID, fmt.Sprintf("chunk_%06d", i))
		src, err := os.Open(chunkPath)
		if err != nil {
			return "", fmt.Errorf("open chunk %d: %w", i, err)
		}
		_, err = io.Copy(dst, src)
		src.Close()
		if err != nil {
			return "", fmt.Errorf("copy chunk %d: %w", i, err)
		}
	}

	// Notify S3 upload hook
	if s3UploadHook != nil {
		if err := s3UploadHook(context.Background(), s.config, filename, destPath); err != nil {
			fmt.Printf("[S3] Upload warning: %v\n", err)
		}
	}

	return destPath, nil
}

func (s *S3Storage) DeleteTemp(sessionID string) error {
	chunkDir := filepath.Join(s.localBase, "_chunks", sessionID)
	os.RemoveAll(chunkDir)
	return nil
}

func (s *S3Storage) ReadFile(path string, offset int64) (io.ReadCloser, error) {
	return readFile(path, offset)
}

func (s *S3Storage) FileSize(path string) (int64, error) {
	return fileSize(path)
}

// S3UploadFunc is a pluggable upload function for S3
type S3UploadFunc func(ctx context.Context, config S3Config, key, filePath string) error

var s3UploadHook S3UploadFunc

// RegisterS3Upload registers an S3 upload implementation
func RegisterS3Upload(fn S3UploadFunc) {
	s3UploadHook = fn
}

func init() {
	// Default no-op
	s3UploadHook = func(ctx context.Context, config S3Config, key, filePath string) error {
		fmt.Printf("[S3] Upload ready: %s -> bucket=%s key=%s\n", filePath, config.Bucket, key)
		fmt.Printf("[S3] To enable real upload import AWS SDK and call RegisterS3Upload.\n")
		return nil
	}
}
