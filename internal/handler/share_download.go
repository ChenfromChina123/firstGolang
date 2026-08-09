package handler

import (
	"archive/zip"
	"encoding/json"
	"filesync/internal/model"
	"filesync/internal/store"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (h *ShareHandler) downloadShare(w http.ResponseWriter, r *http.Request, id string) {
	// 防盗链：校验下载 token（必须从 /api/s/{id} 获取，30 分钟有效）
	token := r.URL.Query().Get("token")
	if !validateShareToken(token, id, h.secret) {
		log.Printf("[Share] download blocked: invalid token, id=%s", id)
		http.Error(w, `{"error":"forbidden","message":"invalid or missing download token"}`, http.StatusForbidden)
		return
	}

	// 频率限制：每 IP 每分钟最多 10 次下载
	if !h.downloadLimiter.Allow(r) {
		http.Error(w, `{"error":"rate_limited","message":"too many downloads, try again later"}`, http.StatusTooManyRequests)
		return
	}

	s, err := h.db.GetShare(id)
	if err != nil {
		http.Error(w, `{"error":"share_not_found"}`, http.StatusNotFound)
		return
	}
	if !s.IsActive {
		http.Error(w, `{"error":"share_disabled"}`, http.StatusForbidden)
		return
	}
	if s.ExpiresAt != nil && time.Now().After(*s.ExpiresAt) {
		http.Error(w, `{"error":"share_expired"}`, http.StatusForbidden)
		return
	}
	// 密码检查：有密码且未认证则返回 401
	if !h.requireShareAuth(w, r, s) {
		return
	}

	// 获取或设置 visitor cookie（30 天有效，跨所有分享复用）
	visitorID := getOrSetVisitorCookie(w, r)

	// 记录下载（利用 UNIQUE 约束去重，新访客才增加计数）
	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.Split(fwd, ",")[0]
		ip = strings.TrimSpace(ip)
	}
	if err := h.db.IncrementShareDownload(id, visitorID, ip, r.UserAgent()); err != nil {
		log.Printf("[Share] increment download error: id=%s err=%v", id, err)
		// 下载记录失败不阻断下载
	}

	// 根据分享类型下载
	if s.ShareType == "file" {
		h.downloadSharedFile(w, r, s)
		return
	}

	// dir 类型：检查 ?path= 参数，有则下载单文件，无则下载整目录 ZIP
	subPath := fastQueryParam(r.URL.RawQuery, "path")
	if subPath == "" {
		h.downloadSharedDir(w, r, s)
		return
	}
	h.downloadSharedSingleFile(w, r, s, subPath)
}

// downloadSharedSingleFile 下载目录分享中的单个文件（按子路径查找）。
// subPath 为相对分享根目录的路径（如 "sub/file.pdf"）。
// 通过 FindFileByName 精确匹配（按分享创建者过滤）。
func (h *ShareHandler) downloadSharedSingleFile(w http.ResponseWriter, r *http.Request, s *model.Share, subPath string) {
	fullPath, err := resolveSharePath(s.DirPrefix, subPath)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid_path","message":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}
	// 精确查找该路径的文件（按分享创建者过滤）
	f, err := h.db.FindFileByName(shareSpaceID(s), fullPath, s.CreatedBy)
	if err != nil || f == nil {
		http.Error(w, `{"error":"file_not_found","message":"文件不存在"}`, http.StatusNotFound)
		return
	}
	if strings.HasSuffix(f.Filename, ".keep") {
		http.Error(w, `{"error":"not_a_file","message":"不能下载目录占位文件"}`, http.StatusBadRequest)
		return
	}
	// 复用 downloadSharedFile 的流式下载逻辑
	h.serveSharedFile(w, r, s, f)
}

