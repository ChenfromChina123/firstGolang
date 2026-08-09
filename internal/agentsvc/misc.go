package agentsvc

import (
	"context"
	"fmt"
	"strings"

	"filesync/internal/model"
)

// ============ 回收站 / 空间 / whoami ============

// TrashList 列出回收站文件。
func (s *AgentSvc) TrashList(ctx context.Context, spaceID, username, role string) ([]FileView, error) {
	owner := s.ownerFilter(username, role)
	files, err := s.db.ListTrashedFiles(spaceID, owner)
	if err != nil {
		return nil, fmtErr(err)
	}
	views := make([]FileView, 0, len(files))
	for _, f := range files {
		v := toFileView(f)
		if f.DeletedAt != nil {
			v.CreatedAt = f.DeletedAt.Format("2006-01-02 15:04:05") // 显示删除时间
		}
		views = append(views, v)
	}
	return views, nil
}

// TrashRestore 从回收站恢复文件。
func (s *AgentSvc) TrashRestore(ctx context.Context, fileID, username, role string) error {
	affected, err := s.db.RestoreFile(fileID, s.ownerFilter(username, role))
	if err != nil {
		return fmtErr(err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// SpaceList 列出用户空间。
func (s *AgentSvc) SpaceList(ctx context.Context, username, role string) ([]model.Space, error) {
	owner := username
	if role == "admin" {
		owner = ""
	}
	spaces, err := s.db.ListSpaces(owner)
	if err != nil {
		return nil, fmtErr(err)
	}
	return spaces, nil
}

// ============ whoami：agent 自我发现能力边界 ============

// Identity 当前 token 身份信息（MCP whoami 工具返回）。
type Identity struct {
	Username  string `json:"username"`
	Role      string `json:"role"`
	Scopes    string `json:"scopes"`
	IsPAT     bool   `json:"is_pat"`
	SpaceID   string `json:"space_id,omitempty"`   // PAT 空间沙箱（空=不限定）
	PathPrefix string `json:"path_prefix,omitempty"` // PAT 目录沙箱（空=不限定）
	QuotaBytes int64  `json:"quota_bytes"`         // 0=不限
	QuotaUsed  int64  `json:"quota_used"`
	SpaceList  []string `json:"spaces,omitempty"` // 可访问空间 ID 列表
}

// Whoami 汇总当前认证身份与能力边界。
func (s *AgentSvc) Whoami(ctx context.Context, username, role, scopes, tokenID, spaceID, pathPrefix string, quotaBytes, quotaUsed int64) (*Identity, error) {
	id := &Identity{
		Username:   username,
		Role:       role,
		Scopes:     scopes,
		IsPAT:      tokenID != "",
		SpaceID:    spaceID,
		PathPrefix: pathPrefix,
		QuotaBytes: quotaBytes,
		QuotaUsed:  quotaUsed,
	}
	// 可访问空间
	owner := username
	if role == "admin" {
		owner = ""
	}
	spaces, err := s.db.ListSpaces(owner)
	if err == nil {
		for _, sp := range spaces {
			id.SpaceList = append(id.SpaceList, sp.ID)
		}
	}
	return id, nil
}

// EnforcePathSandbox 校验请求路径在 PAT path_prefix 沙箱内。
// 越界返回 ErrPathTraversal。pathPrefix 空=不限定。
func EnforcePathSandbox(pathPrefix, target string) error {
	if pathPrefix == "" {
		return nil
	}
	prefix := normalizeDirPrefix(pathPrefix)
	target = strings.TrimPrefix(target, "/")
	if !strings.HasPrefix(target, prefix) {
		return fmt.Errorf("%w: token limited to prefix %q", ErrPathTraversal, prefix)
	}
	return nil
}
