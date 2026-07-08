package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

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
// Returns the S3 object key of the assembled file (用 fileID 分片命名，与 filename 解耦)。
func (s *S3Storage) AssembleFile(sessionID string, fileID string, filename string, totalChunks int) (string, error) {
	ctx := context.Background()

	// S3 object key 用 fileID 分片命名（与 local 存储一致）
	objectKey := ShardPath(fileID, filename)
	log.Printf("[S3] Assembling: session=%s fileID=%s key=%s chunks=%d", sessionID, fileID, objectKey, totalChunks)

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

	_, err = s.client.PutObject(ctx, s.bucket, objectKey, tmpFile, -1, minio.PutObjectOptions{})
	tmpFile.Close()
	if err != nil {
		return "", fmt.Errorf("put assembled file: %w", err)
	}

	log.Printf("[S3] File assembled: key=%s", objectKey)
	return objectKey, nil
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

// CopyFile 在 S3 同 bucket 内服务端复制对象（用于转存功能）。
// 使用 minio CopyObject，数据不经过本地，适合大文件。
func (s *S3Storage) CopyFile(srcPath, dstPath string) error {
	ctx := context.Background()
	src := minio.CopySrcOptions{Bucket: s.bucket, Object: srcPath}
	dst := minio.CopyDestOptions{Bucket: s.bucket, Object: dstPath}
	if _, err := s.client.CopyObject(ctx, dst, src); err != nil {
		return fmt.Errorf("s3 copy object %s -> %s: %w", srcPath, dstPath, err)
	}
	return nil
}

// StoragePathFor 返回 S3 存储的对象键：ShardPath(fileID, filename)。
// S3 的 storage_path 是相对 bucket 的对象键，无需拼接 basePath。
func (s *S3Storage) StoragePathFor(fileID, filename string) string {
	return ShardPath(fileID, filename)
}

// === Presigned URL 直传/直连方法 ===

// partKey 构建 presigned 分片上传的 S3 对象 key。
// 格式：_parts/{sessionID}/part_{N}（6位补零，便于排序）
func partKey(sessionID string, partIndex int) string {
	return fmt.Sprintf("_parts/%s/part_%06d", sessionID, partIndex)
}

// partPrefix 返回 session 的分片对象前缀（用于 ListObjects）。
func partPrefix(sessionID string) string {
	return fmt.Sprintf("_parts/%s/", sessionID)
}

// GeneratePresignedPutURL 生成单个对象的 presigned PUT URL。
// 客户端用此 URL 直接 PUT 数据到 OSS，不经过应用服务器。
func (s *S3Storage) GeneratePresignedPutURL(ctx context.Context, objectKey string, expiry time.Duration) (string, error) {
	u, err := s.client.PresignedPutObject(ctx, s.bucket, objectKey, expiry)
	if err != nil {
		return "", fmt.Errorf("presigned put %s: %w", objectKey, err)
	}
	return u.String(), nil
}

// GeneratePresignedGetURL 生成 presigned GET URL，可覆盖 response headers。
// reqParams 可包含 response-content-disposition 等参数强制下载行为。
func (s *S3Storage) GeneratePresignedGetURL(ctx context.Context, objectKey string, expiry time.Duration, reqParams url.Values) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, s.bucket, objectKey, expiry, reqParams)
	if err != nil {
		return "", fmt.Errorf("presigned get %s: %w", objectKey, err)
	}
	return u.String(), nil
}

// ComposeFile 调用 ComposeObject 将所有分片对象合并为最终文件对象。
// 基于 S3 UploadPartCopy API，数据在 OSS 服务端复制，不经过应用服务器。
// 每个分片必须 >= 5MB（最后一片除外），调用方需保证 partSize 计算正确。
// 返回最终文件的对象 key（ShardPath 格式）。
func (s *S3Storage) ComposeFile(sessionID string, fileID string, filename string, totalParts int) (string, error) {
	ctx := context.Background()
	objectKey := ShardPath(fileID, filename)
	log.Printf("[S3] Composing: session=%s fileID=%s key=%s parts=%d", sessionID, fileID, objectKey, totalParts)

	var srcs []minio.CopySrcOptions
	for i := 0; i < totalParts; i++ {
		srcs = append(srcs, minio.CopySrcOptions{
			Bucket: s.bucket,
			Object: partKey(sessionID, i),
		})
	}

	dst := minio.CopyDestOptions{Bucket: s.bucket, Object: objectKey}
	if _, err := s.client.ComposeObject(ctx, dst, srcs...); err != nil {
		return "", fmt.Errorf("compose object %s: %w", objectKey, err)
	}

	log.Printf("[S3] Composed: session=%s key=%s parts=%d", sessionID, objectKey, totalParts)
	return objectKey, nil
}

