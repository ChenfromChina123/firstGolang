package handler

import (
	"io"
	"log"
	"net/http"
	"os"
	"strconv"

	"filesync/internal/storage"
)

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
