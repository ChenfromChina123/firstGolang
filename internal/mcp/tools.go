package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"filesync/internal/agentsvc"
	"filesync/internal/auth"
	"filesync/internal/ws"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ============================================================
// FileSync MCP 工具注册（15 个）
// 每个工具通过 ToolContext 读取当前请求身份（由 http 包装 handler 注入）。
// scope 门禁：在工具 handler 内通过 requireScope 检查（REST 中间件已覆盖，
// 此处为 MCP 会话内的第二道防线 + 沙箱校验）。
// ============================================================

// ToolContext 是注入到工具 handler ctx 中的身份载体。
// 见 handlerWrap 中的注入逻辑。
type ToolContext struct {
	Username   string
	Role       string
	Scopes     string
	TokenID    string
	SpaceID    string // PAT 空间沙箱（空=不限定）
	PathPrefix string // PAT 目录沙箱（空=不限定）
	QuotaBytes int64
	QuotaUsed  int64
	BaseURL    string // 应用对外基础地址（由请求 Host 推导，用于生成绝对分享 URL）
}

// absURL 把相对路径补全为绝对 URL（使用请求推导的 BaseURL）。
func (tc *ToolContext) absURL(rel string) string {
	if tc == nil || tc.BaseURL == "" || rel == "" || strings.HasPrefix(rel, "http") {
		return rel
	}
	return strings.TrimSuffix(tc.BaseURL, "/") + rel
}

// toolCtxKey context 键
type toolCtxKey struct{}

// GetToolContext 从 ctx 取身份载体（无则返回 nil）。
func GetToolContext(ctx context.Context) *ToolContext {
	if v, ok := ctx.Value(toolCtxKey{}).(*ToolContext); ok {
		return v
	}
	return nil
}

// WithToolContext 把身份载体放入 ctx（供 tool handler 使用）。
func WithToolContext(ctx context.Context, tc *ToolContext) context.Context {
	return context.WithValue(ctx, toolCtxKey{}, tc)
}

// requireScope 校验工具所需 scope（任一命中即放行；admin 恒放行）。
func requireScope(tc *ToolContext, required ...string) error {
	if tc == nil {
		return errors.New("unauthorized: missing token context")
	}
	// 注意：PAT 即使底层是 admin 账号也严格按 token scopes 校验（G2 不继承账号完整权限）
	for _, r := range required {
		if auth.HasScope(tc.Scopes, r) {
			return nil
		}
	}
	return fmt.Errorf("insufficient_scope: token lacks required scope (need one of: %s)", strings.Join(required, ", "))
}

// sandbox 应用 PAT 空间 + 目录沙箱，返回最终 spaceID（被沙箱覆盖时用沙箱值）。
func sandbox(tc *ToolContext, spaceID, path string) (string, error) {
	finalSpace := spaceID
	if tc.SpaceID != "" {
		if spaceID != "" && spaceID != tc.SpaceID {
			return "", errors.New("space_id outside token sandbox")
		}
		finalSpace = tc.SpaceID
	}
	if err := agentsvc.EnforcePathSandbox(tc.PathPrefix, path); err != nil {
		return "", err
	}
	return finalSpace, nil
}

// normalizeSpace 普通用户空 space → 默认空间；admin 空 → "*"。
func normalizeSpace(tc *ToolContext, spaceID string) string {
	if spaceID == "" {
		if tc.Role == "admin" {
			return "*"
		}
		return "default-" + tc.Username
	}
	return spaceID
}

// ---- 输入结构（jsonschema 标签自动生成参数 schema） ----

type fsListInput struct {
	Path      string `json:"path,omitempty" jsonschema:"目录前缀,空根目录(如 docs/)"`
	SpaceID   string `json:"space_id,omitempty" jsonschema:"空间 ID(可选,默认我的空间)"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"true递归列出全部文件,false按目录分层"`
}

type fsStatInput struct {
	FileID string `json:"file_id,omitempty" jsonschema:"文件 ID(与 path 二选一)"`
	Path   string `json:"path,omitempty" jsonschema:"文件路径(与 file_id 二选一)"`
}

type fsReadInput struct {
	Path    string `json:"path,omitempty" jsonschema:"文件路径"`
	MaxSize int64  `json:"max_size,omitempty" jsonschema:"最大读取字节(默认 1MB,超大文件建议用 fs_download)"`
}

type fsDownloadInput struct {
	Path string `json:"path,omitempty" jsonschema:"文件路径"`
}