// ListParts 列举已上传的分片编号（断点续传用）。
// 通过 ListObjects 枚举 _parts/{sessionID}/ 前缀下的对象。
func (s *S3Storage) ListParts(sessionID string) ([]int, error) {
	ctx := context.Background()
	prefix := partPrefix(sessionID)

	objectsCh := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})

	var parts []int
	for obj := range objectsCh {
		if obj.Err != nil {
			return nil, fmt.Errorf("list parts: %w", obj.Err)
		}
		// 从 key 中提取 part number：_parts/{sessionID}/part_000005 → 5
		var partNum int
		if _, err := fmt.Sscanf(obj.Key, prefix+"part_%06d", &partNum); err == nil {
			parts = append(parts, partNum)
		}
	}
	return parts, nil
}

// DeleteParts 批量删除 session 的所有分片对象。
// 在 ComposeFile 成功后调用，清理临时分片。
func (s *S3Storage) DeleteParts(sessionID string) error {
	ctx := context.Background()
	prefix := partPrefix(sessionID)

	objectsCh := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})

	removeCh := make(chan minio.ObjectInfo, 100)
	go func() {
		defer close(removeCh)
		for obj := range objectsCh {
			if obj.Err != nil {
				log.Printf("[S3] list parts for delete error: %v", obj.Err)
				continue
			}
			removeCh <- obj
		}
	}()

	errCh := s.client.RemoveObjects(ctx, s.bucket, removeCh, minio.RemoveObjectsOptions{})
	for err := range errCh {
		log.Printf("[S3] delete part error for %s: %v", err.ObjectName, err.Err)
	}

	log.Printf("[S3] Parts cleaned: session=%s", sessionID)
	return nil
}

// StatObject 获取对象元数据（大小等），用于验证 presigned 上传完成。
func (s *S3Storage) StatObject(objectKey string) (int64, error) {
	ctx := context.Background()
	info, err := s.client.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{})
	if err != nil {
		return 0, fmt.Errorf("stat object %s: %w", objectKey, err)
	}
	return info.Size, nil
}

// === TranscodeCacheStore 接口实现 ===
// 以下方法实现 TranscodeCacheStore 接口，用于视频 HLS 转码产物的 OSS 缓存。
// key 规则见 transcode_cache.go 的 HLSPlaylistKey/HLSSegmentKey/HLSTranscodePrefix。

// UploadTranscodeSegment 上传单个 HLS ts 切片到 OSS。
// 实现 TranscodeCacheStore 接口。size=-1 时由 minio 自动分片上传。
func (s *S3Storage) UploadTranscodeSegment(ctx context.Context, fileID, quality, segName string, r io.Reader, size int64) error {
	key := HLSSegmentKey(fileID, quality, segName)
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{})
	if err != nil {
		return fmt.Errorf("s3 upload transcode segment %s: %w", key, err)
	}
	return nil
}

// UploadTranscodePlaylist 上传 m3u8 播放列表到 OSS。
// 实现 TranscodeCacheStore 接口。上传 m3u8 标志转码产物已完整可用。
// ContentType 设为 application/vnd.apple.mpegurl 确保浏览器识别为 HLS 播放列表。
func (s *S3Storage) UploadTranscodePlaylist(ctx context.Context, fileID, quality string, m3u8Data []byte) error {
	key := HLSPlaylistKey(fileID, quality)
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(m3u8Data), int64(len(m3u8Data)), minio.PutObjectOptions{
		ContentType: "application/vnd.apple.mpegurl",
	})
	if err != nil {
		return fmt.Errorf("s3 upload transcode playlist %s: %w", key, err)
	}
	return nil
}