// serveSharedFile 流式下载单个文件（提取自 downloadSharedFile，供单文件和子路径下载复用）。
func (h *ShareHandler) serveSharedFile(w http.ResponseWriter, r *http.Request, s *model.Share, f *model.FileRecord) {
	fileSize, err := h.storage.FileSize(f.StoragePath)
	if err != nil {
		log.Printf("[Share] file size error: id=%s path=%s err=%v", s.ID, f.StoragePath, err)
		http.Error(w, `{"error":"file_not_found"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileBaseName(f.Filename)))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
	w.WriteHeader(http.StatusOK)

	reader, err := h.storage.ReadFile(f.StoragePath, 0)
	if err != nil {
		log.Printf("[Share] read file error: %v", err)
		return
	}
	defer reader.Close()
	written, err := io.Copy(w, reader)
	log.Printf("[Share] file downloaded: id=%s name=%s written=%d err=%v", s.ID, f.Filename, written, err)
}

// previewShare 公开预览分享文件（inline 模式 + Range 206，支持图片/PDF/文本/音频/视频 MP4）
// GET /api/s/{id}/preview            - 预览单文件分享
// GET /api/s/{id}/preview?path=子路径 - 预览目录分享中的单个文件
// 流程：token 校验 → 频率限制 → 检查有效性 → 密码校验 → 定位文件 → inline 流式返回
// 与 downloadShare 的差异：不计入下载次数，Content-Disposition: inline，支持 Range 206
func (h *ShareHandler) previewShare(w http.ResponseWriter, r *http.Request, id string) {
	// 防盗链：复用下载 token 校验（token 从 /api/s/{id} 获取，30 分钟有效）
	token := r.URL.Query().Get("token")
	if !validateShareToken(token, id, h.secret) {
		log.Printf("[Share] preview blocked: invalid token, id=%s", id)
		http.Error(w, `{"error":"forbidden","message":"invalid or missing preview token"}`, http.StatusForbidden)
		return
	}

	// 频率限制：复用下载限流（每 IP 每分钟 10 次），避免预览绕过限流
	if !h.downloadLimiter.Allow(r) {
		http.Error(w, `{"error":"rate_limited","message":"too many requests, try again later"}`, http.StatusTooManyRequests)
		return
	}

	s, err := h.db.GetShare(id)
	if err != nil {
		http.Error(w, `{"error":"share_not_found"}`, http.StatusNotFound)
		return
	}
	if !s.IsActive {
		http.Error(w, `{"error":"share_disabled"}`, http.StatusForbidden)
		return
	}
	if s.ExpiresAt != nil && time.Now().After(*s.ExpiresAt) {
		http.Error(w, `{"error":"share_expired"}`, http.StatusForbidden)
		return
	}
	// 密码校验：与下载一致，有密码且未认证则返回 401
	if !h.requireShareAuth(w, r, s) {
		return
	}

	// 定位文件：file 类型直接取，dir 类型按 ?path= 查找
	var f *model.FileRecord
	if s.ShareType == "file" {
		f, err = h.db.GetFile(s.FileID)
		if err != nil {
			http.Error(w, `{"error":"file_deleted","message":"文件已被删除"}`, http.StatusNotFound)
			return
		}
	} else {
		subPath := fastQueryParam(r.URL.RawQuery, "path")
		if subPath == "" {
			http.Error(w, `{"error":"path_required","message":"目录分享预览必须指定 path 参数"}`, http.StatusBadRequest)
			return
		}
		fullPath, err := resolveSharePath(s.DirPrefix, subPath)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid_path","message":"%s"}`, err.Error()), http.StatusBadRequest)
			return
		}
		f, err = h.db.FindFileByName(shareSpaceID(s), fullPath, s.CreatedBy)
		if err != nil || f == nil {
			http.Error(w, `{"error":"file_not_found","message":"文件不存在"}`, http.StatusNotFound)
			return
		}
	}
	if strings.HasSuffix(f.Filename, ".keep") {
		http.Error(w, `{"error":"not_a_file","message":"不能预览目录占位文件"}`, http.StatusBadRequest)
		return
	}

	h.serveSharedFileInline(w, r, s, f)
}

