package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"filesync-client/internal/config"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 主应用结构体，持有 Wails 上下文用于调用 runtime API
type App struct {
	ctx context.Context
}

// NewApp 创建应用实例
func NewApp() *App {
	return &App{}
}

// startup Wails 生命周期钩子，应用启动时调用
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
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
// 向导完成时由前端调用。
func (a *App) SaveConfig(cfg config.Config) error {
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
	OK   bool   `json:"ok"`
	Msg  string `json:"msg"`
}

// TestConnection 测试服务器连通性。
// 对服务器地址发起 GET /api/health 请求，5 秒超时。
// 返回 TestResult 结构体（ok + 描述信息）。
func (a *App) TestConnection(serverURL string) TestResult {
	if serverURL == "" {
		return TestResult{OK: false, Msg: "服务器地址不能为空"}
	}

	client := &http.Client{Timeout: 5 * time.Second}
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
