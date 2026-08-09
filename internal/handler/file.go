package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/storage"
	"filesync/internal/store"
)

// FileHandler handles file listing, deletion, rename, mkdir
type FileHandler struct {
	db       *store.DB
	storage  storage.Storage            // Router，用于删除（按路径前缀路由）
	storages map[string]storage.Storage // 写入型（Mkdir 固定 local）
	redis    *store.RedisCache          // 可选 Redis 缓存（用于删除文件时同步清理冲突检查集合）
}

// NewFileHandler creates a new file handler. rc 可为 nil（无 Redis 时）。
func NewFileHandler(db *store.DB, s storage.Storage, storages map[string]storage.Storage, rc *store.RedisCache) *FileHandler {
	return &FileHandler{db: db, storage: s, storages: storages, redis: rc}
}

// spaceIDFromRequest 解析请求 query 中的 space_id 参数（仅解析，不做归一化）。
// 返回 db 层空间过滤语义：
//   - admin：不传或传 "all" → store.SpaceAll（"*"，不过滤，全部空间）
//   - 普通用户：不传 → ""（"我的空间"语义由 resolveSpaceID 归一化）
func spaceIDFromRequest(r *http.Request, role string) string {
	sid := strings.TrimSpace(fastQueryParam(r.URL.RawQuery, "space_id"))
	if role == "admin" && (sid == "" || sid == "all") {
		return store.SpaceAll
	}
	return sid
}

// resolveSpaceID 读操作（列表/删除目录/回收站/下载等）空间归一化：
//   - admin：空或 "all" → store.SpaceAll（全部空间，不过滤）
//   - 普通用户：空 → "default-<username>"（「我的空间」，与数据迁移后的文件空间一致）
//   - 其余：原样返回（具体空间 ID）
//
// 统一语义：空 space_id 即「我的空间」（default-<owner>），而非历史遗留的空字符串空间，
// 保证升级迁移后（文件已归入 default-<owner>）查询结果与旧数据完全一致。
func resolveSpaceID(spaceID, username, role string) string {
	if role == "admin" && (spaceID == "" || spaceID == "all") {
		return store.SpaceAll
	}
	if spaceID == "" {
		return "default-" + username
	}
	return spaceID
}

// normalizeWriteSpace 写操作（创建文件/目录/上传会话/分享）空间归一化：
// 空 space_id → "default-<username>"（「我的空间」）。
// 注意：SpaceAll 仅用于查询过滤，写入记录时必须归一化为具体空间，
// 因此写操作统一走本函数（admin 空 space_id 也归入 admin 的「我的空间」）。
func normalizeWriteSpace(spaceID, username string) string {
	if spaceID == "" {
		return "default-" + username
	}
	return spaceID
}

