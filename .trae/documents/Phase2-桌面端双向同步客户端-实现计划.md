# Phase 2：桌面端双向同步客户端实现计划

> 创建时间：2026-07-07
> 任务来源：基于「云盘功能对标分析-百度网盘.md」路线图 Phase 2
> 技术栈：Wails v2 + Go + Vue 3 + TypeScript
> 预估工期：4-6 周

---

## 一、Context（背景与目标）

### 问题与需求
filesync 当前仅支持 Web 端操作，用户必须手动上传/下载文件。对标百度网盘已有 PC 客户端自动同步能力，filesync 需要补齐这一短板。

### 目标
构建一个基于 Wails v2 的桌面端同步客户端，实现：
1. **双向同步**：本地目录 ↔ 服务器自动同步
2. **冲突解决**：当本地和服务器同时修改同一文件时，提供策略处理
3. **后台运行**：系统托盘常驻，开机自启
4. **跨平台**：Windows/macOS/Linux 三端支持

### 技术选型理由
- **Wails v2**：包体积 10-20MB（vs Electron 150MB+），复用 Go 后端，原生 WebView 性能优
- **Go**：与 filesync 后端同语言，HTTP 客户端生态成熟
- **Vue 3 + TypeScript**：前端响应式开发，类型安全

---

## 二、现有代码分析

### 可复用的 filesync API（已验证）

| API | 方法 | 用途 | 认证 |
|-----|------|------|------|
| `/api/login` | POST | 登录获取 JWT Cookie | 公开 |
| `/api/logout` | POST | 登出 | 需认证 |
| `/api/me` | GET | 获取当前用户信息 | 需认证 |
| `/api/files` | GET | 列出文件（支持 `?prefix=xxx/`） | 需认证 |
| `/api/files/{id}` | GET | 文件详情 | 需认证 |
| `/api/files/rename` | POST | 重命名/移动文件 | 需认证 |
| `/api/files/{id}` | DELETE | 删除文件（软删除） | 需认证 |
| `/api/upload/init` | POST | 初始化上传会话 | 需认证 |
| `/api/upload/chunk` | POST | 上传分片（multipart） | 需认证 |
| `/api/upload/status` | GET | 查询上传进度 | 需认证 |
| `/api/upload/complete` | POST | 完成上传 | 需认证 |
| `/api/upload/check` | POST | 秒传检查（hash+size） | 需认证 |
| `/api/download/{id}` | GET | 下载文件（支持 Range） | 需认证 |

### CLI 客户端参考实现（`cmd/client/main.go`）

关键代码模式：
- **HTTP 客户端**：`http.Post` / `http.Get` + `json.NewDecoder` 解析响应
- **分片上传**：手动构造 multipart body，512KB 分片
- **断点续传**：`Range: bytes={downloaded}-` header + 追加写入
- **SHA256 计算**：`crypto/sha256` + `io.Copy` + `hex.EncodeToString`
- **冲突处理**：服务器返回 409 时让用户选择 skip/overwrite/rename

### FileRecord 关键字段（用于冲突检测）

```go
type FileRecord struct {
    ID        string `json:"id"`         // 文件唯一标识
    Filename  string `json:"filename"`   // 含虚拟目录路径（如 docs/report.pdf）
    Size      int64  `json:"size"`       // 文件大小（字节）
    Hash      string `json:"hash"`       // SHA256 完整文件哈希
    Owner     string `json:"owner"`      // 所属用户
    CreatedAt string `json:"created_at"` // ISO 8601 时间戳
    UpdatedAt string `json:"updated_at"` // 更新时间
}
```

---

## 三、架构设计

### 3.1 整体架构

```
┌─────────────────────────────────────────────┐
│  桌面端同步客户端（Wails v2 应用）            │
├─────────────────────────────────────────────┤
│  前端（Vue 3 + TypeScript）                  │
│  ├── 配置页面（服务器地址、账号、同步目录）    │
│  ├── 同步状态面板（文件列表、进度、日志）      │
│  ├── 冲突解决对话框                          │
│  └── 系统托盘菜单                            │
├─────────────────────────────────────────────┤
│  Go 后端（Wails Bind）                       │
│  ├── config.go    配置管理（JSON 持久化）     │
│  ├── auth.go      认证模块（登录/Cookie 管理）│
│  ├── sync.go      同步引擎（核心）            │
│  ├── watcher.go   本地文件监控（fsnotify）    │
│  ├── transfer.go  文件传输（上传/下载）       │
│  └── conflict.go  冲突解决                    │
├─────────────────────────────────────────────┤
│  本地存储                                    │
│  ├── ~/.filesync-client/config.json          │
│  ├── ~/.filesync-client/sync-state.db        │
│  └── 用户指定的同步目录                       │
└─────────────────────────────────────────────┘
                    │
                    │ HTTP API
                    ▼
┌─────────────────────────────────────────────┐
│  filesync 服务器（已部署）                    │
└─────────────────────────────────────────────┘
```

