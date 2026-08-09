package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/storage"
	"filesync/internal/store"
)

// s3TranscodeMaxSize s3 视频启用服务端转码的最大文件大小。
// 超过此大小的 s3 视频直接返回原文件流，避免下载大文件占用磁盘和带宽。
const s3TranscodeMaxSize = 200 * 1024 * 1024

// hlsSegNamePattern 合法的 HLS 切片文件名格式（防路径穿越）。
// 形如 seg_00001.ts，序号补零 5 位。
var hlsSegNamePattern = regexp.MustCompile(`^seg_\d{5}\.ts$`)

// PreviewHandler 处理文件预览请求：元数据查询、缩略图生成、原始内容流式返回。
// 第一阶段支持图片/PDF/文本/音频；视频海报与转码在后续阶段实现。
type PreviewHandler struct {
	db         *store.DB
	storage    storage.Storage
	baseURL    string                      // 应用基础 URL（用于构造完整 URL，预留）
	cacheStore storage.TranscodeCacheStore // OSS 转码缓存后端（nil 时走本地 MP4 fallback）
}

// NewPreviewHandler 创建预览 handler。
// cacheStore 为 OSS 转码缓存后端，nil 时视频转码走本地 MP4 fallback。
func NewPreviewHandler(db *store.DB, s storage.Storage, baseURL string, cacheStore storage.TranscodeCacheStore) *PreviewHandler {
	return &PreviewHandler{db: db, storage: s, baseURL: baseURL, cacheStore: cacheStore}
}

// previewMeta 是 /api/preview/{fileID} 返回的元数据结构。
type previewMeta struct {
	Type      string            `json:"type"`      // image|pdf|text|code|audio|video|office|unsupported
	Filename  string            `json:"filename"`  // 完整虚拟路径
	Size      int64             `json:"size"`      // 字节
	Supported bool              `json:"supported"` // 是否可预览
	URLs      map[string]string `json:"urls"`      // 各资源 URL
}