// ListFiles returns all completed files
// GET /api/files
// ListFiles 列出已完成文件。支持 prefix 查询参数按虚拟目录过滤（路径枚举）。
// 例：GET /api/files?prefix=docs/ 返回 docs/ 目录下所有文件（递归）。
// shallow=1 时按目录分层返回（只返回当前目录直接子项，不递归），响应格式为 {dirs:[],files:[]}。
// 注：必须用 fastQueryParam 解码（encodeURIComponent 编码 / 为 %2F）。
func (h *FileHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	prefix := fastQueryParam(r.URL.RawQuery, "prefix")
	shallow := fastQueryParam(r.URL.RawQuery, "shallow") == "1"
	// 空间隔离：admin 默认全部空间，普通用户默认「我的空间」，均可通过 space_id 参数指定
	spaceID := resolveSpaceID(spaceIDFromRequest(r, role), username, role)
	// admin 可见所有文件；普通用户仅可见自己的文件
	owner := username
	if role == "admin" {
		owner = ""
	}

	// shallow 模式：分层返回当前目录直接子项（dirs + files），不递归
	if shallow {
		dirs, files, err := h.db.ListDir(spaceID, prefix, owner)
		if err != nil {
			http.Error(w, "failed to list dir", http.StatusInternalServerError)
			return
		}
		fileResp := make([]model.FileInfoResponse, 0, len(files))
		for _, f := range files {
			fileResp = append(fileResp, model.FileInfoResponse{
				ID:          f.ID,
				Filename:    f.Filename,
				Size:        f.Size,
				Hash:        f.Hash,
				StoragePath: f.StoragePath,
				StorageType: f.StorageType,
				Owner:       f.Owner,
				Status:      f.Status,
				CreatedAt:   f.CreatedAt.Format(time.RFC3339),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(model.DirListingResponse{Dirs: dirs, Files: fileResp})
		return
	}

	// 默认模式：递归返回所有匹配文件（兼容 CLI 客户端）
	files, err := h.db.ListFiles(spaceID, prefix, owner)
	if err != nil {
		http.Error(w, "failed to list files", http.StatusInternalServerError)
		return
	}

	resp := make([]model.FileInfoResponse, 0, len(files))
	for _, f := range files {
		resp = append(resp, model.FileInfoResponse{
			ID:          f.ID,
			Filename:    f.Filename,
			Size:        f.Size,
			Hash:        f.Hash,
			StoragePath: f.StoragePath,
			StorageType: f.StorageType,
			Owner:       f.Owner,
			Status:      f.Status,
			CreatedAt:   f.CreatedAt.Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GetFileInfo returns details of a single file
// GET /api/files/{fileID}
func (h *FileHandler) GetFileInfo(w http.ResponseWriter, r *http.Request) {
	// If path is exactly /api/files or /api/files/, list all files
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/files" {
		h.ListFiles(w, r)
		return
	}

	fileID := strings.TrimPrefix(r.URL.Path, "/api/files/")
	fileID = strings.TrimSuffix(fileID, "/")

	if fileID == "" {
		h.ListFiles(w, r)
		return
	}

	f, err := h.db.GetFile(fileID)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// 权限校验：仅 owner 或 admin 可访问
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	if f.Owner != username && role != "admin" {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}

	resp := model.FileInfoResponse{
		ID:          f.ID,
		Filename:    f.Filename,
		Size:        f.Size,
		Hash:        f.Hash,
		StoragePath: f.StoragePath,
		StorageType: f.StorageType,
		Owner:       f.Owner,
		Status:      f.Status,
		CreatedAt:   f.CreatedAt.Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ServeHTTP routes file-related requests
func (h *FileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS 由全局 CORS 中间件统一处理（支持 credentials），此处不再设置局部 CORS 头

	path := strings.TrimSuffix(r.URL.Path, "/")

	switch {
	case path == "/api/files/mkdir" && r.Method == http.MethodPost:
		h.Mkdir(w, r)
		return
	case path == "/api/files/rename" && r.Method == http.MethodPost:
		h.Rename(w, r)
		return
	case path == "/api/files/move-dir" && r.Method == http.MethodPost:
		h.MoveDir(w, r)
		return
	case path == "/api/files/batch-delete" && r.Method == http.MethodPost:
		h.BatchDelete(w, r)
		return
	case path == "/api/files/create" && r.Method == http.MethodPost:
		h.CreateTextFile(w, r) // POST /api/files/create 创建文本文件（在线编辑）
		return
	case strings.HasSuffix(path, "/content") && strings.HasPrefix(path, "/api/files/") && r.Method == http.MethodGet:
		h.GetFileContent(w, r) // GET /api/files/{id}/content 获取文件内容（在线编辑）
		return
	case strings.HasSuffix(path, "/content") && strings.HasPrefix(path, "/api/files/") && r.Method == http.MethodPut:
		h.UpdateFileContent(w, r) // PUT /api/files/{id}/content 更新文件内容（在线编辑）
		return
	case path == "/api/files" && r.Method == http.MethodDelete:
		h.DeleteDir(w, r) // DELETE /api/files?prefix=xxx 删除目录
		return
	case strings.HasPrefix(path, "/api/files/") && r.Method == http.MethodDelete:
		h.DeleteFileByID(w, r) // DELETE /api/files/{id} 删除单个文件
		return
	}

	// 默认：GET /api/files（列表）或 /api/files/{id}（详情）
	h.GetFileInfo(w, r)
}

// normalizeDirPath 规范化目录路径：去开头 /、合并 //、末尾补 /。
// 注意：不对段做 trim——段内空格是合法的目录名（如 "open code"），
// trim 会破坏已存在的目录名，导致删除/列表匹配失败。
// 防止前导/尾随空格的工作由前端 mkdir 负责。
func normalizeDirPath(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "\\", "/")
	s = strings.TrimPrefix(s, "/")
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	if s != "" && !strings.HasSuffix(s, "/") {
		s += "/"
	}
	return s
}
