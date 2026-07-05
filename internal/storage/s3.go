package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
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

// S3Storage stores files directly on S3-compatible object storage.
// Chunks are stored as individual objects. During assembly, chunks are
// downloaded, concatenated locally, and the complete file is uploaded to S3.
type S3Storage struct {
	client *minio.Client
	bucket string
}

// NewS3 creates a new S3 storage backend connected to the given endpoint.
func NewS3(config S3Config) (*S3Storage, error) {
	client, err := minio.New(config.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(config.AccessKey, config.SecretKey, ""),
		Secure: config.UseSSL,
		Region: config.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}

	return &S3Storage{
		client: client,
		bucket: config.Bucket,
	}, nil
}

func (s *S3Storage) BasePath() string {
	return s.bucket
}

// chunkKey builds the S3 object key for a chunk
func chunkKey(sessionID string, chunkIndex int) string {
	return fmt.Sprintf("_chunks/%s/chunk_%06d", sessionID, chunkIndex)
}

// SaveChunk uploads a single chunk directly to S3 as an independent object.
func (s *S3Storage) SaveChunk(sessionID string, chunkIndex int, data io.Reader) (int64, error) {
	ctx := context.Background()
	key := chunkKey(sessionID, chunkIndex)

	info, err := s.client.PutObject(ctx, s.bucket, key, data, -1, minio.PutObjectOptions{})
	if err != nil {
		return 0, fmt.Errorf("s3 put chunk %d: %w", chunkIndex, err)
	}

	log.Printf("[S3] chunk %d saved: key=%s size=%d", chunkIndex, key, info.Size)
	return info.Size, nil
}

// ReadChunk downloads a single chunk from S3.
func (s *S3Storage) ReadChunk(sessionID string, chunkIndex int) (io.ReadCloser, error) {
	ctx := context.Background()
	key := chunkKey(sessionID, chunkIndex)

	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3 get chunk %d: %w", chunkIndex, err)
	}
	return obj, nil
}

// AssembleFile downloads all chunks from S3, assembles them locally, and uploads the
// complete file back to S3. This approach avoids S3's 5 MB minimum part size restriction
// that applies to ComposeObject and UploadPartCopy APIs.
// Returns the S3 object key of the assembled file.
func (s *S3Storage) AssembleFile(sessionID string, filename string, totalChunks int) (string, error) {
	ctx := context.Background()

	log.Printf("[S3] Assembling: session=%s filename=%s chunks=%d", sessionID, filename, totalChunks)

	// Create a local temp file to assemble chunks into
	tmpFile, err := os.CreateTemp("", "s3-assemble-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	// Download each chunk and append to temp file
	for i := 0; i < totalChunks; i++ {
		key := chunkKey(sessionID, i)
		obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
		if err != nil {
			tmpFile.Close()
			return "", fmt.Errorf("get chunk %d: %w", i, err)
		}

		written, err := io.Copy(tmpFile, obj)
		obj.Close()
		if err != nil {
			tmpFile.Close()
			return "", fmt.Errorf("copy chunk %d: %w", i, err)
		}
		_ = written
	}

	// Upload the assembled file to S3
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		tmpFile.Close()
		return "", fmt.Errorf("seek temp file: %w", err)
	}

	_, err = s.client.PutObject(ctx, s.bucket, filename, tmpFile, -1, minio.PutObjectOptions{})
	tmpFile.Close()
	if err != nil {
		return "", fmt.Errorf("put assembled file: %w", err)
	}

	log.Printf("[S3] File assembled: key=%s", filename)
	return filename, nil
}

// DeleteTemp lists and removes all objects under the session's chunk prefix.
func (s *S3Storage) DeleteTemp(sessionID string) error {
	ctx := context.Background()
	prefix := fmt.Sprintf("_chunks/%s/", sessionID)

	objectsCh := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})

	// Collect keys
	var keys []minio.ObjectInfo
	for obj := range objectsCh {
		if obj.Err != nil {
			log.Printf("[S3] list temp error: %v", obj.Err)
			continue
		}
		keys = append(keys, obj)
	}

	if len(keys) == 0 {
		return nil
	}

	// Batch remove
	removeCh := make(chan minio.ObjectInfo, len(keys))
	go func() {
		defer close(removeCh)
		for _, k := range keys {
			removeCh <- minio.ObjectInfo{Key: k.Key}
		}
	}()

	errCh := s.client.RemoveObjects(ctx, s.bucket, removeCh, minio.RemoveObjectsOptions{})
	for err := range errCh {
		log.Printf("[S3] delete temp error for %s: %v", err.ObjectName, err.Err)
	}

	log.Printf("[S3] Temp cleaned: session=%s objects=%d", sessionID, len(keys))
	return nil
}

// ReadFile downloads a file from S3. If offset > 0, uses HTTP Range request.
func (s *S3Storage) ReadFile(path string, offset int64) (io.ReadCloser, error) {
	ctx := context.Background()

	opts := minio.GetObjectOptions{}
	if offset > 0 {
		if err := opts.SetRange(offset, 0); err != nil {
			return nil, fmt.Errorf("set range: %w", err)
		}
	}

	obj, err := s.client.GetObject(ctx, s.bucket, path, opts)
	if err != nil {
		return nil, fmt.Errorf("s3 get object %s: %w", path, err)
	}
	return obj, nil
}

// FileSize returns the size of an object in S3.
func (s *S3Storage) FileSize(path string) (int64, error) {
	ctx := context.Background()

	info, err := s.client.StatObject(ctx, s.bucket, path, minio.StatObjectOptions{})
	if err != nil {
		return 0, fmt.Errorf("s3 stat object %s: %w", path, err)
	}
	return info.Size, nil
}

// HashFile streams the S3 object and computes its SHA256 hex hash.
func (s *S3Storage) HashFile(path string) (string, error) {
	ctx := context.Background()

	obj, err := s.client.GetObject(ctx, s.bucket, path, minio.GetObjectOptions{})
	if err != nil {
		return "", fmt.Errorf("s3 get object for hash: %w", err)
	}
	defer obj.Close()

	h := sha256.New()
	if _, err := io.Copy(h, obj); err != nil {
		return "", fmt.Errorf("hash compute: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DeleteFile removes a completed file object from S3.
func (s *S3Storage) DeleteFile(path string) error {
	ctx := context.Background()
	return s.client.RemoveObject(ctx, s.bucket, path, minio.RemoveObjectOptions{})
}
