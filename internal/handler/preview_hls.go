package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"filesync/internal/model"
	"filesync/internal/storage"
)

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