// GetTranscodeSegment 下载单个 ts 切片。
// 实现 TranscodeCacheStore 接口。调用方需 defer Close 返回的 ReadCloser。
func (s *S3Storage) GetTranscodeSegment(ctx context.Context, fileID, quality, segName string) (io.ReadCloser, error) {
	key := HLSSegmentKey(fileID, quality, segName)
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3 get transcode segment %s: %w", key, err)
	}
	return obj, nil
}

// GetTranscodePlaylist 下载 m3u8 播放列表。
// 实现 TranscodeCacheStore 接口。调用方需 defer Close 返回的 ReadCloser。
func (s *S3Storage) GetTranscodePlaylist(ctx context.Context, fileID, quality string) (io.ReadCloser, error) {
	key := HLSPlaylistKey(fileID, quality)
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3 get transcode playlist %s: %w", key, err)
	}
	return obj, nil
}

// TranscodeExists 检查指定画质的转码产物是否已完整存在（以 m3u8 是否存在为准）。
// 实现 TranscodeCacheStore 接口。NoSuchKey 视为不存在，其他错误向上抛。
func (s *S3Storage) TranscodeExists(ctx context.Context, fileID, quality string) (bool, error) {
	key := HLSPlaylistKey(fileID, quality)
	_, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		resp, ok := err.(minio.ErrorResponse)
		if ok && resp.Code == "NoSuchKey" {
			return false, nil
		}
		return false, fmt.Errorf("s3 stat transcode %s: %w", key, err)
	}
	return true, nil
}

// DeleteTranscode 删除指定画质的全部转码产物（m3u8 + 所有 ts 切片）。
// 实现 TranscodeCacheStore 接口。通过 ListObjects + RemoveObjects 批量删除。
func (s *S3Storage) DeleteTranscode(ctx context.Context, fileID, quality string) error {
	prefix := HLSTranscodePrefix(fileID, quality)
	objectsCh := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})
	removeCh := make(chan minio.ObjectInfo, 100)
	go func() {
		defer close(removeCh)
		for obj := range objectsCh {
			if obj.Err != nil {
				log.Printf("[S3] list transcode for delete error: %v", obj.Err)
				continue
			}
			removeCh <- obj
		}
	}()
	errCh := s.client.RemoveObjects(ctx, s.bucket, removeCh, minio.RemoveObjectsOptions{})
	for err := range errCh {
		log.Printf("[S3] delete transcode error for %s: %v", err.ObjectName, err.Err)
	}
	return nil
}

// ListTranscodeByPrefix 按前缀列举转码产物对象（用于清理 worker LRU 策略）。
// 实现 TranscodeCacheStore 接口。从 key 解析 FileID 和 Quality。
func (s *S3Storage) ListTranscodeByPrefix(ctx context.Context, prefix string) ([]TranscodeObjectInfo, error) {
	objectsCh := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})
	var result []TranscodeObjectInfo
	for obj := range objectsCh {
		if obj.Err != nil {
			return nil, fmt.Errorf("s3 list transcode %s: %w", prefix, obj.Err)
		}
		info := TranscodeObjectInfo{
			Key:          obj.Key,
			LastModified: obj.LastModified,
			Size:         obj.Size,
		}
		// 从 key 解析 fileID 和 quality：transcoded/{fileID}/{quality}/xxx
		// 解析失败不报错，保留 Key 供清理 worker 使用
		trimmed := strings.TrimPrefix(obj.Key, "transcoded/")
		parts := strings.SplitN(trimmed, "/", 3)
		if len(parts) >= 2 {
			info.FileID = parts[0]
			info.Quality = parts[1]
		}
		result = append(result, info)
	}
	return result, nil
}

// PresignedTranscodeURL 生成转码产物的 presigned GET URL。
// 实现 TranscodeCacheStore 接口。segName 为空返回 m3u8 URL，否则返回 ts 切片 URL。
func (s *S3Storage) PresignedTranscodeURL(ctx context.Context, fileID, quality, segName string, expiry time.Duration) (string, error) {
	var key string
	if segName == "" {
		key = HLSPlaylistKey(fileID, quality)
	} else {
		key = HLSSegmentKey(fileID, quality, segName)
	}
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, expiry, nil)
	if err != nil {
		return "", fmt.Errorf("s3 presigned transcode %s: %w", key, err)
	}
	return u.String(), nil
}