type fsWriteInput struct {
	Path      string `json:"path,omitempty" jsonschema:"目标文件路径(如 notes/todo.txt)"`
	Content   string `json:"content,omitempty" jsonschema:"文本内容"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"true覆盖已存在文件(默认 false 冲突报错)"`
	SpaceID   string `json:"space_id,omitempty" jsonschema:"空间 ID(可选)"`
}

type fsUploadInput struct {
	Path          string `json:"path,omitempty" jsonschema:"目标文件路径"`
	ContentBase64 string `json:"content_base64,omitempty" jsonschema:"文件内容 Base64(与 source_url 二选一)"`
	SourceURL     string `json:"source_url,omitempty" jsonschema:"可公开访问的源文件 URL(服务端拉取,与 content_base64 二选一)"`
	SpaceID       string `json:"space_id,omitempty" jsonschema:"空间 ID(可选)"`
}

type fsMkdirInput struct {
	Path    string `json:"path,omitempty" jsonschema:"目录路径(如 docs/notes/)"`
	SpaceID string `json:"space_id,omitempty" jsonschema:"空间 ID(可选)"`
}

type fsRenameInput struct {
	FileID  string `json:"file_id,omitempty" jsonschema:"文件 ID(与 path 二选一)"`
	Path    string `json:"path,omitempty" jsonschema:"原文件路径"`
	NewPath string `json:"new_path,omitempty" jsonschema:"新文件路径(同目录重命名)"`
}

type fsMoveInput struct {
	OldPrefix string `json:"old_prefix,omitempty" jsonschema:"源目录前缀(如 docs/)"`
	NewPrefix string `json:"new_prefix,omitempty" jsonschema:"目标目录前缀(如 archive/)"`
	SpaceID   string `json:"space_id,omitempty" jsonschema:"空间 ID(可选)"`
}

type fsDeleteInput struct {
	Paths   []string `json:"paths,omitempty" jsonschema:"要删除的文件路径列表"`
	FileIDs []string `json:"file_ids,omitempty" jsonschema:"要删除的文件 ID 列表"`
	SpaceID string   `json:"space_id,omitempty" jsonschema:"space ID"`
}

type fsTrashListInput struct {
	SpaceID string `json:"space_id,omitempty" jsonschema:"空间 ID(可选)"`
}

type fsTrashRestoreInput struct {
	FileID string `json:"file_id,omitempty" jsonschema:"回收站中的文件 ID"`
}

type shareCreateInput struct {
	FileID    string `json:"file_id,omitempty" jsonschema:"文件分享时必填(与 dir_prefix 二选一)"`
	DirPrefix string `json:"dir_prefix,omitempty" jsonschema:"目录分享时必填(如 docs/)"`
	ShareType string `json:"share_type,omitempty" jsonschema:"file 或 dir"`
	Password  string `json:"password,omitempty" jsonschema:"访问密码(可选,1-64 字符)"`
	ExpiresIn int64  `json:"expires_in,omitempty" jsonschema:"有效期秒(0永久)"`
	SpaceID   string `json:"space_id,omitempty" jsonschema:"目录分享的目标空间(可选)"`
}

type shareListInput struct{}

type shareDeleteInput struct {
	ShareID string `json:"share_id,omitempty" jsonschema:"分享 ID"`
}

type whoamiInput struct{}

// ---- 工具输出（结构化内容） ----

type result struct {
	OK      bool   `json:"ok,omitempty"`
	Message string `json:"message,omitempty"`
}

func okResult() *mcp.CallToolResult {
	res := &mcp.CallToolResult{}
	return res
}

// ---- 注册所有工具 ----

