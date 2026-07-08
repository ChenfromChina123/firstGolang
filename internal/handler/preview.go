package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	baseURL    string                     // 应用基础 URL（用于构造完整 URL，预留）
	cacheStore storage.TranscodeCacheStore // OSS 转码缓存后端（nil 时走本地 MP4 fallback）
}

// NewPreviewHandler 创建预览 handler。
// cacheStore 为 OSS 转码缓存后端，nil 时视频转码走本地 MP4 fallback。
func NewPreviewHandler(db *store.DB, s storage.Storage, baseURL string, cacheStore storage.TranscodeCacheStore) *PreviewHandler {
	return &PreviewHandler{db: db, storage: s, baseURL: baseURL, cacheStore: cacheStore}
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
		// 校验源文件存在（按 storage_path 前缀路由到对应后端）
		if _, err := h.storage.FileSize(file.StoragePath); err != nil {
			log.Printf("[PREVIEW] thumb: source not found id=%s path=%s err=%v", fileID, file.StoragePath, err)
			http.Error(w, `{"error":"source file not found"}`, http.StatusNotFound)
			return
		}
		// 解析本地可读路径：local 去前缀；s3 下载到临时文件
		srcPath, cleanup, err := h.resolveLocalSource(file)
		if err != nil {
			log.Printf("[PREVIEW] thumb: resolve source failed id=%s err=%v", fileID, err)
			http.Error(w, `{"error":"source file unavailable"}`, http.StatusInternalServerError)
			return
		}
		defer cleanup()
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
		// 校验源文件存在（按 storage_path 前缀路由到对应后端）
		if _, err := h.storage.FileSize(file.StoragePath); err != nil {
			log.Printf("[PREVIEW] poster: source not found id=%s path=%s err=%v", fileID, file.StoragePath, err)
			http.Error(w, `{"error":"source file not found"}`, http.StatusNotFound)
			return
		}
		// 解析本地可读路径：local 去前缀；s3 下载到临时文件
		srcPath, cleanup, err := h.resolveLocalSource(file)
		if err != nil {
			log.Printf("[PREVIEW] poster: resolve source failed id=%s err=%v", fileID, err)
			http.Error(w, `{"error":"source file unavailable"}`, http.StatusInternalServerError)
			return
		}
		defer cleanup()
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

// serveTranscode 返回指定画质的转码视频流，支持 HTTP Range（206 Partial Content）。
// 查询参数 quality: high | medium | low（默认 medium）。
// high 直接 fallback 到原文件 serveContent（无需转码，避免无意义重编码）；
// medium/low 缓存命中直接流式返回，未命中启动异步转码任务并返回 202 Accepted + status_url。
// 前端通过轮询 /transcode-status 获取转码状态，完成后重新请求本端点加载视频流。
func (h *PreviewHandler) serveTranscode(w http.ResponseWriter, r *http.Request, fileID string) {
	quality := r.URL.Query().Get("quality")
	if quality == "" {
		quality = "medium" // 默认中等画质
	}
	if quality != "high" && quality != "medium" && quality != "low" {
		http.Error(w, `{"error":"invalid quality"}`, http.StatusBadRequest)
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
	if getFileType(file.Filename) != "video" {
		http.Error(w, `{"error":"not a video"}`, http.StatusBadRequest)
		return
	}

	// high 走原文件流式（无转码），保持源画质
	if quality == "high" {
		h.serveContent(w, r, fileID)
		return
	}

	basePath := h.storage.BasePath()

	// 缓存命中 → 流式返回转码文件
	// cacheStore 可用时检查 OSS HLS 产物；否则检查本地 MP4
	if storage.TranscodeCacheComplete(basePath, fileID, quality, h.cacheStore) {
		if h.cacheStore != nil {
			// HLS 缓存命中：重定向到 hls playlist 端点（前端用 hls.js 播放）
			http.Redirect(w, r, fmt.Sprintf("/api/preview/%s/hls?quality=%s", fileID, quality), http.StatusFound)
		} else {
			h.serveTranscodeFile(w, r, file, fileID, quality)
		}
		return
	}

	// 缓存未命中 → 启动异步转码任务，返回 202 + status_url（前端轮询）
	var srcPath string
	var tempCleanup func()
	cleanupSrc := false
	if file.StorageType == "s3" {
		// s3 视频 ≤200MB：下载到本地临时文件转码（worker 完成后自动清理）
		// >200MB：直接返回原文件流，避免下载大文件占用磁盘
		if file.Size > s3TranscodeMaxSize {
			h.serveContent(w, r, fileID)
			return
		}
		p, cleanup, err := h.resolveLocalSource(file)
		if err != nil {
			log.Printf("[PREVIEW] transcode: download s3 source failed id=%s err=%v", fileID, err)
			http.Error(w, `{"error":"source file unavailable"}`, http.StatusInternalServerError)
			return
		}
		srcPath = p
		tempCleanup = cleanup
		cleanupSrc = true
	} else {
		// local 文件的 StoragePath 带 "local:" 前缀，需去掉后交给 ffmpeg
		srcPath = strings.TrimPrefix(file.StoragePath, storage.LocalPrefix)
		if _, err := h.storage.FileSize(srcPath); err != nil {
			log.Printf("[PREVIEW] transcode: source not found id=%s path=%s err=%v", fileID, srcPath, err)
			http.Error(w, `{"error":"source file not found"}`, http.StatusNotFound)
			return
		}
	}

	status := storage.StartTranscodeJob(basePath, srcPath, fileID, quality, h.cacheStore, cleanupSrc)
	log.Printf("[PREVIEW] transcode: job started id=%s quality=%s status=%s srcType=%s", fileID, quality, status, file.StorageType)

	// pending：worker 会使用临时文件并负责清理（CleanupSrc=true）
	// 非 pending（running/done/failed）：当前临时文件不会被使用，立即清理
	if tempCleanup != nil && status != "pending" {
		tempCleanup()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted) // 202 Accepted：转码任务已启动，前端轮询 status_url
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     status,
		"quality":    quality,
		"file_id":    fileID,
		"status_url": fmt.Sprintf("/api/preview/%s/transcode-status?quality=%s", fileID, quality),
	})
}

