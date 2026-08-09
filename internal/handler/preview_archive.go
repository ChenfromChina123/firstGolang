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

// serveArchive 处理压缩包预览请求：
//
//	GET /api/preview/{fileID}/archive            → 文件列表 JSON
//	GET /api/preview/{fileID}/archive?path=xxx   → 提取单个文件流（inline 预览或 attachment 下载）
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
