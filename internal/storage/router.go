package storage

import (
	"fmt"
	"io"
	"log"
	"strings"
)

// 存储后端路径前缀：混合模式下 storage_path 携带后端标识。
//   - "local:/abs/path" → LocalStorage（绝对路径）
//   - "s3:object/key"   → S3Storage（相对 bucket 的对象键）
//   - 无前缀（历史数据）→ 视为 local，向后兼容
const (
	LocalPrefix = "local:"
	S3Prefix    = "s3:"
)

// PrefixStoragePath 为原始 storage_path 加上对应后端前缀。
// 写入链路（上传/组装）在拿到后端返回的 raw path 后调用，使 DB 中记录的
// storage_path 自描述后端，读取链路据此路由。
func PrefixStoragePath(storageType, raw string) string {
	if storageType == "s3" {
		return S3Prefix + raw
	}
	return LocalPrefix + raw
}

// Router 按 storage_path 前缀分发读操作的 Storage 实现，用于下载/预览/分享/
// 删除等"读取型"链路。写入型链路（上传分片/组装/Mkdir）由调用方通过
// BackendFor(storageType) 选择具体后端，不经由此 Router 的写入方法。
type Router struct {
	local Storage
	s3    Storage
}

// NewRouter 创建读写路由器。local 必须非 nil；s3 可为 nil（未配置 OSS 时）。
func NewRouter(local, s3 Storage) *Router {
	return &Router{local: local, s3: s3}
}

// BackendFor 返回指定 storageType 的具体后端（用于写入链路）。
// 未知类型或未配置 s3 时安全回退到 local。
func (r *Router) BackendFor(t string) Storage {
	if t == "s3" && r.s3 != nil {
		return r.s3
	}
	return r.local
}

// backendAndPath 解析带前缀的 storage_path，返回对应后端与去前缀后的真实路径。
func (r *Router) backendAndPath(path string) (Storage, string) {
	switch {
	case strings.HasPrefix(path, S3Prefix):
		return r.s3, strings.TrimPrefix(path, S3Prefix)
	case strings.HasPrefix(path, LocalPrefix):
		return r.local, strings.TrimPrefix(path, LocalPrefix)
	default:
		// 历史数据：无前缀视为 local（绝对路径）
		return r.local, path
	}
}

// --- 读取型方法：按前缀路由 ---

func (r *Router) ReadFile(path string, offset int64) (io.ReadCloser, error) {
	b, p := r.backendAndPath(path)
	if b == nil {
		return nil, fmt.Errorf("storage backend unavailable for path %s", path)
	}
	return b.ReadFile(p, offset)
}

func (r *Router) FileSize(path string) (int64, error) {
	b, p := r.backendAndPath(path)
	if b == nil {
		return 0, fmt.Errorf("storage backend unavailable for path %s", path)
	}
	return b.FileSize(p)
}

func (r *Router) DeleteFile(path string) error {
	b, p := r.backendAndPath(path)
	if b == nil {
		return nil // 后端不可用则静默忽略（防止硬删除阻断 DB 清理）
	}
	return b.DeleteFile(p)
}

func (r *Router) HashFile(path string) (string, error) {
	b, p := r.backendAndPath(path)
	if b == nil {
		return "", fmt.Errorf("storage backend unavailable for path %s", path)
	}
	return b.HashFile(p)
}

func (r *Router) CopyFile(src, dst string) error {
	bs, sp := r.backendAndPath(src)
	bd, dp := r.backendAndPath(dst)
	if bs == nil || bd == nil || bs != bd {
		return fmt.Errorf("cross-backend copy unsupported (src=%s dst=%s)", src, dst)
	}
	return bs.CopyFile(sp, dp)
}

// BasePath 返回本地存储根目录。缩略图/转码/海报缓存必须落在本机磁盘，
// 因此统一返回 local 的 BasePath（与源文件所在后端无关）。
func (r *Router) BasePath() string {
	return r.local.BasePath()
}

// --- 写入型方法：占位实现，委托 local ---
// 正常业务不经由 Router 写入（上传/Mkdir 用 BackendFor 选具体后端），
// 此处仅为满足 Storage 接口并提供误用时的安全回退 + 告警日志。

func (r *Router) SaveChunk(sessionID string, chunkIndex int, data io.Reader) (int64, error) {
	log.Printf("[Router] WARN: SaveChunk called on Router, delegating to local")
	return r.local.SaveChunk(sessionID, chunkIndex, data)
}

func (r *Router) ReadChunk(sessionID string, chunkIndex int) (io.ReadCloser, error) {
	return r.local.ReadChunk(sessionID, chunkIndex)
}

func (r *Router) AssembleFile(sessionID string, fileID string, filename string, totalChunks int) (string, error) {
	return r.local.AssembleFile(sessionID, fileID, filename, totalChunks)
}

func (r *Router) DeleteTemp(sessionID string) error {
	return r.local.DeleteTemp(sessionID)
}

func (r *Router) StoragePathFor(fileID, filename string) string {
	return r.local.StoragePathFor(fileID, filename)
}