### 3.2 项目目录结构

```
filesync-client/                    # 新项目目录（与 filesync/ 平级）
├── main.go                         # Wails 入口
├── wails.json                      # Wails 配置
├── app.go                          # 应用结构体 + 生命周期钩子
├── go.mod
├── internal/
│   ├── config/
│   │   └── config.go               # 配置加载/保存
│   ├── auth/
│   │   └── auth.go                 # 登录/Cookie 管理
│   ├── sync/
│   │   ├── engine.go               # 同步引擎主循环
│   │   ├── watcher.go              # fsnotify 文件监控
│   │   ├── transfer.go             # 上传/下载实现
│   │   ├── conflict.go             # 冲突检测与解决
│   │   └── state.go                # 同步状态持久化（SQLite）
│   └── api/
│       └── client.go               # HTTP 客户端（封装 filesync API）
├── frontend/
│   ├── src/
│   │   ├── App.vue                 # 主界面
│   │   ├── views/
│   │   │   ├── ConfigView.vue      # 配置页面
│   │   │   ├── SyncView.vue        # 同步状态面板
│   │   │   └── ConflictView.vue    # 冲突解决对话框
│   │   ├── components/
│   │   │   ├── FileList.vue        # 文件列表
│   │   │   ├── ProgressCard.vue    # 进度卡片
│   │   │   └── LogPanel.vue        # 日志面板
│   │   └── stores/
│   │       └── sync.ts             # Pinia 状态管理
│   ├── package.json
│   └── vite.config.ts
└── build/
    └── ...                         # 打包配置
```

---

## 四、核心模块设计

### 4.1 配置管理（config.go）

```go
type Config struct {
    ServerURL    string `json:"server_url"`    // 服务器地址
    Username     string `json:"username"`      // 用户名
    Password     string `json:"-"`             // 密码（不持久化，仅内存）
    SyncDir      string `json:"sync_dir"`      // 本地同步目录
    AutoStart    bool   `json:"auto_start"`    // 开机自启
    SyncInterval int    `json:"sync_interval"` // 服务器轮询间隔（秒）
    ChunkSize    int64  `json:"chunk_size"`    // 分片大小
    Concurrency  int    `json:"concurrency"`   // 并发上传数
}
```
- 配置文件路径：`~/.filesync-client/config.json`
- 密码不持久化，每次启动需重新输入（或使用系统凭证存储）

### 4.2 认证模块（auth.go）

```go
type AuthManager struct {
    client    *http.Client
    cookieJar *cookiejar.Jar
    serverURL string
    token     string
}

func (a *AuthManager) Login(username, password string) error
func (a *AuthManager) IsAuthenticated() bool
func (a *AuthManager) Logout() error
```
- 使用 `net/http/cookiejar` 自动管理 Cookie
- 登录成功后 Cookie 持久化到内存，应用关闭后丢失

### 4.3 文件监控（watcher.go）

```go
type FileWatcher struct {
    watcher  *fsnotify.Watcher
    syncDir  string
    events   chan FileEvent
}

type FileEvent struct {
    Type     EventType  // Create | Modify | Delete | Rename
    Path     string     // 相对路径
    OldPath  string     // Rename 时的旧路径
}
```
- 使用 `fsnotify` 监控本地目录变化
- 防抖处理：500ms 内连续修改合并为一次事件
- 忽略临时文件（`.tmp`, `.swp`, `~$` 等）

### 4.4 同步引擎（engine.go）

```go
type SyncEngine struct {
    config     *Config
    auth       *AuthManager
    watcher    *FileWatcher
    state      *SyncState
    eventQueue chan FileEvent
    stopCh     chan struct{}
}

func (e *SyncEngine) Start() error
func (e *SyncEngine) Stop() error
func (e *SyncEngine) fullSync() error              // 全量同步
func (e *SyncEngine) handleLocalEvent(ev FileEvent) // 本地变更推送
func (e *SyncEngine) pollServer() error             // 服务器变更拉取
```

**同步流程**：
1. **启动时全量同步**：对比本地与服务器文件列表，同步差异
2. **本地事件驱动**：fsnotify 监控到变化时，立即推送到服务器
3. **服务器轮询**：每 N 秒拉取服务器文件列表，检测远端变更

### 4.5 冲突解决（conflict.go）

