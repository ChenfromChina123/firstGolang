package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"filesync/internal/model"
	"filesync/internal/storage"
)

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
