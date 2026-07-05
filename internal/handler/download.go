package handler

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"filesync/internal/store"
	"filesync/internal/storage"
)

// DownloadHandler handles file downloads with resume support
type DownloadHandler struct {
	db      *store.DB
	storage storage.Storage
}

// NewDownloadHandler creates a new download handler
func NewDownloadHandler(db *store.DB, s storage.Storage) *DownloadHandler {
	return &DownloadHandler{db: db, storage: s}
}

// DownloadFile serves a file with HTTP Range support for resume
// GET /api/download/{fileID}
func (h *DownloadHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimPrefix(r.URL.Path, "/api/download/")
	fileID = strings.TrimSuffix(fileID, "/")

	if fileID == "" {
		http.Error(w, "file_id required", http.StatusBadRequest)
		return
	}

	file, err := h.db.GetFile(fileID)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	filePath := file.StoragePath
	fileSize, err := h.storage.FileSize(filePath)
	if err != nil {
		log.Printf("get file size error: %v", err)
		http.Error(w, "file not found on storage", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, file.Filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")

	// Handle Range header for resume download
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		// Full file download
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)

		reader, err := h.storage.ReadFile(filePath, 0)
		if err != nil {
			http.Error(w, "failed to read file", http.StatusInternalServerError)
			return
		}
		defer reader.Close()
		io.Copy(w, reader)
		return
	}

	// Parse Range header: "bytes=start-end"
	var start, end int64
	_, err = fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end)
	if err != nil {
		// Try "bytes=start-"
		n, err := fmt.Sscanf(rangeHeader, "bytes=%d-", &start)
		if err != nil || n != 1 || start < 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
			http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		end = fileSize - 1
	}

	if start >= fileSize || end >= fileSize || start > end {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
		http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	chunkSize := end - start + 1
	w.Header().Set("Content-Length", strconv.FormatInt(chunkSize, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
	w.WriteHeader(http.StatusPartialContent)

	reader, err := h.storage.ReadFile(filePath, start)
	if err != nil {
		http.Error(w, "failed to read file", http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	// Limit to requested range
	if _, err := io.CopyN(w, reader, chunkSize); err != nil && err != io.EOF {
		log.Printf("stream error: %v", err)
	}
}

// ServeHTTP routes download requests
func (h *DownloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 路由：/api/download/dir?prefix=xxx → ZIP 打包下载目录
	if strings.HasPrefix(r.URL.Path, "/api/download/dir") {
		h.DownloadDir(w, r)
		return
	}
	h.DownloadFile(w, r)
}

// DownloadDir 把指定目录下所有文件（递归）打包成 ZIP 流式下载。
// GET /api/download/dir?prefix=docs/
// ZIP 内文件路径为相对路径（去掉 prefix 前缀），保留虚拟目录结构。
func (h *DownloadHandler) DownloadDir(w http.ResponseWriter, r *http.Request) {
	prefix := fastQueryParam(r.URL.RawQuery, "prefix")
	if prefix == "" {
		http.Error(w, "prefix required", http.StatusBadRequest)
		return
	}

	files, err := h.db.ListFiles(prefix)
	if err != nil {
		log.Printf("list files for zip error: %v", err)
		http.Error(w, "failed to list files", http.StatusInternalServerError)
		return
	}
	if len(files) == 0 {
		http.Error(w, "directory is empty or not found", http.StatusNotFound)
		return
	}

	// ZIP 文件名：用 prefix 去掉末尾 / 作为目录名
	dirName := strings.TrimSuffix(prefix, "/")
	if dirName == "" {
		dirName = "files"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, dirName))
	w.Header().Set("Content-Type", "application/zip")

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, f := range files {
		// 跳过 .keep 占位文件（路径枚举方案下的虚拟目录占位）
		if strings.HasSuffix(f.Filename, ".keep") {
			continue
		}
		// ZIP 内路径：去掉 prefix 前缀，保留相对路径（含子目录）
		relPath := strings.TrimPrefix(f.Filename, prefix)
		if relPath == "" {
			continue
		}
		zf, err := zw.Create(relPath)
		if err != nil {
			log.Printf("zip create %s error: %v", relPath, err)
			continue
		}
		reader, err := h.storage.ReadFile(f.StoragePath, 0)
		if err != nil {
			log.Printf("read file %s for zip error: %v", f.StoragePath, err)
			continue
		}
		if _, err := io.Copy(zf, reader); err != nil {
			log.Printf("zip copy %s error: %v", relPath, err)
		}
		reader.Close()
	}
}

// unused import guard
var _ = time.Now
