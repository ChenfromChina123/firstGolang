// Package mcp 实现 FileSync MCP Server。
//
// 支持两种 transport：
//   - stdio（本地 agent，Claude Desktop / Cursor）
//   - Streamable HTTP（POST /mcp，远程 agent）
//
// 认证：PAT（Bearer fsk_*）由 HTTP 包装 handler 验证后注入 ToolContext；
// stdio 模式从环境变量 FILESYNC_PAT 读取。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"filesync/internal/agentsvc"
	"filesync/internal/model"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP 持有 AgentSvc 引用并负责工具注册。
type MCP struct {
	svc   *agentsvc.AgentSvc
	tools []*mcp.Tool // 已注册工具（含自动生成的 InputSchema），供 manifest 导出
}

const serverInstructions = `FileSync MCP Server：个人仓库文件管理、回收站与分享。

新会话入职规则（必须执行）：
1. 在当前 AI 会话第一次使用 FileSync MCP 时，先调用 whoami 获取令牌绑定的空间与权限。
2. 随后调用 fs_read，读取路径 _index/mcp-onboarding.md。
3. 将该文档作为当前空间的欢迎词、操作手册和维护规则载入当前会话，并遵守其中要求。手册 mode 可为 file-manager、repository 或 custom。
4. 同一 AI 会话只加载一次，不要在后续工具调用中重复展示；新的 AI 会话重新加载，以便获取用户更新后的最新版本。
5. 如果手册不存在：
   - 默认按 file-manager（普通文件管理）模式处理，不得把空间擅自解释为个人知识仓库。
   - 当前令牌有 filesync:write 权限时，先调用 fs_mkdir 创建 _index，再调用 fs_write 创建 _index/mcp-onboarding.md。默认手册必须声明 mode: file-manager，并说明：不自动创建 docs/skills/projects 等仓库目录、不自动移动或删除文件、整理前先征求用户确认、删除使用软删除、用户可随时编辑本文件。创建后立即读取并载入。
   - 当前令牌只有只读权限时，不尝试写入；继续遵守 whoami 返回的权限边界，并明确告诉用户需要 write 权限才能自动初始化。
   - 自动初始化必须幂等：已有目录或手册时不得覆盖，不得创建重复文件。
6. 仅当用户明确要求启用个人仓库、手册声明 mode: repository，或用户确认已有 docs/skills/projects/_index 等结构属于个人仓库时，才应用仓库分类、索引和自动维护规则。不得只凭目录存在就进行移动、删除或批量重构。

用户可以在仓库页面直接编辑这份手册，也可以通过 fs_write 更新它。任何文件操作都必须遵守 PAT 的空间、路径和 scope 沙箱。`

// New 创建 MCP 服务实例。
func New(svc *agentsvc.AgentSvc) *MCP {
	return &MCP{svc: svc}
}

// Server 构建并注册全部工具的 MCP server（stdio 与 HTTP 共用）。
func (s *MCP) Server() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "filesync",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Instructions: serverInstructions,
	})
	s.registerTools(srv, s.svc)
	return srv
}

// RegisteredTools 返回已注册工具列表（含 InputSchema），供 manifest 生成。
func (s *MCP) RegisteredTools() []*mcp.Tool { return s.tools }

// addTool 注册工具并收集定义（供 manifest 自动导出，避免文档漂移）。
func addTool[In, Out any](s *MCP, srv *mcp.Server, tool *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	if tool.InputSchema == nil {
		// 用 jsonschema-go 反射生成参数 schema（与 SDK 内部同源）
		if sch, err := jsonschema.ForType(reflect.TypeFor[In](), &jsonschema.ForOptions{}); err == nil {
			tool.InputSchema = sch
		}
	}
	s.tools = append(s.tools, tool)
	mcp.AddTool(srv, tool, h)
}

// HTTPHandler 返回可挂载到 /mcp 的 Streamable HTTP handler。
// stateless 模式：每个请求独立认证（Authorization: Bearer fsk_*）。
// server 实例只构建一次并缓存复用——每次请求重建会触发客户端
// 重新 initialize / 重新加载工具（表现为「刷新客户端 UI 全部重建」）。
func (s *MCP) HTTPHandler() http.Handler {
	srv := s.Server()
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return srv
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
}

// downloadURL 生成 30 分钟有效的分享式下载链接。
// 复用分享公开路由（/api/s/{id}/download?token=...）：
// 直接创建带 30 分钟过期的 file 分享，返回其访问路径。
func (s *MCP) downloadURL(f *model.FileRecord) (string, error) {
	// 用分享机制生成临时链接（带过期），确保外部可访问 + 防盗链
	v, err := s.svc.CreateShare(context.Background(), agentsvc.ShareCreateOptions{
		FileID:    f.ID,
		ShareType: "file",
		ExpiresIn: 1800,
		Username:  f.Owner,
		Role:      "user",
	})
	if err != nil {
		return "", err
	}
	return v.URL, nil
}

// ============ 工具 helper ============

// jsonMarshal 序列化结构化内容（无法失败，仅内部使用）。
func jsonMarshal(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// looksBinary 判断字节流是否含二进制内容（NUL 或高比例控制字符）。
func looksBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	const sample = 512
	n := len(data)
	if n > sample {
		n = sample
	}
	ctrl := 0
	for _, b := range data[:n] {
		if b == 0 {
			return true
		}
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			ctrl++
		}
	}
	return ctrl > 2
}

// fetchURL 下载远程 URL 内容（fs_upload source_url 用）。
// maxBytes 限制大小，默认 200MB。仅允许 http/https。
func fetchURL(rawURL string, maxBytes int64) ([]byte, error) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return nil, fmt.Errorf("source_url must be http(s)://")
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	if maxBytes <= 0 {
		maxBytes = 200 * 1024 * 1024
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("fetch source_url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch source_url: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch source_url: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("source_url content too large (max %d bytes)", maxBytes)
	}
	return data, nil
}