```go
type Conflict struct {
    LocalFile  *LocalFileInfo
    RemoteFile *RemoteFileInfo
    ConflictType ConflictType  // BothModified | LocalDeletedRemoteModified | ...
}

type ConflictResolution string
const (
    KeepLocal    ConflictResolution = "keep_local"
    KeepRemote   ConflictResolution = "keep_remote"
    KeepBoth     ConflictResolution = "keep_both"     // 创建副本
    Skip         ConflictResolution = "skip"
)

func (e *SyncEngine) detectConflict(local, remote *FileInfo) *Conflict
func (e *SyncEngine) resolveConflict(c *Conflict, resolution ConflictResolution) error
```

**冲突检测逻辑**：
- 对比本地文件 `mtime` 与上次同步时间
- 对比服务器文件 `updated_at` 与上次同步时间
- 若双方都修改了 → 冲突

**冲突解决 UI**：前端弹窗显示冲突详情，用户选择处理方式

### 4.6 同步状态持久化（state.go）

```go
type SyncState struct {
    db *sql.DB
}

type FileSyncRecord struct {
    Path          string // 本地相对路径
    RemoteID      string // 服务器文件 ID
    LocalHash     string // 本地文件 SHA256
    RemoteHash    string // 服务器文件 SHA256
    LocalMtime    int64  // 本地修改时间
    RemoteMtime   int64  // 服务器修改时间
    LastSyncTime  int64  // 上次同步时间
    SyncStatus    string // synced | conflict | pending | error
}
```
- 使用 SQLite 存储同步状态（复用 `modernc.org/sqlite` 纯 Go 驱动）
- 记录每个文件的本地/远程哈希和时间戳
- 用于增量同步和冲突检测

---

## 五、前端设计方案

### 5.1 页面结构

```
┌─────────────────────────────────────────┐
│  FileSync 同步客户端              ─ □ × │
├─────────┬───────────────────────────────┤
│         │                               │
│  导航    │         主内容区              │
│         │                               │
│  📁 同步 │  ┌─────────────────────────┐ │
│  ⚙ 设置 │  │  同步状态卡片            │ │
│  📊 日志 │  │  ● 已同步 | 上次: 5min前 │ │
│         │  └─────────────────────────┘ │
│         │                               │
│         │  ┌─────────────────────────┐ │
│         │  │  文件列表               │ │
│         │  │  ✅ docs/readme.md      │ │
│         │  │  ⬆️  images/logo.png    │ │
│         │  │  ⚠️  config.json        │ │
│         │  └─────────────────────────┘ │
│         │                               │
│         │  ┌─────────────────────────┐ │
│         │  │  实时日志               │ │
│         │  │  [10:32] 上传 docs/...  │ │
│         │  └─────────────────────────┘ │
└─────────┴───────────────────────────────┘
```

### 5.2 技术栈

- **Vue 3** + Composition API
- **TypeScript** 类型安全
- **Pinia** 状态管理
- **Element Plus** 或 **Naive UI** 组件库
- **Vite** 构建工具

### 5.3 关键交互

- **首次启动向导**：引导用户配置服务器地址、登录、选择同步目录
- **系统托盘**：最小化到托盘，右键菜单（立即同步/暂停/退出）
- **通知**：使用 Wails 的通知 API 推送同步完成/冲突提醒
- **拖拽配置**：支持拖拽文件夹到设置页面自动填入路径

---

## 六、开发阶段划分

### Phase 2.1：项目骨架与环境搭建（3-5 天）
- [ ] 安装 Wails CLI，创建项目（`wails init -n filesync-client -t vue-ts`）
- [ ] 配置 go.mod，引入依赖（fsnotify, modernc.org/sqlite, cookiejar）
- [ ] 搭建前端框架（Vue 3 + Pinia + 组件库）
- [ ] 实现配置管理模块（config.go）
- [ ] 实现首次启动向导 UI

### Phase 2.2：认证与基础通信（3-5 天）
- [ ] 实现 AuthManager（登录/Cookie 管理）
- [ ] 实现 API Client（封装所有 filesync API 调用）
- [ ] 实现配置页面 UI（服务器地址/账号/同步目录）
- [ ] 验证登录功能端到端

### Phase 2.3：单向同步 - 上传（5-7 天）
- [ ] 实现 FileWatcher（fsnotify 监控）
- [ ] 实现 Transfer 模块（分片上传，复用 CLI 客户端逻辑）
- [ ] 实现 SyncState（SQLite 状态持久化）
- [ ] 实现本地 → 服务器同步流程
- [ ] 实现同步状态面板 UI

### Phase 2.4：单向同步 - 下载（3-5 天）
- [ ] 实现服务器轮询（定期拉取 `/api/files`）
- [ ] 实现下载功能（Range 断点续传）
- [ ] 实现服务器 → 本地同步流程
- [ ] 实现文件列表 UI（显示同步状态图标）

