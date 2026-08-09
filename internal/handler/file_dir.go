package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/storage"
)

// Mkdir 新建虚拟目录（POST /api/files/mkdir）。
// 路径枚举方案下，目录通过创建 .keep 占位文件实现存在。
// 前端和 ZIP 打包会过滤 .keep 文件，用户不可见。
func (h *FileHandler) Mkdir(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	var req struct {
		Path    string `json:"path"`
		SpaceID string `json:"space_id,omitempty"` // 目标空间 ID（空=「我的空间」）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	// 写操作空间归一化：空 → "default-<username>"（「我的空间」），与迁移后历史数据一致
	req.SpaceID = normalizeWriteSpace(req.SpaceID, username)

	// 规范化目录路径：去开头 /、合并 //、末尾补 /
	dirPath := normalizeDirPath(req.Path)
	if dirPath == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	// 校验目录路径（路径枚举规则）
	if err := validateFilePath(dirPath + ".keep"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 检查目录是否已存在：精确匹配 .keep 占位文件（显式创建的目录）。
	// 修复：不能用 ListFiles(prefix) 前缀递归判断——目录下已有文件（隐式目录，
	// 如用户上传到 docs/ 后目录 docs 已隐式存在）或深层子文件（如 a/b/report.pdf
	// 与创建目录 a/）会导致误报 409「目录已存在」，表现为创建子目录失败。
	// owner 视角统一：admin 的记录 owner 为空串，需用空 owner 精确查找。
	// 空间隔离：目录存在性检查限定在目标空间内。
	owner := username
	if role == "admin" {
		owner = ""
	}
	if existing, _ := h.db.FindFileByName(req.SpaceID, dirPath+".keep", owner); existing != nil {
		http.Error(w, "directory already exists", http.StatusConflict)
		return
	}

	// 创建 .keep 占位文件（存储键用 fileID 分片命名，与 filename 解耦）
	filename := dirPath + ".keep"
	sessionID := generateID()
	fileID := generateID()
	rawPath, err := h.storages["local"].AssembleFile(sessionID, fileID, filename, 0)
	if err != nil {
		log.Printf("mkdir assemble file %s error: %v", filename, err)
		http.Error(w, "failed to create directory", http.StatusInternalServerError)
		return
	}
	storagePath := storage.PrefixStoragePath("local", rawPath)

	now := time.Now()
	fileRecord := &model.FileRecord{
		ID:          fileID,
		Filename:    filename,
		Size:        0,
		Hash:        "",
		StoragePath: storagePath,
		StorageType: "local",
		ChunkSize:   512,
		TotalChunks: 0,
		Status:      "completed",
		Owner:       username,
		SpaceID:     req.SpaceID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := h.db.CreateFile(fileRecord); err != nil {
		log.Printf("mkdir create file record %s error: %v", filename, err)
		// 回滚：删除已创建的存储文件（storagePath 已带 local: 前缀）
		h.storage.DeleteFile(storagePath)
		http.Error(w, "failed to create directory record", http.StatusInternalServerError)
		return
	}

	log.Printf("mkdir: created directory %s (.keep placeholder)", dirPath)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"created": true,
		"path":    dirPath,
	})
}

// Rename 重命名/移动文件（POST /api/files/rename）。
// 修改 filename 字段（含路径前缀），实现文件移动到其他目录。
// 存储文件不移动（storage_path 不变），仅改 filename 虚拟路径。
func (h *FileHandler) Rename(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	var req struct {
		ID          string `json:"id"`
		NewFilename string `json:"new_filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	if req.ID == "" || req.NewFilename == "" {
		http.Error(w, "id and new_filename required", http.StatusBadRequest)
		return
	}

	// 校验新文件名路径
	if err := validateFilePath(req.NewFilename); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 检查文件是否存在
	f, err := h.db.GetFile(req.ID)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// 权限校验：仅 owner 或 admin 可重命名
	if f.Owner != username && role != "admin" {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}

	// 检查新文件名是否冲突（同名校验，按当前用户 + 文件所在空间过滤）
	if f.Filename != req.NewFilename {
		existing, _ := h.db.FindFileByName(f.SpaceID, req.NewFilename, username)
		if existing != nil {
			http.Error(w, fmt.Sprintf("file '%s' already exists", req.NewFilename), http.StatusConflict)
			return
		}
	}

	// 更新文件名（storage_path 不变，仅改虚拟路径）
	ownerForUpdate := username
	if role == "admin" {
		ownerForUpdate = ""
	}
	if err := h.db.UpdateFilename(req.ID, req.NewFilename, ownerForUpdate); err != nil {
		log.Printf("rename file %s error: %v", req.ID, err)
		http.Error(w, "failed to rename file", http.StatusInternalServerError)
		return
	}

	// 同步更新 Redis 冲突检查集合：旧文件名移除，新文件名加入
	if h.redis != nil {
		if err := h.redis.UnmarkFileExists(r.Context(), f.Filename); err != nil {
			log.Printf("redis unmark old file %s error (non-fatal): %v", f.Filename, err)
		}
		if err := h.redis.MarkFileExists(r.Context(), req.NewFilename); err != nil {
			log.Printf("redis mark new file %s error (non-fatal): %v", req.NewFilename, err)
		}
	}

	log.Printf("rename: %s -> %s", f.Filename, req.NewFilename)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"renamed":      true,
		"id":           req.ID,
		"old_filename": f.Filename,
		"new_filename": req.NewFilename,
	})
}

// MoveDir 移动目录（POST /api/files/move-dir）。
// 批量更新目录下所有文件的 filename 前缀，实现目录移动。
// 存储文件不移动（storage_path 不变），仅改 filename 虚拟路径。
// 权限：admin 可移动任意目录，普通用户仅可移动自己的目录。
// 请求体：{"old_prefix":"old_dir/","new_prefix":"new_dir/"}
func (h *FileHandler) MoveDir(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	var req struct {
		OldPrefix string `json:"old_prefix"`
		NewPrefix string `json:"new_prefix"`
		SpaceID   string `json:"space_id,omitempty"` // 目录所在空间 ID（空=「我的空间」）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	// 空间归一化：空 → "default-<username>"（「我的空间」），admin 空 → SpaceAll（移动任意空间）
	req.SpaceID = resolveSpaceID(req.SpaceID, username, role)
	req.OldPrefix = normalizeDirPath(req.OldPrefix)
	req.NewPrefix = normalizeDirPath(req.NewPrefix)
	if req.OldPrefix == "" || req.NewPrefix == "" {
		http.Error(w, "old_prefix and new_prefix required", http.StatusBadRequest)
		return
	}
	if req.OldPrefix == req.NewPrefix {
		http.Error(w, "old_prefix and new_prefix are the same", http.StatusBadRequest)
		return
	}

	// admin 可见所有；普通用户仅自己的（owner 过滤）
	owner := username
	if role == "admin" {
		owner = ""
	}

	// 获取源目录下所有文件（限定在目标空间内）
	files, err := h.db.ListFiles(req.SpaceID, req.OldPrefix, owner)
	if err != nil {
		http.Error(w, "failed to list files", http.StatusInternalServerError)
		return
	}
	if len(files) == 0 {
		http.Error(w, "source directory is empty or not found", http.StatusNotFound)
		return
	}

	// 检查目标目录是否已有同名文件（冲突检查，按当前用户 + 空间过滤）
	targetFiles, _ := h.db.ListFiles(req.SpaceID, req.NewPrefix, owner)
	if len(targetFiles) > 0 {
		http.Error(w, "target directory already has files", http.StatusConflict)
		return
	}

	// 批量更新 filename 前缀（ownerForUpdate 与查询保持一致）
	ownerForUpdate := owner
	var successCount, failCount int
	for _, f := range files {
		newFilename := req.NewPrefix + strings.TrimPrefix(f.Filename, req.OldPrefix)
		if err := h.db.UpdateFilename(f.ID, newFilename, ownerForUpdate); err != nil {
			log.Printf("move file %s error: %v", f.ID, err)
			failCount++
			continue
		}
		// 同步更新 Redis 冲突检查集合
		if h.redis != nil {
			h.redis.UnmarkFileExists(r.Context(), f.Filename)
			h.redis.MarkFileExists(r.Context(), newFilename)
		}
		successCount++
	}

	log.Printf("move-dir: %s -> %s (%d success, %d fail)", req.OldPrefix, req.NewPrefix, successCount, failCount)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"moved":      true,
		"old_prefix": req.OldPrefix,
		"new_prefix": req.NewPrefix,
		"success":    successCount,
		"fail":       failCount,
	})
}
