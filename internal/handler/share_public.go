package handler

import (
	"encoding/json"
	"filesync/internal/auth"
	"filesync/internal/model"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"strings"
	"time"
)

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
	case "auth":
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		h.authenticateShare(w, r, id)
	case "download":
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		h.downloadShare(w, r, id)
	case "preview":
		// 公开预览：inline 模式 + Range 206，复用 token + 频率 + 密码校验，不计入下载次数
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		h.previewShare(w, r, id)
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
	var shareSpaceID string // 分享所在空间（文件=文件所属空间，目录=请求指定空间）
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
		shareSpaceID = f.SpaceID
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
		// 空间归一化（查询与记录语义分离）：
		// 读：admin 空 → SpaceAll 匹配所有空间；普通用户空 → 「我的空间」（default-<owner>）
		// 写：普通用户空 space_id 的目录分享记录存 "default-<username>"；admin 空保持 ''
		//     （旧分享兼容：下载时 space_id='' → 不过滤空间，配合 CreatedBy owner 过滤防泄露）
		resolvedSpace := resolveSpaceID(req.SpaceID, username, role)
		shareSpaceID = req.SpaceID
		if role != "admin" && shareSpaceID == "" {
			shareSpaceID = "default-" + username
		}
		files, err := h.db.ListFiles(resolvedSpace, req.DirPrefix, owner)
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

	// 处理访问密码（可选，1-64 字符）
	var passwordHash string
	if req.Password != "" {
		if len(req.Password) > 64 {
			http.Error(w, `{"error":"password_too_long","message":"密码长度不能超过 64 字符"}`, http.StatusBadRequest)
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			log.Printf("[Share] bcrypt password error: %v", err)
			http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
			return
		}
		passwordHash = string(hash)
	}

	// 创建分享记录
	s := &model.Share{
		ID:            shareID,
		FileID:        req.FileID,
		DirPrefix:     req.DirPrefix,
		ShareType:     req.ShareType,
		CreatedBy:     username,
		SpaceID:       shareSpaceID,
		CreatedAt:     time.Now(),
		ExpiresAt:     expiresAt,
		DownloadCount: 0,
		IsActive:      true,
		PasswordHash:  passwordHash,
	}
	if err := h.db.CreateShare(s); err != nil {
		log.Printf("[Share] create error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("[Share] created: id=%s type=%s name=%s user=%s has_password=%v", shareID, req.ShareType, name, username, passwordHash != "")

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
		ID            string `json:"id"`
		ShareType     string `json:"share_type"`
		Name          string `json:"name"`       // 文件名或目录名
		URL           string `json:"url"`        // 分享页面 URL
		CreatedAt     int64  `json:"created_at"` // Unix 时间戳
		ExpiresAt     *int64 `json:"expires_at,omitempty"`
		DownloadCount int    `json:"download_count"`
		IsActive      bool   `json:"is_active"`
		IsExpired     bool   `json:"is_expired"`
		HasPassword   bool   `json:"has_password"` // 是否设置了访问密码
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
			HasPassword:   s.PasswordHash != "",
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
	// 频率限制：每 IP 每分钟最多 60 次查询分享信息（防暴力枚举分享 ID）
	if !h.infoLimiter.Allow(r) {
		http.Error(w, `{"error":"rate_limited","message":"too many requests, try again later"}`, http.StatusTooManyRequests)
		return
	}

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
		files, err := h.db.ListFiles(shareSpaceID(s), s.DirPrefix, s.CreatedBy)
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
		HasPassword:   s.PasswordHash != "",
	}
	// 仅当无密码或已通过密码认证时才返回下载 token，防止未认证用户绕过密码门下载
	if s.PasswordHash == "" || h.isShareAuthed(r, s.ID) {
		info.DownloadToken = generateShareToken(s.ID, h.secret, shareTokenTTL)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// authenticateShare 验证分享访问密码，验证通过后设置 HMAC 签名 cookie（7 天有效）
// POST /api/s/{id}/auth  Body: {"password":"xxx"}
// 响应 200: {"success":true}（设置 share_auth_{id} cookie）
// 响应 401: {"error":"wrong_password","message":"密码错误"}
// 响应 400: 分享无密码 / 请求格式错误
func (h *ShareHandler) authenticateShare(w http.ResponseWriter, r *http.Request, id string) {
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
	if s.PasswordHash == "" {
		http.Error(w, `{"error":"no_password","message":"此分享无需密码"}`, http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}
	if req.Password == "" {
		http.Error(w, `{"error":"password_required","message":"请输入密码"}`, http.StatusBadRequest)
		return
	}

	// bcrypt 校验密码
	if err := bcrypt.CompareHashAndPassword([]byte(s.PasswordHash), []byte(req.Password)); err != nil {
		http.Error(w, `{"error":"wrong_password","message":"密码错误"}`, http.StatusUnauthorized)
		return
	}

	// 验证通过，设置 HMAC 签名 cookie（7 天有效）
	expire := time.Now().Add(shareAuthSessionTTL).Unix()
	cookieValue := h.signShareAuth(s.ID, expire)
	http.SetCookie(w, &http.Cookie{
		Name:     shareAuthCookieName(s.ID),
		Value:    cookieValue,
		Path:     "/",
		Expires:  time.Unix(expire, 0),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	log.Printf("[Share] auth success: id=%s", s.ID)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success":true}`))
}

// downloadShare 公开下载分享的文件或目录
// GET /api/s/{id}/download            - 下载单文件分享，或整目录 ZIP
// GET /api/s/{id}/download?path=子路径 - 下载目录分享中的单个文件
// 流程：token 校验 → 频率限制 → 检查有效性 → 设置 visitor cookie → 记录下载（去重）→ 流式下载
