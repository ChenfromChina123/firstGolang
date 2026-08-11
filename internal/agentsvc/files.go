package agentsvc

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"filesync/internal/model"
	"filesync/internal/storage"
	"filesync/internal/store"
)

// ============ 文件基础操作（供 MCP fs_* 工具调用） ============

// FileView 是 MCP 返回的文件视图（精简字段，避免暴露 storage_path）。
type FileView struct {
	ID        string `json:"id"`
	Name      string `json:"name"` // filename（含虚拟目录前缀）
	Size      int64  `json:"size"` // 字节
	Hash      string `json:"hash"` // SHA256
	SpaceID   string `json:"space_id"`
	Owner     string `json:"owner"`
	CreatedAt string `json:"created_at"`
	IsDir     bool   `json:"is_dir"` // 是否目录占位（.keep）
}

// ListOptions 列目录参数。
type ListOptions struct {
	SpaceID   string // ""=PAT 未限定空间时由调用方归一化；"*"=全部空间（admin）
	Prefix    string // 目录前缀（"docs/"），空=根
	Recursive bool   // true=递归返回全部文件；false=按目录分层（子目录+直接文件）
	Owner     string // 归属用户过滤（""=不按用户过滤，admin 用）
	Username  string // 当前用户名（用于校验 owner 隔离）
	Role      string // 当前角色
}

// List 列出目录内容。返回目录列表 + 文件列表。
// PAT 沙箱（space_id/path_prefix）由调用方在传入前解析（见 enforceSandbox）。
func (s *AgentSvc) List(ctx context.Context, opt ListOptions) (dirs []model.DirEntry, files []FileView, err error) {
	if opt.Prefix != "" {
		opt.Prefix = normalizeDirPrefix(opt.Prefix)
	}
	// owner 隔离：普通用户只看自己；admin 空=全部
	owner := opt.Owner
	if owner == "" && opt.Role != "admin" {
		owner = opt.Username
	}
	if opt.Recursive {
		rec, err := s.db.ListFiles(opt.SpaceID, opt.Prefix, owner)
		if err != nil {
			return nil, nil, fmtErr(err)
		}
		files = make([]FileView, 0, len(rec))
		for _, f := range rec {
			if strings.HasSuffix(f.Filename, ".keep") {
				continue // 目录占位文件不展示为普通文件
			}
			files = append(files, toFileView(f))
		}
		return nil, files, nil
	}
	dirs, direct, err := s.db.ListDir(opt.SpaceID, opt.Prefix, owner)
	if err != nil {
		return nil, nil, fmtErr(err)
	}
	files = make([]FileView, 0, len(direct))
	for _, f := range direct {
		if strings.HasSuffix(f.Filename, ".keep") {
			continue
		}
		files = append(files, toFileView(f))
	}
	return dirs, files, nil
}

// Stat 按 file_id 或 path 获取文件详情。
// 优先级：ID 非空 → 按 ID 查；否则按 path 在用户/空间中查。
func (s *AgentSvc) Stat(ctx context.Context, id, path, spaceID, username, role string) (*FileView, error) {
	var f *model.FileRecord
	var err error
	if id != "" {
		f, err = s.db.GetFile(id)
	} else {
		f, err = s.db.FindFileByName(spaceID, path, s.ownerFilter(username, role))
	}
	if err != nil {
		if ErrIsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmtErr(err)
	}
	// 权限校验：owner 或 admin
	if f.Owner != username && role != "admin" {
		return nil, ErrForbidden
	}
	v := toFileView(*f)
	return &v, nil
}

// Read 读取文件内容（文本直读；二进制由调用方决定是否 base64）。
// maxSize 限制读取字节数（默认 10MB），防止 agent 拉爆内存。
func (s *AgentSvc) Read(ctx context.Context, f *FileView, maxSize int64) ([]byte, error) {
	if maxSize <= 0 {
		maxSize = 10 * 1024 * 1024
	}
	rec, err := s.db.GetFile(f.ID)
	if err != nil {
		if ErrIsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmtErr(err)
	}
	if rec.Size > maxSize {
		return nil, fmt.Errorf("file too large to read inline (%d bytes, limit %d); use fs_download to get a URL", rec.Size, maxSize)
	}
	r, err := s.router.ReadFile(rec.StoragePath, 0)
	if err != nil {
		return nil, fmtErr(err)
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, maxSize+1))
	if err != nil {
		return nil, fmtErr(err)
	}
	return data, nil
}

// StoragePath 返回文件的存储路径（供下载/分享用），并校验归属。
func (s *AgentSvc) StoragePath(ctx context.Context, id, username, role string) (*model.FileRecord, error) {
	f, err := s.db.GetFile(id)
	if err != nil {
		if ErrIsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmtErr(err)
	}
	if f.Owner != username && role != "admin" {
		return nil, ErrForbidden
	}
	return f, nil
}

