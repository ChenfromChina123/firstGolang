# Phase 2.1 执行计划：项目骨架与环境搭建

> 创建时间：2026-07-07
> 任务来源：Phase 2 桌面端双向同步客户端实现计划（已批准）
> 执行范围：Phase 2.1 全部 5 个子任务，一次性完成
> 技术栈：Wails v2 + Go + Vue 3 + TypeScript + Naive UI + Pinia

---

## 一、摘要

执行 Phase 2.1 项目骨架与环境搭建，从零创建 filesync-client 桌面端同步客户端项目。完成 Wails CLI 安装、项目初始化、前端框架搭建、配置管理模块实现、首次启动向导 UI 实现五大任务，最终在本地启动验证后提交 git。

---

## 二、当前状态分析

### 已验证环境
- **Go**：go1.26.4 windows/amd64
- **GOPATH**：C:\Users\Administrator\go
- **GOPROXY**：https://goproxy.cn,direct（国内加速，go install 可正常工作）
- **现有 filesync 模块名**：`filesync`，Go 版本 1.25.0
- **项目规则**：新代码现在本地启动验证再部署到服务器当中

### 未完成项
- Wails CLI 未安装（`C:\Users\Administrator\go\bin\` 下无 wails.exe）
- `filesync-client` 目录未创建
- 前端框架未搭建
- 配置管理模块未实现
- 首次启动向导 UI 未实现

### 关键决策（用户已确认）
1. **前端组件库**：Naive UI（轻量、TypeScript 友好、设计现代）
2. **执行范围**：一次性完成全部 5 个子任务
3. **首次启动向导 UI**：使用 `design-taste-frontend` skill 获取设计指导

---

## 三、建议的变更（执行步骤）

### 步骤 1：安装 Wails CLI 并验证环境

**操作**：
- 执行 `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- 验证安装：`wails version`
- 运行环境检查：`wails doctor`（检查 WebView2 Runtime、Node.js、Go 等依赖）

**验证标准**：
- `wails version` 输出版本号（v2.x.x）
- `wails doctor` 显示所有依赖已就绪（WebView2、Node.js、Go ✓）
- 若 Node.js 未安装，需先安装 Node.js LTS 版本

**预计耗时**：5-10 分钟（取决于网络）

---

### 步骤 2：创建 filesync-client 项目骨架

**操作**：
- 在 `d:\STUDY\GO\StudyGolang\firstGolang\` 目录下执行：
  ```
  wails init -n filesync-client -t vue-ts
  ```
- 生成的目录结构（Wails vue-ts 模板默认）：
  ```
  filesync-client/
  ├── main.go                 # Wails 入口
  ├── app.go                  # 应用结构体
  ├── wails.json              # Wails 配置
  ├── go.mod                  # Go 模块（模块名：filesync-client）
  ├── frontend/
  │   ├── src/
  │   │   ├── App.vue
  │   │   ├── main.ts
  │   │   └── style.css
  │   ├── index.html
  │   ├── package.json
  │   ├── tsconfig.json
  │   └── vite.config.ts
  └── build/
      ├── appicon.png
      ├── darwin/
      └── windows/
  ```

**调整目录结构**（按 Phase 2 计划要求）：
- 创建 `internal/config/` 目录（步骤 5 使用）
- 创建 `internal/auth/` 目录（Phase 2.2 使用，先建空目录）
- 创建 `internal/sync/` 目录（Phase 2.3 使用，先建空目录）
- 创建 `internal/api/` 目录（Phase 2.2 使用，先建空目录）
- 创建前端子目录：`frontend/src/views/`、`frontend/src/components/`、`frontend/src/stores/`

**验证标准**：
- `filesync-client/` 目录存在且结构完整
- `filesync-client/go.mod` 模块名为 `filesync-client`
- `wails dev` 可启动空白应用（显示默认欢迎页）

---

### 步骤 3：配置 go.mod 与依赖

**操作**：
- 在 `filesync-client/` 下执行 `go get` 引入依赖：
  ```
  go get github.com/fsnotify/fsnotify
  go get modernc.org/sqlite
  ```
- Wails 框架依赖由 `wails init` 自动添加
- cookiejar 为 Go 标准库，无需额外引入
- 执行 `go mod tidy` 清理依赖

**最终 go.mod 关键依赖**：
```go
require (
    github.com/wailsapp/wails/v2 v2.x.x
    github.com/fsnotify/fsnotify v1.x.x
    modernc.org/sqlite v1.x.x
)
```

**验证标准**：
- `go mod tidy` 无错误
- `go build ./...` 编译通过

---

### 步骤 4：搭建前端框架（Vue 3 + Naive UI + Pinia）

**操作**：
- 进入 `filesync-client/frontend/` 目录
- 安装 npm 依赖：
  ```
  npm install naive-ui pinia @vicons/ionicons5
  ```
- 配置 `frontend/src/main.ts`：注册 Naive UI 和 Pinia
- 创建目录结构并创建占位组件文件：
  ```
  frontend/src/
  ├── App.vue                 # 主界面（路由切换）
  ├── main.ts                 # 入口（注册 Naive UI + Pinia）
  ├── views/
  │   ├── ConfigView.vue      # 配置页面（占位）
  │   ├── SyncView.vue        # 同步状态面板（占位）
  │   └── ConflictView.vue    # 冲突解决对话框（占位）
  ├── components/
  │   ├── FileList.vue        # 文件列表（占位）
  │   ├── ProgressCard.vue    # 进度卡片（占位）
  │   └── LogPanel.vue        # 日志面板（占位）
  └── stores/
      └── sync.ts             # Pinia 状态管理（基础骨架）
  ```

**Pinia store 基础结构**（`stores/sync.ts`）：
```typescript
import { defineStore } from 'pinia'

