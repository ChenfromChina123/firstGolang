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
// GET /api/s/{id}          - 获取分享信息
// GET /api/s/{id}/download - 下载
func (h *ShareHandler) handlePublic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/s/")
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		http.Error(w, `{"error":"id_required"}`, http.StatusBadRequest)
		return
	}

	// 分离 id 和 download 后缀
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	isDownload := len(parts) == 2 && parts[1] == "download"

	if isDownload {
		h.downloadShare(w, r, id)
	} else {
		h.getSharePublic(w, r, id)
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
// GET /api/s/{id}/download
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
	} else {
		h.downloadSharedDir(w, r, s)
	}
}

// downloadSharedFile 下载单文件（复用 storage.ReadFile）
func (h *ShareHandler) downloadSharedFile(w http.ResponseWriter, r *http.Request, s *model.Share) {
	f, err := h.db.GetFile(s.FileID)
	if err != nil {
		http.Error(w, `{"error":"file_deleted"}`, http.StatusNotFound)
		return
	}

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
