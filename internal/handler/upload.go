package handler

import (
	"net/http"
	"strings"

	"filesync/internal/model"
	"filesync/internal/storage"
	"filesync/internal/store"
)

// Pre-defined conflict response types to avoid map[string]interface{} allocations.
type conflictResponse struct {
	Conflict   bool                    `json:"conflict"`
	Message    string                  `json:"message"`
	Strategies []string                `json:"strategies"`
	Existing   *model.FileInfoResponse `json:"existing,omitempty"`
}

// UploadHandler handles chunked upload operations
type UploadHandler struct {
	db       *store.DB
	storage  storage.Storage            // Router，用于读取（FileSize/HashFile 读取型）
	storages map[string]storage.Storage // 写入型链路按 storageType 选具体后端
	redis    *store.RedisCache          // optional Redis cache for high concurrency
	initSem  chan struct{}              // InitUpload 并发限流信号量（容量=最大并发数，nil=不限流）
}

// backendFor 返回指定 storageType 的具体写入后端（混合存储）；未知/未配置时回退 local。
func (h *UploadHandler) backendFor(t string) storage.Storage {
	if b, ok := h.storages[t]; ok && b != nil {
		return b
	}
	return h.storages["local"]
}

// NewUploadHandler creates a new upload handler (SQLite-only path)
func NewUploadHandler(db *store.DB, s storage.Storage, storages map[string]storage.Storage) *UploadHandler {
	return &UploadHandler{db: db, storage: s, storages: storages}
}

// NewUploadHandlerWithRedis creates a new upload handler with Redis caching enabled.
// initMaxConcurrency 控制 InitUpload 最大并发数（bench 实测最优区间 500-1000，默认 1000），
// 超过时返回 429 避免高并发下 P99 飙升。
func NewUploadHandlerWithRedis(db *store.DB, s storage.Storage, storages map[string]storage.Storage, rc *store.RedisCache) *UploadHandler {
	return &UploadHandler{
		db:       db,
		storage:  s,
		storages: storages,
		redis:    rc,
		initSem:  make(chan struct{}, 1000),
	}
}

// ServeHTTP routes upload-related requests
func (h *UploadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS 由全局 CORS 中间件统一处理（支持 credentials），此处不再设置局部 CORS 头

	path := strings.TrimPrefix(r.URL.Path, "/api/upload")
	path = strings.TrimSuffix(path, "/")

	switch path {
	case "/init":
		h.InitUpload(w, r)
	case "/chunk":
		h.UploadChunk(w, r)
	case "/status":
		h.GetUploadStatus(w, r)
	case "/complete":
		h.CompleteUpload(w, r)
	case "/check":
		h.CheckUpload(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}
