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

	"filesync/internal/auth"
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
		log.Printf("[DOWNLOAD] file not found: id=%s err=%v", fileID, err)
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// 权限校验：仅 owner 或 admin 可下载
	// 注：分享下载走独立接口 /api/share/...，不受此限制
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	if file.Owner != username && role != "admin" {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}

	filePath := file.StoragePath
	fileSize, err := h.storage.FileSize(filePath)
	if err != nil {
		log.Printf("[DOWNLOAD] get file size error: id=%s path=%s err=%v", fileID, filePath, err)
		http.Error(w, "file not found on storage", http.StatusNotFound)
		return
	}

	rangeHeader := r.Header.Get("Range")
	log.Printf("[DOWNLOAD] req: file=%s id=%s size=%d range=%q ua=%q", file.Filename, fileID, fileSize, rangeHeader, r.UserAgent())

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, file.Filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")

	// Handle Range header for resume download
	if rangeHeader == "" {
		// Full file download
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		log.Printf("[DOWNLOAD] full file: file=%s size=%d status=200", file.Filename, fileSize)

		reader, err := h.storage.ReadFile(filePath, 0)
		if err != nil {
			http.Error(w, "failed to read file", http.StatusInternalServerError)
			return
		}
		defer reader.Close()
		written, err := io.Copy(w, reader)
		log.Printf("[DOWNLOAD] done: file=%s written=%d err=%v", file.Filename, written, err)
		return
	}

	// Parse Range header: "bytes=start-end" or "bytes=start-"
	var start, end int64
	if n, _ := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); n != 2 {
		// Try "bytes=start-"
		n2, err2 := fmt.Sscanf(rangeHeader, "bytes=%d-", &start)
		if err2 != nil || n2 != 1 || start < 0 {
			log.Printf("[DOWNLOAD] invalid range: %q", rangeHeader)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
			http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		end = fileSize - 1
	}

	if start >= fileSize || end >= fileSize || start > end {
		log.Printf("[DOWNLOAD] range not satisfiable: start=%d end=%d size=%d", start, end, fileSize)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
		http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	chunkSize := end - start + 1
	w.Header().Set("Content-Length", strconv.FormatInt(chunkSize, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
	w.WriteHeader(http.StatusPartialContent)
	log.Printf("[DOWNLOAD] range: file=%s start=%d end=%d size=%d status=206", file.Filename, start, end, chunkSize)

	reader, err := h.storage.ReadFile(filePath, start)
	if err != nil {
		http.Error(w, "failed to read file", http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	// Limit to requested range
	written, err := io.CopyN(w, reader, chunkSize)
	log.Printf("[DOWNLOAD] done: file=%s written=%d expected=%d err=%v", file.Filename, written, chunkSize, err)
	if err != nil && err != io.EOF {
		log.Printf("[DOWNLOAD] stream error: file=%s err=%v", file.Filename, err)
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
// 权限：admin 可下载所有，普通用户仅下载自己的目录。
func (h *DownloadHandler) DownloadDir(w http.ResponseWriter, r *http.Request) {
	prefix := fastQueryParam(r.URL.RawQuery, "prefix")
	if prefix == "" {
		http.Error(w, "prefix required", http.StatusBadRequest)
		return
	}

	// owner 过滤：admin 可见所有，普通用户仅自己的
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	owner := username
	if role == "admin" {
		owner = ""
	}

	files, err := h.db.ListFiles(prefix, owner)
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