// serveSharedFileInline 流式返回文件内容用于浏览器内联预览（inline + Range 206）。
// 参考 preview.go 的 serveContent 实现：设置正确 Content-Type、Content-Disposition: inline、
// 支持 HTTP Range 请求（视频/音频拖动进度条需要）。与 serveSharedFile 的差异：
//   - Content-Disposition: inline（浏览器内联显示，不触发下载）
//   - Content-Type: 按 contentTypeFor 设置正确 MIME（图片/PDF/文本/音频/视频）
//   - 支持 Range 206 Partial Content
func (h *ShareHandler) serveSharedFileInline(w http.ResponseWriter, r *http.Request, s *model.Share, f *model.FileRecord) {
	fileSize, err := h.storage.FileSize(f.StoragePath)
	if err != nil {
		log.Printf("[Share] preview file size error: id=%s path=%s err=%v", s.ID, f.StoragePath, err)
		http.Error(w, `{"error":"file_not_found"}`, http.StatusNotFound)
		return
	}

	// 设置正确的 Content-Type 和 inline disposition
	w.Header().Set("Content-Type", contentTypeFor(f.Filename))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fileBaseName(f.Filename)))

	rangeHeader := r.Header.Get("Range")

	// 无 Range：完整文件返回 200
	if rangeHeader == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		reader, err := h.storage.ReadFile(f.StoragePath, 0)
		if err != nil {
			log.Printf("[Share] preview read error: id=%s err=%v", s.ID, err)
			return
		}
		defer reader.Close()
		written, err := io.Copy(w, reader)
		log.Printf("[Share] file previewed: id=%s name=%s written=%d err=%v", s.ID, f.Filename, written, err)
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

	reader, err := h.storage.ReadFile(f.StoragePath, start)
	if err != nil {
		log.Printf("[Share] preview range read error: id=%s err=%v", s.ID, err)
		return
	}
	defer reader.Close()
	io.CopyN(w, reader, chunkSize)
}

// downloadSharedFile 下载单文件分享（复用 serveSharedFile 流式下载逻辑）。
func (h *ShareHandler) downloadSharedFile(w http.ResponseWriter, r *http.Request, s *model.Share) {
	f, err := h.db.GetFile(s.FileID)
	if err != nil {
		http.Error(w, `{"error":"file_deleted"}`, http.StatusNotFound)
		return
	}
	h.serveSharedFile(w, r, s, f)
}

// downloadSharedDir 下载目录为 ZIP（复用 download.go 的逻辑）
// 按 share 创建者过滤，防止同 prefix 下其他用户文件被下载。
// shareSpaceID 分享访问的空间过滤语义：
//   - 新分享（space_id 非空）：精确匹配该空间
//   - 旧分享（space_id=”，多空间功能引入前创建）：不过滤空间（SpaceAll）
//     配合 owner=s.CreatedBy 过滤，仅返回创建者文件，不会跨用户泄露。
func shareSpaceID(s *model.Share) string {
	if s.SpaceID == "" {
		return store.SpaceAll
	}
	return s.SpaceID
}

func (h *ShareHandler) downloadSharedDir(w http.ResponseWriter, r *http.Request, s *model.Share) {
	files, err := h.db.ListFiles(shareSpaceID(s), s.DirPrefix, s.CreatedBy)
	if err != nil {
		log.Printf("[Share] list dir error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	if len(files) == 0 {
		http.Error(w, `{"error":"dir_empty"}`, http.StatusNotFound)
		return
	}

	dirName := strings.TrimSuffix(s.DirPrefix, "/")
	if dirName == "" {
		dirName = "files"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, dirName))
	w.Header().Set("Content-Type", "application/zip")

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, f := range files {
		if strings.HasSuffix(f.Filename, ".keep") {
			continue
		}
		relPath := strings.TrimPrefix(f.Filename, s.DirPrefix)
		if relPath == "" {
			continue
		}
		zf, err := zw.Create(relPath)
		if err != nil {
			log.Printf("[Share] zip create %s error: %v", relPath, err)
			continue
		}
		reader, err := h.storage.ReadFile(f.StoragePath, 0)
		if err != nil {
			log.Printf("[Share] read %s error: %v", f.StoragePath, err)
			continue
		}
		if _, err := io.Copy(zf, reader); err != nil {
			log.Printf("[Share] zip copy %s error: %v", relPath, err)
		}
		reader.Close()
	}
	log.Printf("[Share] dir downloaded: id=%s prefix=%s files=%d", s.ID, s.DirPrefix, len(files))
}

