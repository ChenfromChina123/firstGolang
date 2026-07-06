package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"filesync/internal/auth"
	"filesync/internal/storage"
	"filesync/internal/store"
)

// PreviewHandler 处理文件预览请求：元数据查询、缩略图生成、原始内容流式返回。
// 第一阶段支持图片/PDF/文本/音频；视频海报与转码在后续阶段实现。
type PreviewHandler struct {
	db      *store.DB
	storage storage.Storage
	baseURL string // 应用基础 URL（用于构造完整 URL，预留）
}

// NewPreviewHandler 创建预览 handler。
// basePath 由 storage.BasePath() 提供，用于缩略图缓存目录。
func NewPreviewHandler(db *store.DB, s storage.Storage, baseURL string) *PreviewHandler {
	return &PreviewHandler{db: db, storage: s, baseURL: baseURL}
}

// previewMeta 是 /api/preview/{fileID} 返回的元数据结构。
type previewMeta struct {
	Type      string            `json:"type"`       // image|pdf|text|code|audio|video|office|unsupported
	Filename  string            `json:"filename"`   // 完整虚拟路径
	Size      int64             `json:"size"`       // 字节
	Supported bool              `json:"supported"`  // 是否可预览
	URLs      map[string]string `json:"urls"`       // 各资源 URL
}

// ServeHTTP 路由分发：
//   GET /api/preview/{fileID}            → 元数据
//   GET /api/preview/{fileID}/thumb      → 图片缩略图（?size=small|medium|large）
//   GET /api/preview/{fileID}/poster     → 视频海报（第二阶段，501）
//   GET /api/preview/{fileID}/content    → 原始内容流（支持 Range 206）
//   GET /api/preview/{fileID}/transcode  → 视频转码（第三阶段，501）
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
		// 第三阶段实现
		http.Error(w, `{"error":"transcode not implemented yet"}`, http.StatusNotImplemented)
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
	// 视频类型附加海报 URL（第二阶段：原画播放 + 海报，不做转码）
	if ftype == "video" {
		meta.URLs["poster"] = base + "/poster"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}

