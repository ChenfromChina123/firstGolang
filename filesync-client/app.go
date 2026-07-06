package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"filesync-client/internal/api"
	"filesync-client/internal/auth"
	"filesync-client/internal/config"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 主应用结构体，持有 Wails 上下文、认证管理器和 API 客户端。
type App struct {
	ctx       context.Context
	authMgr   *auth.AuthManager
	apiClient *api.Client
}

// NewApp 创建应用实例。
func NewApp() *App {
	return &App{}
}

// startup Wails 生命周期钩子，应用启动时调用。
// 根据已加载配置初始化 AuthManager 和 API Client。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	cfg, err := config.Load()
	if err != nil {
		return
	}
	a.authMgr = auth.New(cfg.ServerURL)
	a.apiClient = api.New(a.authMgr)
}

// IsFirstRun 检查是否首次启动（配置文件不存在）。
// 前端据此决定显示向导还是主界面。
func (a *App) IsFirstRun() bool {
	return !config.Exists()
}

// LoadConfig 加载配置文件。
// 首次启动时返回默认配置，不报错。
func (a *App) LoadConfig() (*config.Config, error) {
	return config.Load()
}

// SaveConfig 保存配置到用户目录。
// 若 ServerURL 变更，同步更新 AuthManager 的服务器地址。
func (a *App) SaveConfig(cfg config.Config) error {
	if a.authMgr != nil && a.authMgr.ServerURL() != cfg.ServerURL {
		a.authMgr.SetServerURL(cfg.ServerURL)
	}
	return config.Save(&cfg)
}

// SelectDirectory 打开系统目录选择对话框，返回选中的绝对路径。
// 用于向导中选择同步目录。
func (a *App) SelectDirectory() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择同步目录",
	})
}

// TestResult 连接测试结果
type TestResult struct {
	OK  bool   `json:"ok"`
	Msg string `json:"msg"`
}

// TestConnection 测试服务器连通性。
// 对服务器地址发起 GET /api/health 请求，5 秒超时。
// 复用 AuthManager 的 HTTPClient 以保持 Cookie 一致性。
func (a *App) TestConnection(serverURL string) TestResult {
	if serverURL == "" {
		return TestResult{OK: false, Msg: "服务器地址不能为空"}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	if a.authMgr != nil && a.authMgr.ServerURL() == serverURL {
		client = a.authMgr.HTTPClient()
	}
	resp, err := client.Get(serverURL + "/api/health")
	if err != nil {
		return TestResult{OK: false, Msg: fmt.Sprintf("连接失败: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return TestResult{OK: true, Msg: "连接成功"}
	}
	return TestResult{OK: false, Msg: fmt.Sprintf("服务器返回 HTTP %d", resp.StatusCode)}
}

// Login 登录服务器。
// 调用 AuthManager.Login，成功后 cookiejar 自动保存 JWT Cookie。
func (a *App) Login(username, password string) error {
	if a.authMgr == nil {
		return fmt.Errorf("认证管理器未初始化")
	}
	return a.authMgr.Login(username, password)
}

// Logout 登出服务器。
// 清除服务器端 Cookie 和本地用户缓存。
func (a *App) Logout() error {
	if a.authMgr == nil {
		return fmt.Errorf("认证管理器未初始化")
	}
	return a.authMgr.Logout()
}

// GetCurrentUser 返回当前登录用户信息（未登录返回 nil）。
func (a *App) GetCurrentUser() (*auth.UserInfo, error) {
	if a.authMgr == nil {
		return nil, nil
	}
	return a.authMgr.GetUserInfo(), nil
}

// CheckAuth 检查是否已登录（通过 GET /api/me 探测）。
func (a *App) CheckAuth() bool {
	if a.authMgr == nil {
		return false
	}
	return a.authMgr.IsAuthenticated()
}

// ListFiles 列出服务器上的文件。
// 透传给 apiClient，用于验证认证是否生效。
func (a *App) ListFiles(prefix string) ([]api.FileRecord, error) {
	if a.apiClient == nil {
		return nil, fmt.Errorf("API 客户端未初始化")
	}
	return a.apiClient.ListFiles(prefix)
}
