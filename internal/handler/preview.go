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
	"filesync/internal/model"
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
		h.serveTranscode(w, r, fileID)
	case "transcode-status":
		h.serveTranscodeStatus(w, r, fileID)
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
	// 视频类型附加海报与三档画质 URL（第三阶段：多画质转码）
	// video_high=1080p / video_medium=720p（默认）/ video_low=480p
	if ftype == "video" {
		meta.URLs["poster"] = base + "/poster"
		meta.URLs["video_high"] = base + "/transcode?quality=high"
		meta.URLs["video_medium"] = base + "/transcode?quality=medium"
		meta.URLs["video_low"] = base + "/transcode?quality=low"
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

	// 缓存命中 → 流式返回转码文件（支持 Range 206）
	if storage.TranscodeExists(basePath, fileID, quality) {
		h.serveTranscodeFile(w, r, file, fileID, quality)
		return
	}

	// 缓存未命中 → 启动异步转码任务，返回 202 + status_url（前端轮询）
	srcPath := file.StoragePath
	if _, err := h.storage.FileSize(srcPath); err != nil {
		log.Printf("[PREVIEW] transcode: source not found id=%s path=%s err=%v", fileID, srcPath, err)
		http.Error(w, `{"error":"source file not found"}`, http.StatusNotFound)
		return
	}

	status := storage.StartTranscodeJob(basePath, srcPath, fileID, quality)
	log.Printf("[PREVIEW] transcode: job started id=%s quality=%s status=%s", fileID, quality, status)

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

	// 先检查磁盘缓存（最权威状态源：文件存在即等价于转码完成可用）
	if storage.TranscodeExists(basePath, fileID, quality) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "done",
			"url":    fmt.Sprintf("/api/preview/%s/transcode?quality=%s", fileID, quality),
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