// serveThumb 返回图片缩略图。
// 首次请求时调用 imaging 库生成并落盘缓存，后续直接流式返回。
// 查询参数 size: small(320x240) | medium(800x600) | large(1600x1200)
func (h *PreviewHandler) serveThumb(w http.ResponseWriter, r *http.Request, fileID string) {
	size := r.URL.Query().Get("size")
	if size == "" {
		size = "medium" // 默认中档
	}
	if size != "small" && size != "medium" && size != "large" {
		http.Error(w, `{"error":"invalid size"}`, http.StatusBadRequest)
		return
	}

	file, err := h.db.GetFile(fileID)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}
	if !h.checkPermission(w, r, file.Owner) {
		return
	}
	if getFileType(file.Filename) != "image" {
		http.Error(w, `{"error":"not an image"}`, http.StatusBadRequest)
		return
	}

	basePath := h.storage.BasePath()
	thumbPath := storage.ThumbnailPath(basePath, fileID, size)

	// 缓存未命中 → 生成
	if !storage.ThumbnailExists(basePath, fileID, size) {
		// file.StoragePath 在 LocalStorage 实现中已是绝对路径（basePath + ShardPath）
		srcPath := file.StoragePath
		if _, err := h.storage.FileSize(srcPath); err != nil {
			log.Printf("[PREVIEW] thumb: source not found id=%s path=%s err=%v", fileID, srcPath, err)
			http.Error(w, `{"error":"source file not found"}`, http.StatusNotFound)
			return
		}
		if _, err := storage.GenerateThumbnail(basePath, srcPath, fileID, size); err != nil {
			log.Printf("[PREVIEW] thumb: generate failed id=%s size=%s err=%v", fileID, size, err)
			http.Error(w, `{"error":"thumbnail generation failed"}`, http.StatusInternalServerError)
			return
		}
		log.Printf("[PREVIEW] thumb: generated id=%s size=%s", fileID, size)
	}

	// 流式返回缩略图
	f, err := os.Open(thumbPath)
	if err != nil {
		log.Printf("[PREVIEW] thumb: open cache failed id=%s err=%v", fileID, err)
		http.Error(w, `{"error":"thumbnail open failed"}`, http.StatusInternalServerError)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		http.Error(w, `{"error":"stat failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	w.Header().Set("Cache-Control", "public, max-age=86400") // 浏览器缓存 1 天
	w.WriteHeader(http.StatusOK)
	io.Copy(w, f)
}

// servePoster 返回视频海报（ffmpeg 截取的第一帧）。
// 首次请求时调用 ffmpeg 生成并落盘缓存，后续直接流式返回。
// 用于视频预览时的 poster 属性，提升用户感知体验（避免黑屏等待）。
func (h *PreviewHandler) servePoster(w http.ResponseWriter, r *http.Request, fileID string) {
	file, err := h.db.GetFile(fileID)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}
	if !h.checkPermission(w, r, file.Owner) {
		return
	}
	if getFileType(file.Filename) != "video" {
		http.Error(w, `{"error":"not a video"}`, http.StatusBadRequest)
		return
	}

	basePath := h.storage.BasePath()
	posterPath := storage.PosterPath(basePath, fileID)

	// 缓存未命中 → 调用 ffmpeg 生成
	if !storage.PosterExists(basePath, fileID) {
		srcPath := file.StoragePath
		if _, err := h.storage.FileSize(srcPath); err != nil {
			log.Printf("[PREVIEW] poster: source not found id=%s path=%s err=%v", fileID, srcPath, err)
			http.Error(w, `{"error":"source file not found"}`, http.StatusNotFound)
			return
		}
		if _, err := storage.GeneratePoster(basePath, srcPath, fileID); err != nil {
			log.Printf("[PREVIEW] poster: generate failed id=%s err=%v", fileID, err)
			http.Error(w, `{"error":"poster generation failed: ffmpeg not available or video invalid"}`, http.StatusInternalServerError)
			return
		}
		log.Printf("[PREVIEW] poster: generated id=%s", fileID)
	}

	// 流式返回海报
	f, err := os.Open(posterPath)
	if err != nil {
		log.Printf("[PREVIEW] poster: open cache failed id=%s err=%v", fileID, err)
		http.Error(w, `{"error":"poster open failed"}`, http.StatusInternalServerError)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		http.Error(w, `{"error":"stat failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	w.Header().Set("Cache-Control", "public, max-age=86400") // 浏览器缓存 1 天
	w.WriteHeader(http.StatusOK)
	io.Copy(w, f)
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

// getFileType 根据文件扩展名返回预览类型分类。
// 返回值：image|pdf|text|code|audio|video|office|unsupported
func getFileType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return "unsupported"
	}
	ext = strings.TrimPrefix(ext, ".")
	switch ext {
	case "jpg", "jpeg", "png", "gif", "webp", "bmp", "tiff", "tif":
		return "image"
	case "pdf":
		return "pdf"
	case "txt", "md", "log", "csv", "json", "xml", "yml", "yaml":
		return "text"
	case "js", "ts", "go", "py", "java", "c", "cpp", "rs", "rb", "php", "sh", "sql", "html", "css":
		return "code"
	case "mp3", "wav", "ogg", "flac", "aac", "m4a":
		return "audio"
	case "mp4", "webm", "mkv", "avi", "mov":
		return "video"
	case "doc", "docx", "xls", "xlsx", "ppt", "pptx":
		return "office"
	}
	return "unsupported"
}

// contentTypeFor 根据文件扩展名返回 HTTP Content-Type。
// 用于 serveContent 设置响应头，确保浏览器正确渲染。
func contentTypeFor(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".tiff", ".tif":
		return "image/tiff"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".log", ".md":
		return "text/plain; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".xml":
		return "application/xml; charset=utf-8"
	case ".yml", ".yaml":
		return "text/plain; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".ts":
		return "application/typescript; charset=utf-8"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".aac":
		return "audio/aac"
	case ".m4a":
		return "audio/mp4"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".mov":
		return "video/quicktime"
	}
	return "application/octet-stream"
}