// listShareDir 列出分享目录内容（支持子目录导航）。
// GET /api/s/{id}/list?path=子目录
// 仅 dir 类型分享有效；返回当前层级的子目录和文件（不递归）。
// 响应：{path:"子目录", dirs:[{name,file_count}], files:[{id,name,size,created_at}]}
// 浏览不计数（仅下载计数）。
func (h *ShareHandler) listShareDir(w http.ResponseWriter, r *http.Request, id string) {
	// 频率限制：每 IP 每分钟最多 60 次列表查询（防暴力枚举分享 ID）
	if !h.infoLimiter.Allow(r) {
		http.Error(w, `{"error":"rate_limited","message":"too many requests, try again later"}`, http.StatusTooManyRequests)
		return
	}

	// 防盗链：校验 token（防止目录枚举攻击）
	token := r.URL.Query().Get("token")
	if !validateShareToken(token, id, h.secret) {
		http.Error(w, `{"error":"forbidden","message":"invalid or missing token"}`, http.StatusForbidden)
		return
	}

	s, err := h.db.GetShare(id)
	if err != nil {
		http.Error(w, `{"error":"share_not_found","message":"分享不存在或已删除"}`, http.StatusNotFound)
		return
	}
	if !s.IsActive {
		http.Error(w, `{"error":"share_disabled","message":"分享已禁用"}`, http.StatusForbidden)
		return
	}
	if s.ExpiresAt != nil && time.Now().After(*s.ExpiresAt) {
		http.Error(w, `{"error":"share_expired","message":"分享已过期"}`, http.StatusForbidden)
		return
	}
	if s.ShareType != "dir" {
		http.Error(w, `{"error":"not_dir_share","message":"仅目录分享支持浏览"}`, http.StatusBadRequest)
		return
	}
	// 密码检查：有密码且未认证则返回 401
	if !h.requireShareAuth(w, r, s) {
		return
	}

	// 解析子路径（相对分享根目录）
	subPath := fastQueryParam(r.URL.RawQuery, "path")
	fullPath, err := resolveSharePath(s.DirPrefix, subPath)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid_path","message":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}
	// 确保 fullPath 以 / 结尾以匹配 LIKE 前缀
	if !strings.HasSuffix(fullPath, "/") {
		fullPath += "/"
	}

	// 查询当前层级所有文件（递归，按分享创建者 + 空间过滤）
	files, err := h.db.ListFiles(shareSpaceID(s), fullPath, s.CreatedBy)
	if err != nil {
		log.Printf("[Share] list dir error: id=%s path=%s err=%v", id, fullPath, err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	// 聚合：区分当前层级文件和子目录
	// relPath = filename 去掉 fullPath 前缀
	type dirItem struct {
		Name      string `json:"name"`
		FileCount int    `json:"file_count"`
	}
	type fileItem struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		CreatedAt int64  `json:"created_at"`
	}

	dirMap := make(map[string]int) // 子目录名 → 文件数
	var fileItems []fileItem
	for _, f := range files {
		if strings.HasSuffix(f.Filename, ".keep") {
			continue
		}
		relPath := strings.TrimPrefix(f.Filename, fullPath)
		if relPath == "" {
			continue
		}
		// 若 relPath 含 /，说明在子目录下，取第一段作为子目录名
		if idx := strings.Index(relPath, "/"); idx > 0 {
			subDir := relPath[:idx]
			dirMap[subDir]++
		} else {
			// 当前层级文件
			fileItems = append(fileItems, fileItem{
				ID:        f.ID,
				Name:      relPath,
				Size:      f.Size,
				CreatedAt: f.CreatedAt.Unix(),
			})
		}
	}

	// 构造响应
	dirs := make([]dirItem, 0, len(dirMap))
	for name, count := range dirMap {
		dirs = append(dirs, dirItem{Name: name, FileCount: count})
	}

	// 返回的 path 字段：相对分享根的路径（去掉 s.DirPrefix）
	displayPath := strings.TrimPrefix(fullPath, s.DirPrefix)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"path":  displayPath,
		"dirs":  dirs,
		"files": fileItems,
	})
}

// batchDownloadShare 批量下载分享内多个文件为 ZIP。
// POST /api/s/{id}/batch  Body: {"paths":["sub/file1.pdf","file2.txt"]}
// 限制：最多 500 个文件，防止 DoS。
// 流程：校验分享 → 记录下载 → 逐个 resolveSharePath + FindFileByName → 流式 ZIP。