export const useSyncStore = defineStore('sync', {
  state: () => ({
    isSyncing: false,
    lastSyncTime: '',
    syncProgress: 0,
    files: [] as Array<{ id: string; name: string; status: string }>,
    logs: [] as Array<{ time: string; level: string; message: string }>,
  }),
  actions: {
    addLog(level: string, message: string) {
      this.logs.push({
        time: new Date().toLocaleTimeString(),
        level,
        message,
      })
    },
  },
})
```

**验证标准**：
- `npm install` 无错误
- `wails dev` 启动后显示空白 Vue 页面无报错
- 浏览器控制台无 Naive UI / Pinia 注册错误

---

### 步骤 5：实现配置管理模块（config.go）

**操作**：
- 创建文件 `filesync-client/internal/config/config.go`
- 实现配置结构体、加载、保存、默认配置函数

**配置结构体设计**：
```go
package config

// Config 桌面端客户端配置
type Config struct {
    ServerURL    string `json:"server_url"`    // 服务器地址（如 https://aistudy.icu）
    Username     string `json:"username"`      // 用户名
    SyncDir      string `json:"sync_dir"`      // 本地同步目录绝对路径
    AutoStart    bool   `json:"auto_start"`    // 开机自启
    SyncStrategy string `json:"sync_strategy"` // 冲突默认策略：ask|keep_local|keep_remote|create_copy
    LastSyncTime string `json:"last_sync_time"` // 上次同步时间（ISO 8601）
    AutoSync     bool   `json:"auto_sync"`     // 是否启用自动同步
    SyncInterval int    `json:"sync_interval"` // 同步间隔（秒），默认 30
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config { ... }

// Load 从用户目录加载配置
// 配置文件位置：~/.filesync-client/config.json
func Load() (*Config, error) { ... }

// Save 保存配置到用户目录
func Save(c *Config) error { ... }

// ConfigPath 返回配置文件路径
func ConfigPath() (string, error) { ... }

// Exists 检查配置文件是否存在（用于判断是否首次启动）
func Exists() bool { ... }
```

**关键实现要点**：
- 配置文件位置：跨平台支持（`os.UserHomeDir()` + `.filesync-client/config.json`）
- 目录不存在时自动创建（`os.MkdirAll`）
- 函数级注释（遵循用户规则 8）

**验证标准**：
- `go build ./internal/config/` 编译通过
- 手动测试 Load/Save（通过 Wails Bind 在步骤 6 验证）

---

### 步骤 6：实现首次启动向导 UI（使用前端设计 skill）

**操作**：
- 调用 `design-taste-frontend` skill 获取设计指导
- 修改 `app.go`：暴露配置相关方法给前端
- 实现首次启动向导 UI（多步骤表单）

**Wails Bind 方法**（在 `app.go` 中实现）：
```go
type App struct {
    ctx context.Context
}

// SelectDirectory 打开目录选择对话框
func (a *App) SelectDirectory() (string, error) { ... }

// LoadConfig 加载配置
func (a *App) LoadConfig() (*config.Config, error) { ... }

// SaveConfig 保存配置
func (a *App) SaveConfig(c config.Config) error { ... }

// IsFirstRun 检查是否首次启动
func (a *App) IsFirstRun() bool { ... }

// TestConnection 测试服务器连接
func (a *App) TestConnection(serverURL string) (bool, string) { ... }
```

**首次启动向导 UI 设计**（使用 Naive UI 的 NSteps + NForm）：
- **步骤 1**：欢迎页（应用介绍 + 开始按钮）
- **步骤 2**：服务器配置（输入服务器地址 + 测试连接按钮）
- **步骤 3**：登录（用户名/密码，调用 /api/login）
  - 注意：登录逻辑在 Phase 2.2 完整实现，此处仅做 UI + 简单验证
- **步骤 4**：选择同步目录（文件夹选择器 + 路径显示）
- **步骤 5**：完成（保存配置 + 进入主界面）

**App.vue 逻辑**：
- 启动时调用 `IsFirstRun()` 判断
- 首次启动：显示向导组件
- 非首次启动：显示主界面（SyncView 占位）

**验证标准**：
- `wails dev` 启动后首次显示向导
- 走完向导后配置文件 `~/.filesync-client/config.json` 正确生成
- 再次启动跳过向导进入主界面
- 目录选择器可正常打开系统对话框

---

### 步骤 7：本地验证与 git 提交

**操作**：
- 启动本地 filesync 服务器（`d:\STUDY\GO\StudyGolang\firstGolang\filesync\` 下的可执行文件）
- 运行 `wails dev` 启动客户端
- 走完首次启动向导流程（服务器地址填 http://localhost:8080）
- 验证配置文件正确生成
- 验证再次启动跳过向导

**git 提交**：
- `git add filesync-client/`
- `git commit -m "Phase 2.1: 完成 filesync-client 项目骨架与环境搭建"`

**验证标准**：
- 本地联调通过
- git commit 成功
- `.sessions/` 存档已更新（用户规则 16）

---

## 四、假设与决策

| 决策项 | 选择 | 理由 |
|--------|------|------|
| 前端组件库 | Naive UI | 用户确认；轻量、TS 友好、设计现代 |
| 执行范围 | 一次性完成 5 子任务 | 用户确认；符合规则 17 |
| Wails 模板 | vue-ts | Phase 2 计划明确指定 |
| 模块名 | filesync-client | 与现有 filesync 模块平级、独立 |
| 配置文件位置 | ~/.filesync-client/config.json | 跨平台兼容、用户目录隔离 |
| 前端设计 skill | design-taste-frontend | 用户要求"使用前端设计skill" |
| Node.js | 需已安装 | Wails 前端构建依赖，wails doctor 会检查 |

---

## 五、验证步骤（端到端）

1. **环境验证**：`wails version` + `wails doctor` 全部通过
2. **项目结构**：`filesync-client/` 目录完整，包含 main.go、app.go、go.mod、frontend/
3. **依赖完整**：`go mod tidy` + `go build ./...` 无错误
4. **前端框架**：`wails dev` 启动后无控制台报错
5. **配置模块**：Load/Save 函数可正常调用
6. **向导 UI**：首次启动显示向导，走完后配置文件生成
7. **二次启动**：非首次启动跳过向导
8. **git 提交**：commit 成功，文件已纳入版本控制

---

## 六、风险与应对

| 风险 | 应对方案 |
|------|----------|
| Wails CLI 安装失败（网络） | GOPROXY 已配置 goproxy.cn，若仍失败尝试 go env -w GOFLAGS=-insecure |
| WebView2 未安装 | Windows 11 通常已预装；wails doctor 会提示，必要时从微软官网下载安装 |
| Node.js 未安装 | 先安装 Node.js LTS（https://nodejs.org） |
| wails dev 启动慢 | 首次编译需下载依赖，耐心等待；后续启动会缓存 |
| 设计 skill 无法满足需求 | 参考现有 filesync/web/style.css 风格，保持视觉一致 |