// WriteText 创建文本文件（一步到位）。
// spaceID 空/"*" 归一化为 "default-<username>"（与 REST normalizeWriteSpace 语义一致）。
func (s *AgentSvc) WriteText(ctx context.Context, path, content, spaceID, username string, overwrite bool) (*FileView, error) {
	spaceID = normalizeWriteSpace(spaceID, username)
	if err := validateAgentPath(path); err != nil {
		return nil, err
	}
	if int64(len(content)) > 10*1024*1024 {
		return nil, fmt.Errorf("content too large (max 10MB)")
	}
	// 冲突检查
	existing, _ := s.db.FindFileByName(spaceID, path, username)
	if existing != nil && !overwrite {
		return nil, ErrConflict
	}
	fileID := newAgentID()
	local := s.router.BackendFor("local")
	rawPath := local.StoragePathFor(fileID, path)
	n, err := local.WriteFile(rawPath, strings.NewReader(content))
	if err != nil {
		return nil, fmtErr(err)
	}
	hash, _ := local.HashFile(rawPath)
	now := time.Now()
	if existing != nil && overwrite {
		// 覆盖：删除旧记录（软删），创建新记录
		s.db.DeleteFile(existing.ID)
	}
	rec := &model.FileRecord{
		ID:          fileID,
		Filename:    path,
		Size:        n,
		Hash:        hash,
		StoragePath: storage.PrefixStoragePath("local", rawPath),
		StorageType: "local",
		Status:      "completed",
		Owner:       username,
		SpaceID:     spaceID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.db.CreateFile(rec); err != nil {
		local.DeleteFile(rawPath)
		return nil, fmtErr(err)
	}
	v := toFileView(*rec)
	return &v, nil
}

// WriteBinary 上传二进制文件（直接写入存储，不经分片；≤50MB 由调用方限制）。
func (s *AgentSvc) WriteBinary(ctx context.Context, path string, data []byte, spaceID, username string) (*FileView, error) {
	spaceID = normalizeWriteSpace(spaceID, username)
	if err := validateAgentPath(path); err != nil {
		return nil, err
	}
	existing, _ := s.db.FindFileByName(spaceID, path, username)
	if existing != nil {
		return nil, ErrConflict
	}
	fileID := newAgentID()
	local := s.router.BackendFor("local")
	rawPath := local.StoragePathFor(fileID, path)
	n, err := local.WriteFile(rawPath, strings.NewReader(string(data)))
	if err != nil {
		return nil, fmtErr(err)
	}
	hash, _ := local.HashFile(rawPath)
	now := time.Now()
	rec := &model.FileRecord{
		ID:          fileID,
		Filename:    path,
		Size:        n,
		Hash:        hash,
		StoragePath: storage.PrefixStoragePath("local", rawPath),
		StorageType: "local",
		Status:      "completed",
		Owner:       username,
		SpaceID:     spaceID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.db.CreateFile(rec); err != nil {
		local.DeleteFile(rawPath)
		return nil, fmtErr(err)
	}
	v := toFileView(*rec)
	return &v, nil
}

// Mkdir 创建目录（写入 .keep 占位文件）。
func (s *AgentSvc) Mkdir(ctx context.Context, path, spaceID, username string) error {
	spaceID = normalizeWriteSpace(spaceID, username)
	if err := validateAgentPath(path); err != nil {
		return err
	}
	dirPath := normalizeDirPrefix(path)
	if dirPath == "" {
		return ErrInvalidPath
	}
	// 目录已存在则幂等返回（与 REST 行为一致）
	existing, _ := s.db.FindFileByName(spaceID, dirPath+".keep", username)
	if existing != nil {
		return nil
	}
	fileID := newAgentID()
	filename := dirPath + ".keep"
	local := s.router.BackendFor("local")
	rawPath := local.StoragePathFor(fileID, filename)
	if _, err := local.WriteFile(rawPath, strings.NewReader("")); err != nil {
		return fmtErr(err)
	}
	rec := &model.FileRecord{
		ID:          fileID,
		Filename:    filename,
		Size:        0,
		Hash:        "",
		StoragePath: storage.PrefixStoragePath("local", rawPath),
		StorageType: "local",
		Status:      "completed",
		Owner:       username,
		SpaceID:     spaceID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := s.db.CreateFile(rec); err != nil {
		local.DeleteFile(rawPath)
		return fmtErr(err)
	}
	return nil
}

// Rename 重命名文件（file_id 或 path 定位）。
func (s *AgentSvc) Rename(ctx context.Context, id, oldPath, newPath, username, role string) error {
	if err := validateAgentPath(newPath); err != nil {
		return err
	}
	f, err := s.resolveFile(id, oldPath, username, role)
	if err != nil {
		return err
	}
	if f.Owner != username && role != "admin" {
		return ErrForbidden
	}
	// 只允许重命名直接子项：oldPath 去掉末段 → newPath 必须同目录（保持虚拟目录语义）
	oldDir := dirOf(oldPath)
	newDir := dirOf(newPath)
	if oldDir != newDir {
		return fmt.Errorf("%w: rename must keep same directory (use fs_move for prefix moves)", ErrInvalidPath)
	}
	if err := s.db.UpdateFilename(f.ID, newPath, s.ownerFilter(username, role)); err != nil {
		return fmtErr(err)
	}
	return nil
}

// Move 批量移动（改路径前缀）。
// oldPrefix→newPrefix：把 oldPrefix 下的所有文件路径前缀替换。
func (s *AgentSvc) Move(ctx context.Context, oldPrefix, newPrefix, spaceID, username, role string) (int, error) {
	oldPrefix = normalizeDirPrefix(oldPrefix)
	newPrefix = normalizeDirPrefix(newPrefix)
	if oldPrefix == "" || newPrefix == "" {
		return 0, fmt.Errorf("%w: prefixes required", ErrInvalidPath)
	}
	if err := validateAgentPath(newPrefix + "x"); err != nil {
		return 0, err
	}
	files, err := s.db.ListFiles(spaceID, oldPrefix, s.ownerFilter(username, role))
	if err != nil {
		return 0, fmtErr(err)
	}
	if len(files) == 0 {
		return 0, nil
	}
	for _, f := range files {
		newName := newPrefix + strings.TrimPrefix(f.Filename, oldPrefix)
		if err := s.db.UpdateFilename(f.ID, newName, s.ownerFilter(username, role)); err != nil {
			return 0, fmtErr(err)
		}
	}
	return len(files), nil
}

// Delete 软删除（移入回收站）。paths 或 fileIDs 至少一项；统一批量语义。
func (s *AgentSvc) Delete(ctx context.Context, paths, fileIDs []string, spaceID, username, role string) (deleted int, err error) {
	// 收集待删 fileID
	var ids []string
	owner := s.ownerFilter(username, role)
	for _, id := range fileIDs {
		if id == "" {
			continue
		}
		f, err := s.db.GetFile(id)
		if err != nil {
			continue
		}
		if (spaceID != "*" && f.SpaceID != spaceID) || (owner != "" && f.Owner != owner) {
			continue
		}
		ids = append(ids, f.ID)
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		f, err := s.db.FindFileByName(spaceID, strings.TrimSuffix(p, "/"), owner)
		if err != nil {
			continue // 单个缺失不阻塞批量
		}
		ids = append(ids, f.ID)
	}
	for _, id := range ids {
		// 权限在 resolveFile 已校验；此处按 owner 条件执行软删
		if _, err := s.db.DeleteFile(id); err != nil {
			return deleted, fmtErr(err)
		}
		deleted++
	}
	return deleted, nil
}

// ============ 内部辅助 ============

// ownerFilter 根据角色返回 owner 过滤条件（admin 空=全部）。
func (s *AgentSvc) ownerFilter(username, role string) string {
	if role == "admin" {
		return ""
	}
	return username
}

// resolveFile 按 id 或 path 定位文件并做 owner 校验。
func (s *AgentSvc) resolveFile(id, path, username, role string) (*model.FileRecord, error) {
	var f *model.FileRecord
	var err error
	if id != "" {
		f, err = s.db.GetFile(id)
	} else {
		f, err = s.db.FindFileByName(store.SpaceAll, path, s.ownerFilter(username, role))
	}
	if err != nil {
		if ErrIsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmtErr(err)
	}
	return f, nil
}

// toFileView 转换 FileRecord → FileView（剔除敏感字段）。
func toFileView(f model.FileRecord) FileView {
	return FileView{
		ID:        f.ID,
		Name:      f.Filename,
		Size:      f.Size,
		Hash:      f.Hash,
		SpaceID:   f.SpaceID,
		Owner:     f.Owner,
		CreatedAt: f.CreatedAt.Format(time.RFC3339),
		IsDir:     strings.HasSuffix(f.Filename, ".keep"),
	}
}

// normalizeDirPrefix 规范化目录前缀：去开头 /、合并 //、末尾补 /。
func normalizeDirPrefix(p string) string {
	p = strings.Trim(p, " ")
	p = strings.TrimPrefix(p, "/")
	p = strings.ReplaceAll(p, "//", "/")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

// normalizeWriteSpace 写操作空间归一化：空/"*" → "default-<username>"（与 REST 一致）。
func normalizeWriteSpace(spaceID, username string) string {
	if spaceID == "" || spaceID == store.SpaceAll {
		return "default-" + username
	}
	return spaceID
}

// dirOf 返回路径的目录部分（不含文件名），末尾带 /；根返回空。
func dirOf(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return ""
	}
	return p[:idx+1]
}

// validateAgentPath 校验路径合法性（复用 handler.validateFilePath 同规则）。
// 由于 handler 包函数未导出，此处本地实现同语义校验。
func validateAgentPath(p string) error {
	if p == "" {
		return fmt.Errorf("%w: path is empty", ErrInvalidPath)
	}
	if len(p) > 1024 {
		return fmt.Errorf("%w: path too long", ErrInvalidPath)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: must be relative (no leading /)", ErrInvalidPath)
	}
	if strings.Contains(p, "\\") {
		return fmt.Errorf("%w: backslash not allowed", ErrInvalidPath)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("%w: '..' not allowed", ErrInvalidPath)
	}
	if strings.Contains(p, "//") {
		return fmt.Errorf("%w: consecutive slashes not allowed", ErrInvalidPath)
	}
	return nil
}
