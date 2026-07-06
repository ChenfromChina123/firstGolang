package handler

import (
	"archive/zip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/store"
	"filesync/internal/storage"
)

// ShareHandler 处理分享链接的创建、管理和公开访问
type ShareHandler struct {
	db      *store.DB
	storage storage.Storage
}

// NewShareHandler 创建分享 handler
func NewShareHandler(db *store.DB, st storage.Storage) *ShareHandler {
	return &ShareHandler{db: db, storage: st}
}

// createShareRequest 创建分享请求体
type createShareRequest struct {
	FileID     string `json:"file_id,omitempty"`     // 单文件分享：文件 ID
	DirPrefix  string `json:"dir_prefix,omitempty"`  // 目录分享：目录前缀
	ShareType  string `json:"share_type"`            // "file" | "dir"
	ExpiresIn  int    `json:"expires_in"`            // 过期秒数（0=永久）
}

// createShareResponse 创建分享响应体
type createShareResponse struct {
	ID        string `json:"id"`
	ShareType string `json:"share_type"`
	URL       string `json:"url"`        // 分享页面 URL（/web/share.html?id=xxx）
	ExpiresAt *int64  `json:"expires_at,omitempty"` // Unix 时间戳
}

// ServeHTTP 路由分发：
// 认证路由（/api/share, /api/share/）：
//   GET    /api/share           - 列出当前用户的分享
//   POST   /api/share           - 创建分享
//   DELETE /api/share/{id}      - 删除分享
// 公开路由（/api/s/）：
//   GET    /api/s/{id}          - 获取分享信息
//   GET    /api/s/{id}/download - 下载文件或目录
func (h *ShareHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	path := r.URL.Path

	// 公开路由：/api/s/{id} 和 /api/s/{id}/download
	if strings.HasPrefix(path, "/api/s/") {
		h.handlePublic(w, r)
		return
	}

	// 认证路由：/api/share 和 /api/share/{id}
	username := auth.UsernameFromContext(r.Context())
	if username == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// /api/share（无 ID 后缀）
	if path == "/api/share" || path == "/api/share/" {
		switch r.Method {
		case http.MethodGet:
			h.listShares(w, r, username)
		case http.MethodPost:
			h.createShare(w, r, username)
		default:
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		}
		return
	}

	// /api/share/save - 转存分享文件到自己账户
	if path == "/api/share/save" && r.Method == http.MethodPost {
		h.saveShareToMyFiles(w, r, username)
		return
	}

	// /api/share/{id}
	if strings.HasPrefix(path, "/api/share/") {
		id := strings.TrimPrefix(path, "/api/share/")
		id = strings.TrimSuffix(id, "/")
		if id == "" {
			http.Error(w, `{"error":"id_required"}`, http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodDelete {
			h.deleteShare(w, r, username, id)
		} else {
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		}
		return
	}

	http.NotFound(w, r)
}

// handlePublic 处理公开访问路由（无需认证）
// GET  /api/s/{id}          - 获取分享信息
// GET  /api/s/{id}/download - 下载（支持 ?path= 子路径下载单文件）
// GET  /api/s/{id}/list     - 列出目录内容（支持 ?path= 子目录）
// POST /api/s/{id}/batch    - 批量下载 ZIP（Body: {"paths":[...]}）
func (h *ShareHandler) handlePublic(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/s/")
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		http.Error(w, `{"error":"id_required"}`, http.StatusBadRequest)
		return
	}

	// 分离 id 和后缀动作
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	if id == "" {
		http.Error(w, `{"error":"id_required"}`, http.StatusBadRequest)
		return
	}

	// 无后缀：获取分享信息
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		h.getSharePublic(w, r, id)
		return
	}

	action := parts[1]
	switch action {
	case "download":
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		h.downloadShare(w, r, id)
	case "list":
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		h.listShareDir(w, r, id)
	case "batch":
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		h.batchDownloadShare(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

// createShare 创建分享链接
// POST /api/share  Body: {"file_id":"xxx","share_type":"file","expires_in":0}
// 或 {"dir_prefix":"docs/","share_type":"dir","expires_in":604800}
func (h *ShareHandler) createShare(w http.ResponseWriter, r *http.Request, username string) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req createShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}

	if req.ShareType != "file" && req.ShareType != "dir" {
		http.Error(w, `{"error":"invalid_share_type","message":"share_type must be file or dir"}`, http.StatusBadRequest)
		return
	}

	// 验证文件/目录存在（按当前用户过滤，确保只能分享自己的文件）
	var name string
	var size int64
	var fileCount int
	if req.ShareType == "file" {
		if req.FileID == "" {
			http.Error(w, `{"error":"file_id_required"}`, http.StatusBadRequest)
			return
		}
		f, err := h.db.GetFile(req.FileID)
		if err != nil {
			http.Error(w, `{"error":"file_not_found"}`, http.StatusNotFound)
			return
		}
		// 权限校验：仅 owner 或 admin 可分享
		role := auth.RoleFromContext(r.Context())
		if f.Owner != username && role != "admin" {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		name = f.Filename
		size = f.Size
		fileCount = 1
	} else {
		// 目录分享
		if req.DirPrefix == "" {
			http.Error(w, `{"error":"dir_prefix_required"}`, http.StatusBadRequest)
			return
		}
		// 确保以 / 结尾
		if !strings.HasSuffix(req.DirPrefix, "/") {
			req.DirPrefix += "/"
		}
		// 按当前用户过滤（admin 传 "" 看所有）
		role := auth.RoleFromContext(r.Context())
		owner := username
		if role == "admin" {
			owner = ""
		}
		files, err := h.db.ListFiles(req.DirPrefix, owner)
		if err != nil {
			log.Printf("[Share] list files error: %v", err)
			http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
			return
		}
		if len(files) == 0 {
			http.Error(w, `{"error":"dir_empty_or_not_found"}`, http.StatusNotFound)
			return
		}
		name = strings.TrimSuffix(req.DirPrefix, "/")
		for _, f := range files {
			if !strings.HasSuffix(f.Filename, ".keep") {
				size += f.Size
				fileCount++
			}
		}
	}

	// 生成 8 字符短 ID
	shareID, err := generateShareID()
	if err != nil {
		log.Printf("[Share] generate id error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	// 计算过期时间
	var expiresAt *time.Time
	if req.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(req.ExpiresIn) * time.Second)
		expiresAt = &t
	}

	// 创建分享记录
	s := &model.Share{
		ID:            shareID,
		FileID:        req.FileID,
		DirPrefix:     req.DirPrefix,
		ShareType:     req.ShareType,
		CreatedBy:     username,
		CreatedAt:     time.Now(),
		ExpiresAt:     expiresAt,
		DownloadCount: 0,
		IsActive:      true,
	}
	if err := h.db.CreateShare(s); err != nil {
		log.Printf("[Share] create error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("[Share] created: id=%s type=%s name=%s user=%s", shareID, req.ShareType, name, username)

	resp := createShareResponse{
		ID:        shareID,
		ShareType: req.ShareType,
		URL:       fmt.Sprintf("/web/share.html?id=%s", shareID),
	}
	if expiresAt != nil {
		ts := expiresAt.Unix()
		resp.ExpiresAt = &ts
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// listShares 列出当前用户的所有分享
// GET /api/share
func (h *ShareHandler) listShares(w http.ResponseWriter, r *http.Request, username string) {
	shares, err := h.db.ListSharesByCreator(username)
	if err != nil {
		log.Printf("[Share] list error: user=%s err=%v", username, err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	// 构造响应（补充文件名信息）
	type shareListItem struct {
		ID            string  `json:"id"`
		ShareType     string  `json:"share_type"`
		Name          string  `json:"name"`           // 文件名或目录名
		URL           string  `json:"url"`            // 分享页面 URL
		CreatedAt     int64   `json:"created_at"`     // Unix 时间戳
		ExpiresAt     *int64  `json:"expires_at,omitempty"`
		DownloadCount int     `json:"download_count"`
		IsActive      bool    `json:"is_active"`
		IsExpired     bool    `json:"is_expired"`
	}

	items := make([]shareListItem, 0, len(shares))
	for _, s := range shares {
		var name string
		if s.ShareType == "file" && s.FileID != "" {
			if f, err := h.db.GetFile(s.FileID); err == nil {
				name = f.Filename
			} else {
				name = "(file deleted)"
			}
		} else if s.ShareType == "dir" {
			name = strings.TrimSuffix(s.DirPrefix, "/")
		}

		isExpired := s.ExpiresAt != nil && time.Now().After(*s.ExpiresAt)
		item := shareListItem{
			ID:            s.ID,
			ShareType:     s.ShareType,
			Name:          name,
			URL:           fmt.Sprintf("/web/share.html?id=%s", s.ID),
			CreatedAt:     s.CreatedAt.Unix(),
			DownloadCount: s.DownloadCount,
			IsActive:      s.IsActive,
			IsExpired:     isExpired,
		}
		if s.ExpiresAt != nil {
			ts := s.ExpiresAt.Unix()
			item.ExpiresAt = &ts
		}
		items = append(items, item)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// deleteShare 删除分享（验证所有权）
// DELETE /api/share/{id}
func (h *ShareHandler) deleteShare(w http.ResponseWriter, r *http.Request, username, id string) {
	s, err := h.db.GetShare(id)
	if err != nil {
		http.Error(w, `{"error":"share_not_found"}`, http.StatusNotFound)
		return
	}
	if s.CreatedBy != username {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	if err := h.db.DeleteShare(id); err != nil {
		log.Printf("[Share] delete error: id=%s err=%v", id, err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	log.Printf("[Share] deleted: id=%s user=%s", id, username)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success":true}`))
}

// getSharePublic 返回公开分享信息（不暴露 file_id/storage_path 等敏感字段）
// GET /api/s/{id}
func (h *ShareHandler) getSharePublic(w http.ResponseWriter, r *http.Request, id string) {
	s, err := h.db.GetShare(id)
	if err != nil {
		http.Error(w, `{"error":"share_not_found","message":"分享不存在或已删除"}`, http.StatusNotFound)
		return
	}

	// 检查是否激活
	if !s.IsActive {
		http.Error(w, `{"error":"share_disabled","message":"分享已禁用"}`, http.StatusForbidden)
		return
	}

	// 检查是否过期
	isExpired := s.ExpiresAt != nil && time.Now().After(*s.ExpiresAt)
	if isExpired {
		http.Error(w, `{"error":"share_expired","message":"分享已过期"}`, http.StatusForbidden)
		return
	}

	// 构造公开信息（不暴露内部路径）
	var name string
	var size int64
	var fileCount int
	if s.ShareType == "file" {
		f, err := h.db.GetFile(s.FileID)
		if err != nil {
			http.Error(w, `{"error":"file_deleted","message":"文件已被删除"}`, http.StatusNotFound)
			return
		}
		name = f.Filename
		size = f.Size
		fileCount = 1
	} else {
		name = strings.TrimSuffix(s.DirPrefix, "/")
		// 公开访问也按 share 创建者过滤，防止同 prefix 下其他用户文件泄露
		files, err := h.db.ListFiles(s.DirPrefix, s.CreatedBy)
		if err == nil {
			for _, f := range files {
				if !strings.HasSuffix(f.Filename, ".keep") {
					size += f.Size
					fileCount++
				}
			}
		}
	}

	info := model.SharePublicInfo{
		ID:            s.ID,
		ShareType:     s.ShareType,
		Name:          name,
		Size:          size,
		FileCount:     fileCount,
		ExpiresAt:     s.ExpiresAt,
		DownloadCount: s.DownloadCount,
		IsExpired:     isExpired,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// downloadShare 公开下载分享的文件或目录
// GET /api/s/{id}/download            - 下载单文件分享，或整目录 ZIP
// GET /api/s/{id}/download?path=子路径 - 下载目录分享中的单个文件
// 流程：检查有效性 → 设置 visitor cookie → 记录下载（去重）→ 流式下载
func (h *ShareHandler) downloadShare(w http.ResponseWriter, r *http.Request, id string) {
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
	f, err := h.db.FindFileByName(fullPath, s.CreatedBy)
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

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, f.Filename))
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
func (h *ShareHandler) downloadSharedDir(w http.ResponseWriter, r *http.Request, s *model.Share) {
	files, err := h.db.ListFiles(s.DirPrefix, s.CreatedBy)
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

	// 查询当前层级所有文件（递归，按分享创建者过滤）
	files, err := h.db.ListFiles(fullPath, s.CreatedBy)
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
func (h *ShareHandler) batchDownloadShare(w http.ResponseWriter, r *http.Request, id string) {
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
	if s.ShareType != "dir" {
		http.Error(w, `{"error":"not_dir_share","message":"仅目录分享支持批量下载"}`, http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024) // 限制 body 64KB
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}
	if len(req.Paths) == 0 {
		http.Error(w, `{"error":"paths_required"}`, http.StatusBadRequest)
		return
	}
	if len(req.Paths) > 500 {
		http.Error(w, `{"error":"too_many_files","message":"单次最多 500 个文件"}`, http.StatusBadRequest)
		return
	}

	// 记录下载（幂等，同 visitor 不重复计数）
	visitorID := getOrSetVisitorCookie(w, r)
	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	if err := h.db.IncrementShareDownload(id, visitorID, ip, r.UserAgent()); err != nil {
		log.Printf("[Share] batch increment download error: id=%s err=%v", id, err)
	}

	// 流式打包 ZIP
	dirName := strings.TrimSuffix(s.DirPrefix, "/")
	if dirName == "" {
		dirName = "files"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-batch.zip"`, dirName))
	w.Header().Set("Content-Type", "application/zip")

	zw := zip.NewWriter(w)
	defer zw.Close()

	var successCount, failCount int
	for _, p := range req.Paths {
		fullPath, err := resolveSharePath(s.DirPrefix, p)
		if err != nil {
			log.Printf("[Share] batch skip invalid path %q: %v", p, err)
			failCount++
			continue
		}
		f, err := h.db.FindFileByName(fullPath, s.CreatedBy)
		if err != nil || f == nil {
			log.Printf("[Share] batch file not found: %s", fullPath)
			failCount++
			continue
		}
		if strings.HasSuffix(f.Filename, ".keep") {
			continue // 跳过目录占位文件
		}
		// ZIP 内路径用相对分享根的路径（去掉 s.DirPrefix）
		relPath := strings.TrimPrefix(f.Filename, s.DirPrefix)
		if relPath == "" {
			continue
		}
		zf, err := zw.Create(relPath)
		if err != nil {
			log.Printf("[Share] batch zip create %s error: %v", relPath, err)
			failCount++
			continue
		}
		reader, err := h.storage.ReadFile(f.StoragePath, 0)
		if err != nil {
			log.Printf("[Share] batch read %s error: %v", f.StoragePath, err)
			failCount++
			continue
		}
		if _, err := io.Copy(zf, reader); err != nil {
			log.Printf("[Share] batch zip copy %s error: %v", relPath, err)
			failCount++
		} else {
			successCount++
		}
		reader.Close()
	}
	log.Printf("[Share] batch downloaded: id=%s success=%d fail=%d", id, successCount, failCount)
}

// saveShareToMyFiles 转存分享文件到自己的文件中心。
// POST /api/share/save  Body: {"share_id":"xxx","file_ids":["id1","id2"],"target_dir":"docs/"}
// 权限：必须登录；每个文件校验属于该分享范围（isFileInShare）。
// 失败回滚：CopyFile 失败时不创建 DB 记录；CreateFile 失败时删除已复制的存储文件。
// 同名冲突自动重命名（file.pdf → file_1.pdf）。
func (h *ShareHandler) saveShareToMyFiles(w http.ResponseWriter, r *http.Request, username string) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req struct {
		ShareID   string   `json:"share_id"`
		FileIDs   []string `json:"file_ids"`
		TargetDir string   `json:"target_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}
	if req.ShareID == "" || len(req.FileIDs) == 0 {
		http.Error(w, `{"error":"share_id_and_file_ids_required"}`, http.StatusBadRequest)
		return
	}
	if len(req.FileIDs) > 500 {
		http.Error(w, `{"error":"too_many_files","message":"单次最多转存 500 个文件"}`, http.StatusBadRequest)
		return
	}

	// 校验分享有效性
	s, err := h.db.GetShare(req.ShareID)
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

	// 规范化目标目录（如 "docs/" 或 "" 表示根目录）
	targetDir := normalizeDirPath(req.TargetDir)
	if targetDir != "" {
		// 校验目标目录路径合法性
		if err := validateFilePath(targetDir + ".keep"); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid_target_dir","message":"%s"}`, err.Error()), http.StatusBadRequest)
			return
		}
	}

	type result struct {
		FileID    string `json:"file_id"`
		Filename  string `json:"filename"`
		NewName   string `json:"new_name"`
		Size      int64  `json:"size"`
		Success   bool   `json:"success"`
		Error     string `json:"error,omitempty"`
	}
	results := make([]result, 0, len(req.FileIDs))
	var successCount, failCount int

	for _, fileID := range req.FileIDs {
		// 查询源文件
		f, err := h.db.GetFile(fileID)
		if err != nil {
			results = append(results, result{FileID: fileID, Success: false, Error: "file_not_found"})
			failCount++
			continue
		}
		// 校验文件属于该分享范围（防越权）
		if !isFileInShare(f, s) {
			results = append(results, result{FileID: fileID, Filename: f.Filename, Success: false, Error: "not_in_share"})
			failCount++
			continue
		}
		// 跳过目录占位文件
		if strings.HasSuffix(f.Filename, ".keep") {
			results = append(results, result{FileID: fileID, Filename: f.Filename, Success: false, Error: "is_directory_placeholder"})
			continue
		}

		// 构造新文件名：target_dir + 原文件 basename（去掉原路径前缀，只保留文件名）
		baseName := f.Filename
		if idx := strings.LastIndex(f.Filename, "/"); idx >= 0 {
			baseName = f.Filename[idx+1:]
		}
		newFilename := targetDir + baseName
		// 同 owner 下避免重名
		newFilename = generateUniqueFilename(newFilename, username, h.db)

		// 生成新 fileID 和 storage_path
		newFileID := generateID()
		dstStoragePath := h.storage.StoragePathFor(newFileID, newFilename)

		// 复制存储文件（失败则不创建 DB 记录）
		if err := h.storage.CopyFile(f.StoragePath, dstStoragePath); err != nil {
			log.Printf("[Share] save copy file error: src=%s dst=%s err=%v", f.StoragePath, dstStoragePath, err)
			results = append(results, result{FileID: fileID, Filename: f.Filename, Success: false, Error: "copy_failed"})
			failCount++
			continue
		}

		// 创建新的 DB 记录（owner=当前用户；失败则回滚存储文件）
		now := time.Now()
		newRecord := &model.FileRecord{
			ID:          newFileID,
			Filename:    newFilename,
			Size:        f.Size,
			Hash:        f.Hash,
			StoragePath: dstStoragePath,
			StorageType: f.StorageType,
			ChunkSize:   f.ChunkSize,
			TotalChunks: f.TotalChunks,
			Status:      "completed",
			Owner:       username,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := h.db.CreateFile(newRecord); err != nil {
			log.Printf("[Share] save create file record error: %v, rolling back storage", err)
			h.storage.DeleteFile(dstStoragePath) // 回滚
			results = append(results, result{FileID: fileID, Filename: f.Filename, Success: false, Error: "db_create_failed"})
			failCount++
			continue
		}

		results = append(results, result{
			FileID:   fileID,
			Filename: f.Filename,
			NewName:  newFilename,
			Size:     f.Size,
			Success:  true,
		})
		successCount++
	}

	log.Printf("[Share] save to my files: user=%s share=%s success=%d fail=%d", username, req.ShareID, successCount, failCount)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"saved":   true,
		"success": successCount,
		"fail":    failCount,
		"results": results,
	})
}

// generateShareID 生成 8 字符短 ID（16 字节随机数 hex 编码后取前 8 字符）
// 碰撞概率：62^8 ≈ 218 万亿种组合，足够使用
func generateShareID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b)[:8], nil
}

// getOrSetVisitorCookie 获取或设置 visitor cookie。
// cookie 名 "visitor"，值 32 字符 hex，HttpOnly, SameSite=Lax, 30 天有效。
// 跨所有分享复用同一 visitor ID（标识同一访客）。
func getOrSetVisitorCookie(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie("visitor")
	if err == nil && cookie.Value != "" {
		return cookie.Value
	}
	// 生成新 visitor ID
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 降级：用时间戳
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	visitorID := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     "visitor",
		Value:    visitorID,
		Path:     "/",
		MaxAge:   30 * 24 * 3600, // 30 天
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return visitorID
}

// resolveSharePath 规范化并校验分享内子路径，防止路径遍历逃逸。
// shareDirPrefix：分享目录前缀（如 "docs/"）；requestPath：访客请求的子路径（如 "sub/file.pdf" 或 ""）。
// 返回完整路径（如 "docs/sub/file.pdf"）。requestPath 为空时返回 shareDirPrefix。
// 校验：去开头/、合并//、validateFilePath、结果必须以 shareDirPrefix 为前缀。
func resolveSharePath(shareDirPrefix, requestPath string) (string, error) {
	// 规范化请求路径
	p := strings.TrimSpace(requestPath)
	p = strings.TrimPrefix(p, "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return shareDirPrefix, nil
	}
	// 校验路径合法性（禁止 .. \ 等）
	if err := validateFilePath(p); err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	// 拼接完整路径并校验前缀（双重防护）
	full := shareDirPrefix + p
	if !strings.HasPrefix(full, shareDirPrefix) {
		return "", fmt.Errorf("path escape detected")
	}
	return full, nil
}

// isFileInShare 校验文件是否属于分享范围。
// file 类型：f.ID == s.FileID；dir 类型：f.Filename 以 s.DirPrefix 为前缀。
// 用于转存接口防止越权转存非分享范围内的文件。
func isFileInShare(f *model.FileRecord, s *model.Share) bool {
	if s.ShareType == "file" {
		return f.ID == s.FileID
	}
	// dir 类型：filename 必须以分享前缀开头
	return s.DirPrefix != "" && strings.HasPrefix(f.Filename, s.DirPrefix)
}

// generateUniqueFilename 生成不冲突的文件名（同 owner 下）。
// 若 filename 已存在，自动追加 _1, _2, ... 后缀直到不冲突。
// 例：report.pdf → report_1.pdf → report_2.pdf
// 用于转存功能避免覆盖已有同名文件。
func generateUniqueFilename(filename, owner string, db *store.DB) string {
	// 快速路径：原名不冲突直接返回
	existing, _ := db.FindFileByName(filename, owner)
	if existing == nil {
		return filename
	}
	// 拆分扩展名
	ext := ""
	base := filename
	if dot := strings.LastIndex(filename, "."); dot > 0 {
		ext = filename[dot:]
		base = filename[:dot]
	}
	// 尝试 base_1.ext, base_2.ext, ... 最多 9999
	for i := 1; i < 10000; i++ {
		candidate := fmt.Sprintf("%s_%d%s", base, i, ext)
		existing, _ := db.FindFileByName(candidate, owner)
		if existing == nil {
			return candidate
		}
	}
	// 兜底：用时间戳
	return fmt.Sprintf("%s_%d%s", base, time.Now().Unix(), ext)
}
