// Package agentsvc 提供文件域纯业务服务（供 MCP 工具调用）。
//
// 设计动机：
//   - handler 层强绑定 http.ResponseWriter，MCP 无法复用；
//   - 本次顺带补齐架构评估指出的「service 层缺失」；
//   - 未来 REST 与 MCP 共享同一业务逻辑，避免第三套实现。
//
// 本包不依赖 http，只依赖 store 层与 storage 层，方便单元测试与复用。
package agentsvc

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"filesync/internal/storage"
	"filesync/internal/store"
)

// AgentSvc 文件域业务服务。
type AgentSvc struct {
	db     *store.DB
	router *storage.Router
	// baseURL 应用对外访问基础地址（如 https://fs.example.com）。
	// 用于生成分享链接等绝对 URL；空则退化为相对路径。
	baseURL string
}

// New 创建 AgentSvc。baseURL 可为空。
func New(db *store.DB, router *storage.Router, baseURL string) *AgentSvc {
	return &AgentSvc{db: db, router: router, baseURL: strings.TrimSuffix(baseURL, "/")}
}

// DB 暴露底层存储（供 MCP 层做配额记账等扩展）。
func (s *AgentSvc) DB() *store.DB { return s.db }

// BaseURL 返回配置的应用基础地址。
func (s *AgentSvc) BaseURL() string { return s.baseURL }

// AbsURL 把分享相对路径转成绝对 URL；baseURL 为空时原样返回。
func (s *AgentSvc) AbsURL(relative string) string {
	if s.baseURL == "" || relative == "" || strings.HasPrefix(relative, "http") {
		return relative
	}
	return s.baseURL + relative
}

// 常见业务错误（供 MCP 层映射为结构化错误信息）
var (
	// ErrNotFound 文件/分享不存在
	ErrNotFound = errors.New("not found")
	// ErrForbidden 权限不足（非本人文件或越权操作）
	ErrForbidden = errors.New("forbidden")
	// ErrConflict 同名文件已存在
	ErrConflict = errors.New("conflict: file already exists")
	// ErrInvalidPath 路径非法
	ErrInvalidPath = errors.New("invalid path")
	// ErrPathTraversal PAT path_prefix 沙箱越界
	ErrPathTraversal = errors.New("path outside token sandbox")
)

// ErrIsNotFound 判定是否为未找到错误。
func ErrIsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, sql.ErrNoRows)
}

// fmtErr 统一格式化错误。
func fmtErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("agentsvc: %w", err)
}