### Phase 2.5：双向同步与冲突解决（5-7 天）
- [ ] 实现冲突检测逻辑
- [ ] 实现冲突解决对话框 UI
- [ ] 实现全量同步（启动时对比）
- [ ] 实现增量同步（事件驱动 + 轮询）

### Phase 2.6：系统托盘与优化（3-5 天）
- [ ] 实现系统托盘（最小化/右键菜单）
- [ ] 实现开机自启
- [ ] 实现通知推送
- [ ] 性能优化（并发上传、防抖、断点续传）

### Phase 2.7：打包与测试（3-5 天）
- [ ] Windows 打包（`wails build -platform windows/amd64`）
- [ ] macOS 打包（`wails build -platform darwin/universal`）
- [ ] 端到端测试（与 filesync 服务器联调）
- [ ] 文档编写（README + 用户手册）

---

## 七、技术难点与解决方案

### 7.1 文件监控的跨平台兼容性
- **问题**：fsnotify 在不同平台行为不一致（如 Windows 不支持某些事件）
- **方案**：封装 FileWatcher，对差异做适配；使用轮询作为兜底方案

### 7.2 大文件同步的内存控制
- **问题**：大文件（>1GB）分片上传时内存占用高
- **方案**：流式读取 + 固定缓冲区（512KB-4MB），复用 CLI 客户端的分片逻辑

### 7.3 冲突解决的 UX 设计
- **问题**：双向同步必然产生冲突，用户需要清晰的解决界面
- **方案**：弹窗显示冲突详情（本地版本 vs 服务器版本），提供"保留本地/保留服务器/创建副本/跳过"四选项

### 7.4 网络中断恢复
- **问题**：同步过程中网络中断
- **方案**：利用 filesync 的断点续传能力（分片上传 + Range 下载），自动重试 + 指数退避

### 7.5 服务器文件删除的本地感知
- **问题**：服务器端删除文件后，客户端如何知道
- **方案**：定期轮询 `/api/files`，对比本地 SyncState 中的 RemoteID，缺失则删除本地文件

---

## 八、验证方案

### 8.1 开发环境验证
- 本地启动 filesync 服务器（`./filesync_server.exe`）
- 运行 `wails dev` 启动桌面客户端
- 配置服务器地址 `http://localhost:8080`，登录 admin/changeme123

### 8.2 功能测试用例
1. **首次同步**：客户端配置同步目录后，本地文件应全部上传到服务器
2. **本地新增**：在同步目录创建新文件，验证自动上传
3. **本地修改**：修改已有文件，验证服务器版本更新
4. **本地删除**：删除本地文件，验证服务器文件被删除（软删除）
5. **服务器新增**：通过 Web 端上传文件，验证客户端自动下载
6. **服务器删除**：通过 Web 端删除文件，验证客户端本地文件被删除
7. **冲突场景**：同时修改本地和服务器同一文件，验证冲突对话框弹出
8. **断网恢复**：同步过程中断网，恢复后验证续传

### 8.3 性能验证
- 1000 个小文件（< 1MB）批量同步：应在 5 分钟内完成
- 单个大文件（1GB）上传：内存占用 < 100MB
- 长时间运行（24 小时）：无内存泄漏

---

## 九、依赖清单

### Go 依赖
- `github.com/wailsapp/wails/v2` - 桌面应用框架
- `github.com/fsnotify/fsnotify` - 文件监控
- `modernc.org/sqlite` - SQLite 驱动（纯 Go，无 CGO）
- `net/http/cookiejar` - Cookie 管理（标准库）

### 前端依赖
- `vue@^3.4` - 前端框架
- `pinia@^2.1` - 状态管理
- `naive-ui@^2.38` 或 `element-plus@^2.13` - UI 组件库
- `vite@^5.1` - 构建工具
- `typescript@^5.3` - 类型系统

---

## 十、风险与回滚

### 风险
1. **Wails v2 学习曲线**：团队首次使用 Wails，可能需要 2-3 天熟悉
2. **跨平台兼容性**：fsnotify 在 Linux 上的行为可能与 Windows 不同
3. **冲突解决复杂度**：双向同步的冲突场景比预期复杂

### 回滚方案
- 若 Wails v2 遇到不可解决的问题，可降级为 Electron 方案（但包体积增大 10 倍）
- 若双向同步过于复杂，可先发布单向同步版本（仅本地 → 服务器）

---

## 十一、下一步行动

1. ⏭ 用户审批本计划
2. ⏭ 执行 Phase 2.1：项目骨架与环境搭建
3. ⏭ 按 Phase 2.2-2.7 顺序开发