// ServeHTTP 路由分发：
//
//	GET /api/preview/{fileID}            → 元数据
//	GET /api/preview/{fileID}/thumb      → 图片缩略图（?size=small|medium|large）
//	GET /api/preview/{fileID}/poster     → 视频海报（第二阶段，501）
//	GET /api/preview/{fileID}/content    → 原始内容流（支持 Range 206）
//	GET /api/preview/{fileID}/transcode  → 视频转码（第三阶段，501）
func (h *PreviewHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// 解析路径：/api/preview/{fileID}[/{action}]
	path := strings.TrimPrefix(r.URL.Path, "/api/preview/")
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		http.Error(w, `{"error":"file_id required"}`, http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(path, "/", 2)
	fileID := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	if fileID == "" {
		http.Error(w, `{"error":"file_id required"}`, http.StatusBadRequest)
		return
	}

	switch action {
	case "":
		h.serveMeta(w, r, fileID)
	case "thumb":
		h.serveThumb(w, r, fileID)
	case "poster":
		h.servePoster(w, r, fileID)
	case "transcode":
		h.serveTranscode(w, r, fileID)
	case "transcode-status":
		h.serveTranscodeStatus(w, r, fileID)
	case "hls":
		h.serveHLS(w, r, fileID)
	case "archive":
		h.serveArchive(w, r, fileID)
	case "content":
		h.serveContent(w, r, fileID)
	default:
		http.Error(w, `{"error":"unknown action"}`, http.StatusBadRequest)
	}
}

// serveMeta 返回预览元数据 JSON。
// 包含文件类型、可预览性、各资源 URL（缩略图/原始内容）。
func (h *PreviewHandler) serveMeta(w http.ResponseWriter, r *http.Request, fileID string) {
	file, err := h.db.GetFile(fileID)
	if err != nil {
		log.Printf("[PREVIEW] meta: file not found id=%s err=%v", fileID, err)
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}
	if !h.checkPermission(w, r, file.Owner) {
		return
	}

	ftype := getFileType(file.Filename)
	base := "/api/preview/" + fileID
	meta := previewMeta{
		Type:      ftype,
		Filename:  file.Filename,
		Size:      file.Size,
		Supported: ftype != "unsupported",
		URLs:      map[string]string{"original": base + "/content"},
	}
	// 图片类型附加 3 档缩略图 URL
	if ftype == "image" {
		meta.URLs["thumb_small"] = base + "/thumb?size=small"
		meta.URLs["thumb_medium"] = base + "/thumb?size=medium"
		meta.URLs["thumb_large"] = base + "/thumb?size=large"
	}
	// 压缩包类型附加列表/提取 URL（前端 archive 渲染器调用）
	if ftype == "archive" {
		meta.URLs["archive_list"] = base + "/archive"
		meta.URLs["archive_extract"] = base + "/archive?path="
	}
	// 视频类型附加海报与三档画质 URL
	// hls_* = HLS 流式播放（边转边播，hls.js）；transcode_* = MP4 fallback
	if ftype == "video" {
		meta.URLs["poster"] = base + "/poster"
		meta.URLs["video_high"] = base + "/transcode?quality=high"
		meta.URLs["video_medium"] = base + "/transcode?quality=medium"
		meta.URLs["video_low"] = base + "/transcode?quality=low"
		meta.URLs["hls_high"] = base + "/hls?quality=high"
		meta.URLs["hls_medium"] = base + "/hls?quality=medium"
		meta.URLs["hls_low"] = base + "/hls?quality=low"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}

// serveThumb 返回图片缩略图。
// resolveLocalSource 返回可用于 imaging/ffmpeg 的本地文件路径。
//   - local 文件：去掉 "local:" 前缀得到绝对路径
//   - s3 文件：先从 OSS 下载到本地临时文件，返回临时路径；调用方需 defer cleanup 清理
func (h *PreviewHandler) resolveLocalSource(file *model.FileRecord) (string, func(), error) {
	if file.StorageType != "s3" {
		p := strings.TrimPrefix(file.StoragePath, storage.LocalPrefix)
		return p, func() {}, nil
	}
	// s3：下载到本地临时文件
	rc, err := h.storage.ReadFile(file.StoragePath, 0)
	if err != nil {
		return "", nil, err
	}
	defer rc.Close()
	tmp, err := os.CreateTemp("", "filesync-preview-*")
	if err != nil {
		return "", nil, err
	}
	if _, err := io.Copy(tmp, rc); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", nil, err
	}
	tmp.Close()
	return tmp.Name(), func() { os.Remove(tmp.Name()) }, nil
}

// serveContent 流式返回文件原始内容，支持 HTTP Range（206 Partial Content）。
// 用于 PDF/文本/音频/视频原画的前端预览。
// Content-Type 根据文件类型设置，不设置 Content-Disposition: attachment（避免触发下载）。
func (h *PreviewHandler) serveContent(w http.ResponseWriter, r *http.Request, fileID string) {
	file, err := h.db.GetFile(fileID)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}
	if !h.checkPermission(w, r, file.Owner) {
		return
	}

	filePath := file.StoragePath
	fileSize, err := h.storage.FileSize(filePath)
	if err != nil {
		log.Printf("[PREVIEW] content: file not on storage id=%s path=%s err=%v", fileID, filePath, err)
		http.Error(w, `{"error":"file not found on storage"}`, http.StatusNotFound)
		return
	}

	// 设置 Content-Type（根据文件类型）
	w.Header().Set("Content-Type", contentTypeFor(file.Filename))
	w.Header().Set("Accept-Ranges", "bytes")
	// inline 表示浏览器内联显示，不触发下载
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fileBaseName(file.Filename)))

	rangeHeader := r.Header.Get("Range")

	if rangeHeader == "" {
		// 完整文件
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		reader, err := h.storage.ReadFile(filePath, 0)
		if err != nil {
			http.Error(w, `{"error":"read failed"}`, http.StatusInternalServerError)
			return
		}
		defer reader.Close()
		io.Copy(w, reader)
		return
	}

	// 解析 Range: bytes=start-end 或 bytes=start-
	var start, end int64
	if n, _ := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); n != 2 {
		n2, err2 := fmt.Sscanf(rangeHeader, "bytes=%d-", &start)
		if err2 != nil || n2 != 1 || start < 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
			http.Error(w, `{"error":"invalid range"}`, http.StatusRequestedRangeNotSatisfiable)
			return
		}
		end = fileSize - 1
	}
	if start >= fileSize || end >= fileSize || start > end {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
		http.Error(w, `{"error":"range not satisfiable"}`, http.StatusRequestedRangeNotSatisfiable)
		return
	}

	chunkSize := end - start + 1
	w.Header().Set("Content-Length", strconv.FormatInt(chunkSize, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
	w.WriteHeader(http.StatusPartialContent)

	reader, err := h.storage.ReadFile(filePath, start)
	if err != nil {
		http.Error(w, `{"error":"read failed"}`, http.StatusInternalServerError)
		return
	}
	defer reader.Close()
	io.CopyN(w, reader, chunkSize)
}

// checkPermission 校验当前用户是否有权预览文件。
// 权限模型同下载：仅 owner 或 admin 可预览。
// 校验失败时已写入 HTTP 响应，调用方应直接 return。
func (h *PreviewHandler) checkPermission(w http.ResponseWriter, r *http.Request, owner string) bool {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	if owner != "" && owner != username && role != "admin" {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return false
	}
	return true
}