// serveTranscodeFile 流式返回已转码的缓存文件，支持 HTTP Range（206 Partial Content）。
// 仅在 TranscodeExists 命中后调用，调用方需先校验文件存在与权限。
func (h *PreviewHandler) serveTranscodeFile(w http.ResponseWriter, r *http.Request, file *model.FileRecord, fileID, quality string) {
	basePath := h.storage.BasePath()
	transcodePath := storage.TranscodePath(basePath, fileID, quality)

	f, err := os.Open(transcodePath)
	if err != nil {
		log.Printf("[PREVIEW] transcode: open cache failed id=%s quality=%s err=%v", fileID, quality, err)
		http.Error(w, `{"error":"transcode open failed"}`, http.StatusInternalServerError)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		http.Error(w, `{"error":"stat failed"}`, http.StatusInternalServerError)
		return
	}
	fileSize := fi.Size()

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fileBaseName(file.Filename)))

	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		io.Copy(w, f)
		return
	}

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
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		http.Error(w, `{"error":"seek failed"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(chunkSize, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
	w.WriteHeader(http.StatusPartialContent)
	io.CopyN(w, f, chunkSize)
}

// serveTranscodeStatus 查询转码任务状态（前端轮询用）。
// 查询参数 quality: high | medium | low（默认 medium）。
// 返回 JSON：{status: "done|pending|running|failed|not_started", url?, error?}
//   - done：磁盘缓存命中（最权威状态源），附加 url 供前端直接加载
//   - not_started：任务表无记录（服务器重启或从未启动），前端应触发 transcode 端点启动
//   - pending/running/failed：返回 job 当前状态与错误信息
func (h *PreviewHandler) serveTranscodeStatus(w http.ResponseWriter, r *http.Request, fileID string) {
	quality := r.URL.Query().Get("quality")
	if quality == "" {
		quality = "medium"
	}
	if quality != "high" && quality != "medium" && quality != "low" {
		http.Error(w, `{"error":"invalid quality"}`, http.StatusBadRequest)
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
	if getFileType(file.Filename) != "video" {
		http.Error(w, `{"error":"not a video"}`, http.StatusBadRequest)
		return
	}

	basePath := h.storage.BasePath()

	// 先检查缓存（最权威状态源：cacheStore 可用时检查 OSS HLS；否则检查本地 MP4）
	if storage.TranscodeCacheComplete(basePath, fileID, quality, h.cacheStore) {
		url := fmt.Sprintf("/api/preview/%s/transcode?quality=%s", fileID, quality)
		if h.cacheStore != nil {
			url = fmt.Sprintf("/api/preview/%s/hls?quality=%s", fileID, quality)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "done",
			"url":    url,
		})
		return
	}

	// 查询内存任务表
	job := storage.GetTranscodeJob(fileID, quality)
	if job == nil {
		// 任务不存在（从未启动或已被 time.AfterFunc 清理）
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "not_started",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": string(job.GetStatus()),
		"error":  job.GetError(),
	})
}

// serveHLS 分发 HLS 请求：无 seg 参数返回 m3u8 playlist，有 seg 参数返回 ts 切片。
// 路由：GET /api/preview/{fileID}/hls?quality={q}&seg={segName}
//   - seg 为空：返回 m3u8 playlist（转码中从本地读，完成从 OSS 读）
//   - seg 非空：返回 ts 切片（转码中从本地读，完成 302 重定向到 OSS presigned URL）
func (h *PreviewHandler) serveHLS(w http.ResponseWriter, r *http.Request, fileID string) {
	quality := r.URL.Query().Get("quality")
	if quality == "" {
		quality = "medium"
	}
	if quality != "high" && quality != "medium" && quality != "low" {
		http.Error(w, `{"error":"invalid quality"}`, http.StatusBadRequest)
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
	if getFileType(file.Filename) != "video" {
		http.Error(w, `{"error":"not a video"}`, http.StatusBadRequest)
		return
	}

	seg := r.URL.Query().Get("seg")
	if seg == "" {
		h.serveHLSPlaylist(w, r, fileID, file, quality)
	} else {
		h.serveHLSSegment(w, r, fileID, quality, seg)
	}
}

// serveHLSPlaylist 返回 m3u8 播放列表。
// 转码完成（OSS）：从 OSS 读取 m3u8，重写 segment URL 为绝对路径。
// 转码中（本地）：从本地临时目录读取 m3u8，重写 segment URL。
// 未启动：触发转码任务，返回 202 Accepted。
func (h *PreviewHandler) serveHLSPlaylist(w http.ResponseWriter, r *http.Request, fileID string, file *model.FileRecord, quality string) {
	basePath := h.storage.BasePath()

	// 检查转码产物是否已完整可用（OSS 或本地）
	complete, location := storage.HLSTranscodeComplete(basePath, fileID, quality, h.cacheStore)

	if complete {
		var m3u8Data []byte
		if location == "oss" && h.cacheStore != nil {
			// 从 OSS 读取 m3u8
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			rc, err := h.cacheStore.GetTranscodePlaylist(ctx, fileID, quality)
			if err != nil {
				log.Printf("[PREVIEW] hls: get playlist from oss failed id=%s quality=%s err=%v", fileID, quality, err)
				http.Error(w, `{"error":"playlist read failed"}`, http.StatusInternalServerError)
				return
			}
			defer rc.Close()
			m3u8Data, err = io.ReadAll(rc)
			if err != nil {
				http.Error(w, `{"error":"playlist read failed"}`, http.StatusInternalServerError)
				return
			}
		} else {
			// 从本地临时目录读取 m3u8
			playlistPath := storage.HLSLocalPlaylistPath(basePath, fileID, quality)
			data, err := os.ReadFile(playlistPath)
			if err != nil {
				log.Printf("[PREVIEW] hls: read local playlist failed id=%s quality=%s err=%v", fileID, quality, err)
				http.Error(w, `{"error":"playlist read failed"}`, http.StatusInternalServerError)
				return
			}
			m3u8Data = data
		}

		// 重写 segment URL：seg_00001.ts → /api/preview/{fileID}/hls?quality={q}&seg=seg_00001.ts
		m3u8Text := string(m3u8Data)
		segURLPrefix := fmt.Sprintf("/api/preview/%s/hls?quality=%s&seg=", fileID, quality)
		rewritten := regexp.MustCompile(`(seg_\d{5}\.ts)`).ReplaceAllString(m3u8Text, segURLPrefix+"$1")

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(rewritten))
		return
	}

	// 转码产物不完整，检查本地临时 m3u8 是否存在（转码中）
	playlistPath := storage.HLSLocalPlaylistPath(basePath, fileID, quality)
	if data, err := os.ReadFile(playlistPath); err == nil {
		m3u8Text := string(data)
		segURLPrefix := fmt.Sprintf("/api/preview/%s/hls?quality=%s&seg=", fileID, quality)
		rewritten := regexp.MustCompile(`(seg_\d{5}\.ts)`).ReplaceAllString(m3u8Text, segURLPrefix+"$1")

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache") // 转码中需重新请求获取新切片
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(rewritten))
		return
	}

	// 转码未启动：解析源文件路径并触发转码
	var srcPath string
	var tempCleanup func()
	cleanupSrc := false
	if file.StorageType == "s3" {
		if file.Size > s3TranscodeMaxSize {
			http.Error(w, `{"error":"s3 video too large for transcode"}`, http.StatusRequestEntityTooLarge)
			return
		}
		p, cleanup, err := h.resolveLocalSource(file)
		if err != nil {
			log.Printf("[PREVIEW] hls: download s3 source failed id=%s err=%v", fileID, err)
			http.Error(w, `{"error":"source file unavailable"}`, http.StatusInternalServerError)
			return
		}
		srcPath = p
		tempCleanup = cleanup
		cleanupSrc = true
	} else {
		srcPath = strings.TrimPrefix(file.StoragePath, storage.LocalPrefix)
	}

	status := storage.StartTranscodeJob(basePath, srcPath, fileID, quality, h.cacheStore, cleanupSrc)
	if tempCleanup != nil && status != "pending" {
		tempCleanup()
	}

	log.Printf("[PREVIEW] hls: transcode triggered id=%s quality=%s status=%s", fileID, quality, status)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     status,
		"quality":    quality,
		"file_id":    fileID,
		"status_url": fmt.Sprintf("/api/preview/%s/transcode-status?quality=%s", fileID, quality),
	})
}

// serveHLSSegment 返回单个 ts 切片。
// 转码完成（OSS）：302 重定向到 presigned URL（前端直连 OSS，省服务器流量）。
// 转码中（本地）：serve 本地 ts 文件（支持边转边播）。
// 不存在：404。
func (h *PreviewHandler) serveHLSSegment(w http.ResponseWriter, r *http.Request, fileID, quality, segName string) {
	// 校验 segName 格式（防路径穿越）
	if !hlsSegNamePattern.MatchString(segName) {
		http.Error(w, `{"error":"invalid segment name"}`, http.StatusBadRequest)
		return
	}

	basePath := h.storage.BasePath()

	// 转码完成（OSS）：302 重定向到 presigned URL
	complete, location := storage.HLSTranscodeComplete(basePath, fileID, quality, h.cacheStore)
	if complete && location == "oss" && h.cacheStore != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		url, err := h.cacheStore.PresignedTranscodeURL(ctx, fileID, quality, segName, 1*time.Hour)
		if err != nil {
			log.Printf("[PREVIEW] hls: presigned url failed id=%s seg=%s err=%v", fileID, segName, err)
			http.Error(w, `{"error":"presigned url failed"}`, http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
		return
	}

	// 转码中或本地完成：serve 本地 ts 文件
	segPath := storage.HLSLocalSegmentPath(basePath, fileID, quality, segName)
	f, err := os.Open(segPath)
	if err != nil {
		log.Printf("[PREVIEW] hls: segment not found id=%s seg=%s err=%v", fileID, segName, err)
		http.Error(w, `{"error":"segment not found"}`, http.StatusNotFound)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		http.Error(w, `{"error":"stat failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
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

// serveArchive 处理压缩包预览请求：
//   GET /api/preview/{fileID}/archive            → 文件列表 JSON
//   GET /api/preview/{fileID}/archive?path=xxx   → 提取单个文件流（inline 预览或 attachment 下载）
//
// 安全限制：
//   - 压缩包大小 ≤ 2GB（防止临时文件占用磁盘过大）
//   - 文件条目数 ≤ 10000（防压缩炸弹）
//   - 单文件提取大小 ≤ 500MB（防止内存/磁盘爆炸）
//   - 路径穿越拒绝（..、绝对路径，由 storage.ListArchive/ExtractArchiveFile 内部清洗）
//   - 加密压缩包拒绝（zip 标准库不支持 AES）
func (h *PreviewHandler) serveArchive(w http.ResponseWriter, r *http.Request, fileID string) {
	file, err := h.db.GetFile(fileID)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}
	if !h.checkPermission(w, r, file.Owner) {
		return
	}

	// 压缩包大小限制（≤ 2GB）
	const maxArchiveSize = 2 * 1024 * 1024 * 1024
	if file.Size > maxArchiveSize {
		http.Error(w, `{"error":"archive too large (max 2GB)"}`, http.StatusRequestEntityTooLarge)
		return
	}

	targetPath := r.URL.Query().Get("path")
	if targetPath == "" {
		h.serveArchiveList(w, r, file)
	} else {
		h.serveArchiveExtract(w, r, file, targetPath)
	}
}

// serveArchiveList 返回压缩包内文件列表 JSON。
// 调用 storage.ListArchive 流式解析，拒绝路径穿越条目。
func (h *PreviewHandler) serveArchiveList(w http.ResponseWriter, r *http.Request, file *model.FileRecord) {
	reader, err := h.storage.ReadFile(file.StoragePath, 0)
	if err != nil {
		log.Printf("[PREVIEW] archive list: read failed id=%s path=%s err=%v", file.ID, file.StoragePath, err)
		http.Error(w, `{"error":"read archive failed"}`, http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	entries, truncated, err := storage.ListArchive(reader, file.Filename, 10000)
	if err != nil {
		log.Printf("[PREVIEW] archive list: parse failed id=%s err=%v", file.ID, err)
		http.Error(w, fmt.Sprintf(`{"error":"parse archive failed: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"entries":   entries,
		"total":     len(entries),
		"truncated": truncated,
	})
}

// serveArchiveExtract 提取压缩包内单个文件并流式返回。
// ?download=1 → attachment 下载；否则 inline 预览。
// 单文件提取大小限制 500MB（超出返回 413）。
// size=-1 表示流式无法预知大小（单文件 .gz/.bz2），跳过大小校验。
func (h *PreviewHandler) serveArchiveExtract(w http.ResponseWriter, r *http.Request, file *model.FileRecord, targetPath string) {
	const maxExtractSize = 500 * 1024 * 1024 // 500MB

	reader, err := h.storage.ReadFile(file.StoragePath, 0)
	if err != nil {
		log.Printf("[PREVIEW] archive extract: read failed id=%s err=%v", file.ID, err)
		http.Error(w, `{"error":"read archive failed"}`, http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	rc, size, err := storage.ExtractArchiveFile(reader, file.Filename, targetPath)
	if err != nil {
		if err == os.ErrNotExist {
			http.Error(w, `{"error":"file not found in archive"}`, http.StatusNotFound)
			return
		}
		log.Printf("[PREVIEW] archive extract: failed id=%s path=%s err=%v", file.ID, targetPath, err)
		http.Error(w, fmt.Sprintf(`{"error":"extract failed: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	// 大小校验（size=-1 表示流式无法预知，跳过）
	if size > maxExtractSize {
		http.Error(w, `{"error":"extracted file too large (max 500MB)"}`, http.StatusRequestEntityTooLarge)
		return
	}

	// 提取文件名（targetPath 最后一段）
	extractName := targetPath
	if idx := strings.LastIndex(extractName, "/"); idx >= 0 {
		extractName = extractName[idx+1:]
	}
	if extractName == "" {
		extractName = "extracted"
	}

	// 设置响应头
	w.Header().Set("Content-Type", contentTypeFor(extractName))
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, extractName))
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}

	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("[PREVIEW] archive extract: copy failed id=%s path=%s err=%v", file.ID, targetPath, err)
	}
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
// 返回值：image|pdf|text|code|audio|video|office|archive|unsupported
// 压缩包类型优先用 storage.SupportedArchive 判断（覆盖 .tar.gz/.tar.bz2 双扩展名）。
func getFileType(filename string) string {
	// 压缩包优先判断（多扩展名，filepath.Ext 只取最后一段会漏判 .tar.gz）
	if storage.SupportedArchive(filename) {
		return "archive"
	}
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
