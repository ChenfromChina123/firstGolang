package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ============================================================
// /mcp/manifest.json 与 /mcp/llms.txt 自动生成
// 直接从已注册工具（含 jsonschema-go 生成的 InputSchema）导出，
// 避免手写文档导致的漂移（项目已有 doc/分享功能.md:118 前科）。
// ============================================================

// Manifest 是 /mcp/manifest.json 的机器可读结构。
type Manifest struct {
	Name        string             `json:"name"`
	Version     string             `json:"version"`
	Description string             `json:"description"`
	Transport   ManifestTransport  `json:"transport"`
	Auth        ManifestAuth       `json:"auth"`
	Tools       []ManifestToolSpec `json:"tools"`
}

// ManifestTransport transport 端点描述。
type ManifestTransport struct {
	StreamableHTTP string `json:"streamable_http"`
	Stdio          bool   `json:"stdio"`
}

// ManifestAuth PAT 认证说明。
type ManifestAuth struct {
	Type        string `json:"type"`
	Scheme      string `json:"scheme"`
	Description string `json:"description"`
}

// ManifestToolSpec 单工具描述。
type ManifestToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Scopes      []string        `json:"scopes,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ensureTools 确保工具已注册收集（Server() 在 HTTPHandler 闭包内惰性调用，
// 构建文档时需先行注册以填充 s.tools）。
func (s *MCP) ensureTools() {
	if len(s.tools) == 0 {
		s.Server() // 触发 registerTools 填充 s.tools（丢弃该 server 实例）
	}
}

// BuildManifest 生成 manifest（transport 端点由调用方注入）。
func (s *MCP) BuildManifest(httpEndpoint string) *Manifest {
	s.ensureTools()
	// scope 映射（与 tools.go requireScope 校验一致，双源对齐于 auth 常量）
	scopeOf := func(name string) []string {
		switch name {
		case "fs_list", "fs_stat", "fs_read", "fs_download", "fs_trash_list":
			return []string{"filesync:read"}
		case "fs_write", "fs_upload", "fs_mkdir", "fs_rename", "fs_move", "fs_delete", "fs_trash_restore":
			return []string{"filesync:write"}
		case "share_create", "share_list", "share_delete":
			return []string{"filesync:share"}
		case "whoami":
			return nil // 无需 scope（仅身份信息）
		}
		return nil
	}

	mt := &Manifest{
		Name:        "filesync",
		Version:     "1.0.0",
		Description: "FileSync MCP Server: 文件管理、回收站、分享。使用前先调用 whoami 查看当前令牌能力边界。",
		Transport: ManifestTransport{
			StreamableHTTP: httpEndpoint,
			Stdio:          true,
		},
		Auth: ManifestAuth{
			Type:        "pat",
			Scheme:      "Bearer",
			Description: "创建 PAT 后通过 Authorization: Bearer fsk_<token> 访问。PAT 在 Web 控制台『访问令牌』页面生成。",
		},
	}
	for _, t := range s.tools {
		raw, _ := json.Marshal(t.InputSchema)
		mt.Tools = append(mt.Tools, ManifestToolSpec{
			Name:        t.Name,
			Description: t.Description,
			Scopes:      scopeOf(t.Name),
			InputSchema: raw,
		})
	}
	return mt
}

// BuildLLMSText 生成 /mcp/llms.txt（agent 纯文本速查）。
func (s *MCP) BuildLLMSText() string {
	s.ensureTools()
	var b strings.Builder
	b.WriteString("# FileSync MCP Server\n\n")
	b.WriteString("> FileSync 文件管理 MCP Server。通过 PAT（个人访问令牌）认证。\n")
	b.WriteString("> 认证方式: `Authorization: Bearer fsk_<token>`（Streamable HTTP）或环境变量 `FILESYNC_PAT`（stdio）。\n")
	b.WriteString("> 令牌在 Web 控制台『访问令牌』页生成，可指定 scopes / 空间 / 目录前缀 / 配额。\n\n")
	b.WriteString("## 快速上手\n\n")
	b.WriteString("1. 登录 FileSync Web 控制台，生成 PAT（建议最小权限：仅勾选需要的 scope）。\n")
	b.WriteString("2. HTTP: POST /mcp + `Authorization: Bearer fsk_...`；stdio: `FILESYNC_PAT=fsk_... go run ./cmd/mcp-stdio`。\n")
	b.WriteString("3. 首先调用 `whoami` 查看当前令牌能力边界（scopes / 空间 / 路径前缀 / 配额）。\n")
	b.WriteString("4. 文件操作路径为虚拟目录（如 `docs/report.pdf`），无前导斜杠。\n\n")
	b.WriteString("## Scope 体系\n\n")
	b.WriteString("- `filesync:read`  : fs_list / fs_stat / fs_read / fs_download / fs_trash_list / whoami\n")
	b.WriteString("- `filesync:write` : fs_write / fs_upload / fs_mkdir / fs_rename / fs_move / fs_delete / fs_trash_restore\n")
	b.WriteString("- `filesync:share` : share_create / share_list / share_delete\n\n")
	b.WriteString("## 工具速查\n\n")
	b.WriteString("| 工具 | 说明 | scope |\n")
	b.WriteString("|------|------|-------|\n")
	for _, t := range s.tools {
		desc := strings.ReplaceAll(t.Description, "|", "\\|")
		b.WriteString(fmt.Sprintf("| `%s` | %s | %s |\n", t.Name, desc, strings.Join(scopeMap[t.Name], ", ")))
	}
	b.WriteString("\n## 通用约定\n\n")
	b.WriteString("- 文件路径为虚拟目录路径，空路径表示根目录；`docs/` 结尾斜杠表示目录。\n")
	b.WriteString("- 删除为软删除（进回收站，30 天保留）；恢复用 `fs_trash_restore`。\n")
	b.WriteString("- 二进制文件：`fs_read` ≤1MB 返回 base64；大文件用 `fs_download` 拿临时下载链接。\n")
	b.WriteString("- 分享 URL 为绝对地址，可直接交付给终端用户；支持密码与过期时间。\n")
	b.WriteString("- 配额：令牌级配额超额时写入返回 413。\n")
	return b.String()
}

// scopeMap 供 llms.txt 表格使用（与 BuildManifest 同一数据源）。
var scopeMap = map[string][]string{
	"fs_list":          {"filesync:read"},
	"fs_stat":          {"filesync:read"},
	"fs_read":          {"filesync:read"},
	"fs_download":      {"filesync:read"},
	"fs_write":         {"filesync:write"},
	"fs_upload":        {"filesync:write"},
	"fs_mkdir":         {"filesync:write"},
	"fs_rename":        {"filesync:write"},
	"fs_move":          {"filesync:write"},
	"fs_delete":        {"filesync:write"},
	"fs_trash_list":    {"filesync:read"},
	"fs_trash_restore": {"filesync:write"},
	"share_create":     {"filesync:share"},
	"share_list":       {"filesync:share"},
	"share_delete":     {"filesync:share"},
	"whoami":           {},
}