// registerTools 向 MCP server 注册 15 个工具。
func (s *MCP) registerTools(srv *mcp.Server, a *agentsvc.AgentSvc) {
	// ========== 3.1 文件基础（10） ==========

	addTool(s, srv, &mcp.Tool{
		Name:        "fs_list",
		Description: "列出目录内容(虚拟目录树)。返回子目录与文件列表。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fsListInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeRead); err != nil {
			return errResult(err), nil, nil
		}
		spaceID, err := sandbox(tc, normalizeSpace(tc, in.SpaceID), in.Path)
		if err != nil {
			return errResult(err), nil, nil
		}
		dirs, files, err := a.List(ctx, agentsvc.ListOptions{
			SpaceID: spaceID, Prefix: in.Path, Recursive: in.Recursive,
			Username: tc.Username, Role: tc.Role,
		})
		if err != nil {
			return errResult(err), nil, nil
		}
		return okJSON(map[string]any{"dirs": dirs, "files": files}), nil, nil
	})

	addTool(s, srv, &mcp.Tool{
		Name:        "fs_stat",
		Description: "获取文件元信息(大小/哈希/空间/归属)。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fsStatInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeRead); err != nil {
			return errResult(err), nil, nil
		}
		if in.FileID == "" && in.Path == "" {
			return errResult(errors.New("file_id 或 path 至少提供一个")), nil, nil
		}
		if _, err := sandbox(tc, "", in.Path); err != nil {
			return errResult(err), nil, nil
		}
		f, err := a.Stat(ctx, in.FileID, in.Path, normalizeSpace(tc, ""), tc.Username, tc.Role)
		if err != nil {
			return errResult(err), nil, nil
		}
		return okJSON(map[string]any{"file": f}), nil, nil
	})

	addTool(s, srv, &mcp.Tool{
		Name:        "fs_read",
		Description: "读取文件内容。文本直接返回;二进制或超大文件返回 base64(≤1MB 内联)。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fsReadInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeRead); err != nil {
			return errResult(err), nil, nil
		}
		if _, err := sandbox(tc, "", in.Path); err != nil {
			return errResult(err), nil, nil
		}
		f, err := a.Stat(ctx, "", in.Path, normalizeSpace(tc, ""), tc.Username, tc.Role)
		if err != nil {
			return errResult(err), nil, nil
		}
		maxSize := in.MaxSize
		if maxSize <= 0 {
			maxSize = 1024 * 1024 // 默认 1MB
		}
		data, err := a.Read(ctx, f, maxSize)
		if err != nil {
			return errResult(err), nil, nil
		}
		if looksBinary(data) {
			return okJSON(map[string]any{
				"filename":    f.Name,
				"size":        f.Size,
				"encoding":    "base64",
				"content_b64": base64.StdEncoding.EncodeToString(data),
			}), nil, nil
		}
		return okJSON(map[string]any{"filename": f.Name, "content": string(data), "size": f.Size}), nil, nil
	})

	addTool(s, srv, &mcp.Tool{
		Name:        "fs_download",
		Description: "为文件生成 30 分钟有效的下载链接(适合大文件,不内联返回内容)。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fsDownloadInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeRead); err != nil {
			return errResult(err), nil, nil
		}
		if _, err := sandbox(tc, "", in.Path); err != nil {
			return errResult(err), nil, nil
		}
		f, err := a.Stat(ctx, "", in.Path, normalizeSpace(tc, ""), tc.Username, tc.Role)
		if err != nil {
			return errResult(err), nil, nil
		}
		rec, err := a.StoragePath(ctx, f.ID, tc.Username, tc.Role)
		if err != nil {
			return errResult(err), nil, nil
		}
		url, err := s.downloadURL(rec)
		if err != nil {
			return errResult(err), nil, nil
		}
		return okJSON(map[string]any{
			"filename": f.Name, "size": f.Size,
			"download_url": tc.absURL(url), "expires_in": 1800,
		}), nil, nil
	})

	addTool(s, srv, &mcp.Tool{
		Name:        "fs_write",
		Description: "创建文本文件(一步到位,走 REST /api/files/create 同语义)。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fsWriteInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeWrite); err != nil {
			return errResult(err), nil, nil
		}
		spaceID, err := sandbox(tc, normalizeSpace(tc, in.SpaceID), in.Path)
		if err != nil {
			return errResult(err), nil, nil
		}
		f, err := a.WriteText(ctx, in.Path, in.Content, spaceID, tc.Username, in.Overwrite)
		if err != nil {
			return errResult(err), nil, nil
		}
		ws.PublishUpload(tc.Username, in.Path, spaceID, "fs_write", f.Size)
		return okJSON(map[string]any{"created": true, "file": f}), nil, nil
	})

	addTool(s, srv, &mcp.Tool{
		Name:        "fs_upload",
		Description: "上传二进制文件。content_base64(≤50MB) 或 source_url(服务端拉取,≤200MB)。内部聚合 init→chunk→complete 或秒传。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fsUploadInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeWrite); err != nil {
			return errResult(err), nil, nil
		}
		spaceID, err := sandbox(tc, normalizeSpace(tc, in.SpaceID), in.Path)
		if err != nil {
			return errResult(err), nil, nil
		}
		if in.ContentBase64 == "" && in.SourceURL == "" {
			return errResult(errors.New("content_base64 或 source_url 至少提供一个")), nil, nil
		}
		var data []byte
		if in.ContentBase64 != "" {
			data, err = base64.StdEncoding.DecodeString(in.ContentBase64)
			if err != nil {
				return errResult(errors.New("invalid base64 content")), nil, nil
			}
			if len(data) > 50*1024*1024 {
				return errResult(errors.New("base64 content too large (max 50MB)")), nil, nil
			}
		} else {
			data, err = fetchURL(in.SourceURL, 200*1024*1024)
			if err != nil {
				return errResult(err), nil, nil
			}
		}
		f, err := a.WriteBinary(ctx, in.Path, data, spaceID, tc.Username)
		if err != nil {
			return errResult(err), nil, nil
		}
		ws.PublishUpload(tc.Username, in.Path, spaceID, "fs_upload", f.Size)
		return okJSON(map[string]any{"uploaded": true, "file": f}), nil, nil
	})

	addTool(s, srv, &mcp.Tool{
		Name:        "fs_mkdir",
		Description: "创建目录(虚拟目录,写入 .keep 占位)。幂等:已存在则成功。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fsMkdirInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeWrite); err != nil {
			return errResult(err), nil, nil
		}
		spaceID, err := sandbox(tc, normalizeSpace(tc, in.SpaceID), in.Path)
		if err != nil {
			return errResult(err), nil, nil
		}
		if err := a.Mkdir(ctx, in.Path, spaceID, tc.Username); err != nil {
			return errResult(err), nil, nil
		}
		return okJSON(map[string]any{"created": true, "path": in.Path}), nil, nil
	})

	addTool(s, srv, &mcp.Tool{
		Name:        "fs_rename",
		Description: "重命名文件(同目录内改名,跨目录请用 fs_move)。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fsRenameInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeWrite); err != nil {
			return errResult(err), nil, nil
		}
		if _, err := sandbox(tc, "", in.Path); err != nil {
			return errResult(err), nil, nil
		}
		if _, err := sandbox(tc, "", in.NewPath); err != nil {
			return errResult(err), nil, nil
		}
		if err := a.Rename(ctx, in.FileID, in.Path, in.NewPath, tc.Username, tc.Role); err != nil {
			return errResult(err), nil, nil
		}
		return okJSON(map[string]any{"renamed": true, "new_path": in.NewPath}), nil, nil
	})

	addTool(s, srv, &mcp.Tool{
		Name:        "fs_move",
		Description: "批量移动:把 old_prefix 下的所有文件移到 new_prefix(改路径前缀)。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fsMoveInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeWrite); err != nil {
			return errResult(err), nil, nil
		}
		spaceID, err := sandbox(tc, normalizeSpace(tc, in.SpaceID), in.OldPrefix)
		if err != nil {
			return errResult(err), nil, nil
		}
		if _, err := sandbox(tc, spaceID, in.NewPrefix); err != nil {
			return errResult(err), nil, nil
		}
		moved, err := a.Move(ctx, in.OldPrefix, in.NewPrefix, spaceID, tc.Username, tc.Role)
		if err != nil {
			return errResult(err), nil, nil
		}
		return okJSON(map[string]any{"moved": moved > 0, "moved_count": moved, "from": in.OldPrefix, "to": in.NewPrefix}), nil, nil
	})

	addTool(s, srv, &mcp.Tool{
		Name:        "fs_delete",
		Description: "删除文件(软删除移入回收站,30 天保留)。paths 或 file_ids 批量。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fsDeleteInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeWrite); err != nil {
			return errResult(err), nil, nil
		}
		spaceID, err := sandbox(tc, normalizeSpace(tc, in.SpaceID), "")
		if err != nil {
			return errResult(err), nil, nil
		}
		for _, p := range in.Paths {
			if _, err := sandbox(tc, "", p); err != nil {
				return errResult(err), nil, nil
			}
		}
		deleted, err := a.Delete(ctx, in.Paths, in.FileIDs, spaceID, tc.Username, tc.Role)
		if err != nil {
			return errResult(err), nil, nil
		}
		return okJSON(map[string]any{"deleted": deleted}), nil, nil
	})

	// ========== 3.2 回收站（2） ==========

	addTool(s, srv, &mcp.Tool{
		Name:        "fs_trash_list",
		Description: "列出回收站中的文件(软删除,30 天保留)。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fsTrashListInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeRead); err != nil {
			return errResult(err), nil, nil
		}
		spaceID, err := sandbox(tc, normalizeSpace(tc, in.SpaceID), "")
		if err != nil {
			return errResult(err), nil, nil
		}
		items, err := a.TrashList(ctx, spaceID, tc.Username, tc.Role)
		if err != nil {
			return errResult(err), nil, nil
		}
		return okJSON(map[string]any{"trash": items}), nil, nil
	})

	addTool(s, srv, &mcp.Tool{
		Name:        "fs_trash_restore",
		Description: "从回收站恢复文件。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in fsTrashRestoreInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeWrite); err != nil {
			return errResult(err), nil, nil
		}
		if err := a.TrashRestore(ctx, in.FileID, tc.Username, tc.Role); err != nil {
			return errResult(err), nil, nil
		}
		return okJSON(map[string]any{"restored": true, "file_id": in.FileID}), nil, nil
	})

	// ========== 3.3 分享（3） ==========

	addTool(s, srv, &mcp.Tool{
		Name:        "share_create",
		Description: "创建分享链接(文件或目录)。支持访问密码与过期时间。返回可直接交付给用户的绝对 URL。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in shareCreateInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeShare); err != nil {
			return errResult(err), nil, nil
		}
		if _, err := sandbox(tc, "", in.DirPrefix); err != nil {
			return errResult(err), nil, nil
		}
		v, err := a.CreateShare(ctx, agentsvc.ShareCreateOptions{
			FileID: in.FileID, DirPrefix: in.DirPrefix, ShareType: in.ShareType,
			Password: in.Password, ExpiresIn: in.ExpiresIn,
			Username: tc.Username, Role: tc.Role, SpaceID: in.SpaceID,
		})
		if err != nil {
			return errResult(err), nil, nil
		}
		v.URL = tc.absURL(v.URL)
		return okJSON(map[string]any{"share": v}), nil, nil
	})

	addTool(s, srv, &mcp.Tool{
		Name:        "share_list",
		Description: "列出当前用户(或 token)创建的分享。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in shareListInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeShare); err != nil {
			return errResult(err), nil, nil
		}
		shares, err := a.ListShares(ctx, tc.Username)
		if err != nil {
			return errResult(err), nil, nil
		}
		for i := range shares {
			shares[i].URL = tc.absURL(shares[i].URL)
		}
		return okJSON(map[string]any{"shares": shares}), nil, nil
	})

	addTool(s, srv, &mcp.Tool{
		Name:        "share_delete",
		Description: "删除分享链接。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in shareDeleteInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if err := requireScope(tc, auth.ScopeShare); err != nil {
			return errResult(err), nil, nil
		}
		if err := a.DeleteShare(ctx, in.ShareID, tc.Username, tc.Role); err != nil {
			return errResult(err), nil, nil
		}
		return okJSON(map[string]any{"deleted": true, "share_id": in.ShareID}), nil, nil
	})

	// ========== 3.4 元信息（1） ==========

	addTool(s, srv, &mcp.Tool{
		Name:        "whoami",
		Description: "返回当前 token 身份、生效 scopes、空间/路径沙箱限制、配额使用情况。Agent 应优先调用以了解能力边界。",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in whoamiInput) (*mcp.CallToolResult, any, error) {
		tc := GetToolContext(ctx)
		if tc == nil {
			return errResult(errors.New("unauthorized")), nil, nil
		}
		id, err := a.Whoami(ctx, tc.Username, tc.Role, tc.Scopes, tc.TokenID, tc.SpaceID, tc.PathPrefix, tc.QuotaBytes, tc.QuotaUsed)
		if err != nil {
			return errResult(err), nil, nil
		}
		return okJSON(map[string]any{"identity": id}), nil, nil
	})
}

// errResult 构造错误工具结果（符合 MCP spec：IsError=true）。
func errResult(err error) *mcp.CallToolResult {
	res := &mcp.CallToolResult{}
	res.SetError(err)
	return res
}

// okJSON 构造成功结果，同时填充 StructuredContent 和 Content 数组。
// 重要：许多 MCP 客户端（Claude Desktop / Cursor / atomcode 等）只解析 content 数组，
// 不解析 structuredContent。若仅设置 structuredContent，客户端会看到 content:[] 空响应，
// 表现为「fs_read 返回空」「fs_list 返回空」等现象。
// 此处把结构化结果序列化为 JSON 文本同时放入 content，确保所有客户端都能读到结果。
func okJSON(v any) *mcp.CallToolResult {
	res := &mcp.CallToolResult{}
	raw, _ := jsonMarshal(v)
	res.StructuredContent = raw
	res.Content = []mcp.Content{&mcp.TextContent{Text: string(raw)}}
	return res
}
