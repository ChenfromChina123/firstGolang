# FileSync - 文件同步服务（断点续传）

一个支持**分片上传**和**断点续传**的文件同步服务，包含 HTTP 服务端和 CLI 客户端。

## 功能特性

- **分片上传**：大文件自动切分为 512KB 分片逐个上传
- **上传断点续传**：网络中断或程序中断后，自动恢复未完成的分片
- **下载断点续传**：支持 HTTP `Range` 请求头，中断后从断点继续下载
- **文件冲突检测**：同名文件上传时提示冲突，支持 `skip`（跳过）/ `overwrite`（覆盖）/ `rename`（重命名）
- **存储后端**：本地文件系统存储（默认）；S3 兼容对象存储（扩展）
- **文件完整性**：上传完成后计算 SHA256 校验
- **账号注册**：邮箱激活链接方式注册，24 小时有效，支持用户名/邮箱登录
- **忘记密码**：邮箱 6 位验证码重置密码，10 分钟有效
- **跨浏览器配置同步**：分片大小与并发数存储在服务器端，换浏览器/换设备后自动加载（同时写入 localStorage 即时响应）
- **分享链接功能**：将文件或目录生成分享链接，访客无需登录即可查看和下载，支持有效期设置和下载次数去重统计（visitor cookie + UNIQUE 约束）
- **分享在线预览**：分享页支持文件在线预览（`GET /api/s/{id}/preview`），单文件分享和目录分享中的单个文件均可预览。支持类型：图片（jpg/png/gif/webp/svg 等）、PDF、文本/代码（txt/md/json/js/py/go/sql 等 40+ 扩展名）、音频（mp3/wav/ogg/flac 等）、视频（mp4/webm/mov 等 inline 播放）。预览复用下载 token + 频率限制 + 密码校验（防盗链），但不计入下载次数。视频/音频支持 HTTP Range 206（拖动进度条）；视频预览仅使用原生 controls（全屏/音量/播放/原生进度条），保留 loading/error 覆盖层（loadstart/waiting/seeking/stalled 显示加载状态，canplay/playing/seeked 隐藏，error 显示错误码文案）；音频保留原生 controls；文本超过 512KB 截断显示。前端按文件扩展名自动显示「预览」按钮，不支持预览的文件类型（如 zip/exe）不显示按钮
- **文件所有权隔离**：files 表记录 owner 字段，用户只能下载/删除/重命名/移动/分享自己的文件；admin 可操作所有文件；历史文件 owner 为空时仅 admin 可访问；分享链接公开访问按创建者过滤防止越权下载
- **深度防盗链**：4 层防护保障资源不被盗链：①安全响应头（X-Frame-Options/CSP 防 iframe 嵌入）；②Referer 校验（只允许空 Referer 或白名单域名）；③签名 Token（分享下载需携带 HMAC-SHA256 token，30 分钟有效，绑定 share_id）；④频率限制（分享下载每 IP 每分钟 10 次）
- **安全加固**：基于 aistudy.icu 安全评估报告（评分 7.3/10 → 9.0/10）实施多阶段加固：①8 个安全响应头（HSTS/CSP/Permissions-Policy/X-Frame-Options/X-Content-Type-Options/Referrer-Policy/X-Permitted-Cross-Domain-Policies/X-XSS-Protection）；②HTTP 方法限制中间件（拒绝 TRACE/CONNECT，返回 405）；③路径净化中间件（拒绝 `/../` 和 `/./` 序列，返回 400，纵深防御）；④分享端点速率限制（30 次/分钟/IP，防分享 ID 暴力枚举）；⑤登录限流（rps=0.05, burst=2，2 次错误后 429）；⑥security.txt 安全联系信息文件；⑦SSH 加固参考脚本（deploy/security/harden_ssh.sh，禁用密码登录+fail2ban）；⑧CSP 精细化修复（评分 9.0→9.5）：Google Fonts 白名单（style-src 添加 fonts.googleapis.com，font-src 添加 fonts.gstatic.com）、所有 HTML 内联脚本提取为外部 `/web/js/*.js` 满足 `script-src 'self'` 不依赖 unsafe-inline、表单统一 `method="POST"` 防 CSP 阻断内联脚本时密码明文泄露到 URL、Cache-Control 分路径策略（`/api/` no-store 防敏感数据缓存，`/web/` public max-age=3600 加速静态资源）、admin.html 内联 onclick 全部改为事件委托（data-action + addEventListener）。中间件链：SecurityHeaders -> MethodGuard -> PathGuard -> RefererCheck -> JWT -> mux
- **安全加固（第二轮）**：基于深度安全评估修复 2 个高风险 + 4 个中风险：①JWT 签名由 HS256 对称改为 RS256 非对称（私钥签发+公钥验证，密钥独立持久化于 `data/jwt_rsa_private.pem`，与密码加密 RSA 密钥分离）；②RSA 密码传输移除明文回退（前后端解密/加密失败即拒绝请求，旧客户端无法登录）；③`.env` 文件权限检查（Linux 启动时 `mode&0o077 != 0` 拒绝启动，要求 `chmod 600`）；④X-Forwarded-For 可信代理机制（仅 `TRUSTED_PROXIES` 配置的 IP 的 XFF 被采信，防伪造绕过登录限流）；⑤登录限流 cleanup 改为 LRU 淘汰（删除超过 10 分钟未访问的条目，而非清空全部，避免误杀正常用户）；⑥随机数生成失败移除弱兜底（`GenerateActivationToken`/`GenerateResetCode` 返回 error 而非回退时间戳/固定值）
- **RSA 密码字段加密传输**：前端使用 RSA-2048 公钥加密所有 password 类字段（登录 password、注册 password/confirm_password、重置密码 new_password/confirm_password、管理员重置用户密码 new_password），防止 DevTools Network 面板中明文泄露。后端 `GET /api/pubkey` 返回 PKIX PEM 格式公钥（`Cache-Control: public, max-age=3600`，1 小时缓存），私钥持久化于 `data/rsa_private.pem`（0600 权限，重启后公钥不变前端缓存不失效）。前端 `web/js/crypto.js` 封装 `encryptPassword()` 函数（jsencrypt 3.3.2 本地打包符合 CSP `script-src 'self'`），公钥缓存在内存避免重复请求。后端 `DecryptPassword` 解密失败时原样返回字段值（向后兼容部署过渡期，不降低 HTTPS 已有传输安全）。加密范围：Login.password、Register.password/confirm_password、ResetPassword.new_password/confirm_password、AdminResetPassword.new_password
- **秒传功能（全局存储）**：上传前通过 Web Worker 在后台线程计算完整文件 SHA256，调用 `/api/upload/check` 接口检查哈希是否已存在。命中时后端直接共享源文件 storage_path（不复制物理文件）并创建新记录，整个上传流程被跳过，实现"秒级"上传。**跨用户秒传**：任意用户上传相同 hash+size 文件均可命中，多个 DB 记录共享同一物理文件，永久删除时通过引用计数（CountByStoragePath）判断是否删除物理文件。hash+size 双重校验避免误判，秒传检查失败自动降级为正常上传（非致命）。
- **回收站功能**：文件删除采用软删除机制（deleted_at 字段），移入回收站保留 30 天可恢复。支持列出回收站、恢复文件（含文件名冲突检测）、永久删除单个文件、清空回收站。服务启动时自动清理过期回收站文件（物理删除数据库记录 + 删除存储文件）。admin 可通过 `?all=true` 查看和管理所有用户的回收站。
- **压缩包在线解压**：支持 zip / tar / tar.gz / tar.bz2 四种格式在线预览，单击压缩包即列出包内文件树（目录可折叠），文件可在线预览或下载，无需将整个压缩包下载到本地。安全防护：Zip Slip 路径穿越拒绝、压缩炸弹限制（包 ≤2GB / 条目 ≤10000 / 单文件 ≤500MB）、加密压缩包拒绝。tar.gz/tar.bz2 真流式解析，zip 采用临时文件方案（标准库要求 ReaderAt）。
- **存储用量显示**：顶部导航栏实时显示当前用户已用云空间（`GET /api/storage-usage`），admin 可通过 `?username=xxx` 查看指定用户或 `?username=` 查看全局用量。统计含正常文件和回收站文件两部分。
- **管理员后台**：独立的管理后台页面（`/web/admin.html`），4 个 Tab 模块：①系统总览（用户/文件/存储/分享/回收站统计卡片）；②用户管理（列表/禁用启用/重置密码，防止管理员禁用自己或修改其他 admin）；③文件管理（所有用户文件列表，含 owner 列）；④分享管理（所有分享列表/删除分享/查看分享页）。权限守卫：非 admin 访问自动重定向到首页。
- **品牌视觉系统**：统一 SVG 图标库（`/web/img/`）：①`favicon.svg` 浏览器标签页图标（圆角方形 + 文件图标 + 双向同步箭头，青绿渐变）；②`logo.svg` 顶部导航栏与登录页品牌 logo（与 favicon 同源，56px 登录页带光晕 hover 动画）；③`banner.svg` 首页顶部品牌横幅（网格背景 + 同步光效 + 标语 "分片上传 · 断点续传 · 秒传"）。所有 8 个 HTML 页面添加 favicon link，7 个页面 brand 区域统一引用 logo.svg，首页在 topbar 下方添加 banner 装饰条。SVG 矢量格式无损缩放，符合 CSP `img-src 'self'` 策略。
- **在线文件编辑（CodeMirror 6）**：支持在线新建和编辑文本文件（txt/md/js/ts/py/json/html/css/go/sql/yml/xml 等 40+ 扩展名）。后端新增 3 个 API（`POST /api/files/create` 创建、`GET /api/files/{id}/content` 读取、`PUT /api/files/{id}/content` 更新），Storage 接口扩展 `WriteFile` 方法（Local/S3/Router 三实现）。前端使用 CodeMirror 6 编辑器（esbuild 本地打包为 642KB IIFE bundle，避免 CDN 依赖，符合 CSP `script-src 'self'`），支持语法高亮、行号、括号匹配、代码折叠、缩进、Ctrl+S 快捷键保存。右键文件 → "编辑" 打开编辑器加载原内容，修改后保存即更新。暗色主题适配终端美学深色配色。


## 项目结构

```
filesync/
├── cmd/
│   ├── server/              # 服务端入口
│   │   └── main.go
│   └── client/              # CLI 客户端入口
│       └── main.go
├── internal/
│   ├── handler/
│   │   ├── upload.go        # 分片上传处理器
│   │   ├── download.go      # 下载处理器（Range 支持）
│   │   └── file.go          # 文件列表/查询处理器
│   ├── model/
│   │   └── model.go         # 数据模型定义
│   ├── storage/
│   │   ├── storage.go       # 存储接口定义
│   │   ├── local.go         # 本地文件存储实现
│   │   └── s3.go            # S3 对象存储实现（可扩展）
│   └── store/
│       └── db.go            # SQLite 数据库层
├── data/                    # 数据存储目录（自动创建）
├── README.md
├── go.mod
└── go.sum
```

## 环境要求

- Go 1.23+
- PowerShell / Git Bash（Windows）或 Bash（Linux/Mac）

## 快速开始

### 1. 克隆项目

```bash
cd filesync
```

### 2. 下载依赖

```bash
go mod tidy
```

### 3. 编译

```bash
# 编译服务端
go build -o server.exe ./cmd/server/

# 编译客户端
go build -o client.exe ./cmd/client/
```

### 4. 启动服务端

```bash
# 默认监听 :8080，数据存储在 ./data/
./server.exe

# 自定义配置（可选）
PORT=9090 DATA_DIR=/path/to/data ./server.exe

# 使用 S3 存储（需自行实现上传钩子）
STORAGE_TYPE=s3 S3_ENDPOINT=http://localhost:9000 S3_BUCKET=mybucket ./server.exe
```

### 5. 使用客户端

```bash
# 查看帮助
./client.exe -server http://localhost:8080

# 上传文件
./client.exe -server http://localhost:8080 upload 大文件.zip

# 列出所有文件
./client.exe -server http://localhost:8080 list

# 查看文件详情
./client.exe -server http://localhost:8080 info <file-id>

# 下载文件（支持断点续传）
./client.exe -server http://localhost:8080 download <file-id> <输出路径>
```

## Web 控制台

filesync 内置一个纯 HTML+CSS+JS 的 Web 控制台（无框架依赖，轻量），支持分片上传、断点续传、文件列表、下载、文件名冲突处理。前端仅做页面展示，所有业务方法走 `/api/*` 后端（规则15）。

### 启用方式

服务启动时自动检测 `./web/` 目录（可用 `WEB_DIR` 环境变量自定义路径）。目录存在时注册：

- `GET /web/` — 静态文件服务（index.html / style.css / app.js）
- `GET /` — 重定向到 `/web/index.html`

### 访问

浏览器打开 `http://<server>:<port>/` 或 `http://<server>:<port>/web/` 即可使用。

### 功能

- **分片上传**：拖拽/点击选择文件，自动分片（默认 512KB），可配置分片大小与并发数
- **断点续传**：上传中断后再次上传同一文件，自动查询已传分片跳过
- **进度显示**：实时进度条 + 已传/总大小，扫描线动画
- **冲突处理**：文件名冲突时弹窗选择 跳过 / 覆盖（?force=true）/ 重命名（?rename=true），支持"应用到所有"
- **目标目录**：上传时可指定目标目录（如 `docs/`），文件名前缀目录路径实现虚拟目录
- **树形文件库**：路径枚举方案，文件名中 `/` 作为虚拟目录分隔符，面包屑导航+目录展开，子目录在前文件在后，点击目录进入，点击下载文件
- **健康状态**：顶部实时显示服务健康状态（每 10 秒刷新）
- **账号注册**：邮箱+密码+确认密码，注册后发送激活邮件，点击激活链接激活账号
- **忘记密码**：输入邮箱发送 6 位验证码，凭验证码重置新密码
- **配置同步**：分片大小与并发数持久化到服务器，换浏览器自动加载（localStorage 即时响应 + 服务器后台同步）
- **分享链接**：文件/目录可生成分享链接（永久/7天/30天有效期），访客无需登录即可查看和下载，下载次数去重统计
- **秒传功能（全局存储）**：上传前 Web Worker 在后台计算完整文件 SHA256，调用 `/api/upload/check` 检查是否已存在相同哈希。命中则跳过整个上传流程，秒级完成；跨用户秒传（任意用户上传相同文件均可命中）；未命中或计算失败自动降级为正常上传
- **回收站**：文件删除后移入回收站（软删除），30 天内可恢复。回收站对话框支持列出文件、恢复（含冲突检测）、永久删除、清空操作。删除提示文字明确告知"移入回收站，30 天内可恢复"
- **压缩包预览**：单击 zip/tar.gz 等压缩包即弹窗显示包内文件树，目录可折叠展开。文件行右侧显示「预览」和「下载」按钮，预览按钮在内嵌 iframe 中加载文件内容（支持图片/PDF/文本/音视频等可预览类型），下载按钮触发附件下载。底部显示文件/目录统计。安全限制：包 ≤2GB / 条目 ≤10000 / 单文件 ≤500MB。
- **文件夹上传**：点击「+ 上传文件夹」按钮选择整个文件夹（webkitdirectory API），自动保留多级目录结构上传。复用现有 3 文件并发 + 单文件分片并发双层并发控制；冲突处理复用「应用到所有」批量决策；每文件独立进度条 + 失败隔离；空文件夹自动跳过（webkitdirectory 不返回空目录）。
- **目录重命名**：选中单个目录时工具栏按钮文案动态显示「重命名目录」（多选时显示「移动 N 项」），右键菜单也提供「重命名目录」入口。弹窗标题改为「重命名目录」，输入框默认选中末级目录名（如 `docs/sub/` 选中 `sub`），降低误改父级风险。后端复用 `MoveDir` API 批量更新目录前缀，零改动。

### 设计要点

- 终端美学深色主题（JetBrains Mono + Outfit 字体，电光青强调色）
- 左侧侧栏布局：功能按钮（刷新/新建目录/新建文件/多选/分享管理/回收站/设置）按「文件/工具/系统」分组，sticky 跟随滚动；窄屏下转为顶部横栏
- 上传配置独立设置 Modal：分片大小/并发数/存储位置从上传区域移至设置对话框，上传区域显示配置摘要标签（如"512KB · 5并发 · 本地"），点击可快速打开设置
- 加载状态用 shimmer 骨架屏替代纯文字「加载中」，空状态显示文件夹图标与引导文案；面板入场用 `panel-fade-in` 动画，树行与侧栏按钮有 `:active` 缩放微交互，尊重 `prefers-reduced-motion`
- 纯前端无构建步骤，3 个文件（index.html + style.css + app.js）
- 所有请求复用同源（避免 CORS），后端已开启 CORS 支持

## API 文档

### 上传

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/upload/init` | 初始化上传会话 |
| `POST` | `/api/upload/chunk` | 上传单个分片（multipart） |
| `GET` | `/api/upload/status?session_id=xxx` | 查询上传进度 |
| `POST` | `/api/upload/complete` | 完成上传并合并文件 |
| `POST` | `/api/upload/check` | 秒传检查（需认证） |

**初始化上传：**
```json
// POST /api/upload/init
// Request:
{
  "filename": "photo.jpg",
  "file_size": 10485760,
  "chunk_size": 524288,
  "storage": "local"
}

// Response 200:
{
  "session_id": "abc123...",
  "filename": "photo.jpg",
  "chunk_size": 524288,
  "total_chunks": 20,
  "storage_type": "local"
}

// Response 409 (冲突):
{
  "conflict": true,
  "message": "file 'photo.jpg' already exists...",
  "existing": { "id": "...", "filename": "photo.jpg", ... },
  "strategies": ["skip", "overwrite", "rename"]
}
```

**上传分片：**
```http
POST /api/upload/chunk
Content-Type: multipart/form-data

session_id: abc123...
chunk_index: 0
chunk_data: <binary>
```

**查询进度（用于断点续传恢复）：**
```
GET /api/upload/status?session_id=abc123...

Response:
{
  "session_id": "abc123...",
  "filename": "photo.jpg",
  "file_size": 10485760,
  "chunk_size": 524288,
  "total_chunks": 20,
  "received_chunks": [0, 1, 2],
  "missing_chunks": [3, 4, 5, ..., 19],
  "progress": "15.0%",
  "status": "active"
}
```

**完成上传：**
```json
// POST /api/upload/complete
// Request: { "session_id": "abc123..." }

// Response:
{
  "file_id": "def456...",
  "filename": "photo.jpg",
  "size": 10485760,
  "hash": "sha256hex...",
  "storage_path": "data/photo.jpg"
}
```

**秒传检查（上传前检查哈希是否已存在）：**
```json
// POST /api/upload/check（需认证）
// Request:
{
  "filename": "docs/report.pdf",
  "file_size": 10485760,
  "file_hash": "完整文件SHA256（64 hex字符）"
}

// Response 200（命中秒传）:
{
  "instant_upload": true,
  "file_id": "新文件ID",
  "filename": "docs/report.pdf",
  "size": 10485760,
  "hash": "sha256hex..."
}

// Response 200（未命中，需正常上传）:
{ "instant_upload": false }

// Response 409（目标文件名已存在）:
{ "error": "filename_conflict", "message": "目标文件名已存在" }
```
秒传范围全局（跨用户），任意 owner 的已完成文件命中即秒传；新记录共享源文件 storage_path（不复制物理文件）；hash+size 双重校验避免误判。前端计算 SHA256 失败自动降级为正常上传。永久删除时通过引用计数（CountByStoragePath）判断是否删除物理文件。

### 下载

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/download/{fileID}` | 下载文件（支持 Range 断点续传） |

**下载（完整文件）：**
```bash
curl -O http://localhost:8080/api/download/abc123...
```

**下载（断点续传）：**
```bash
# 已下载 1024 字节，从中断处继续
curl -H "Range: bytes=1024-" -o output.file \
  http://localhost:8080/api/download/abc123...
```

### 文件管理

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/files` | 列出已完成文件（支持 `?prefix=docs/` 递归过滤，`?shallow=1` 分层返回当前目录直接子项） |
| `GET` | `/api/files/{fileID}` | 查询文件详情 |
| `POST` | `/api/files/mkdir` | 新建目录（创建 `.keep` 占位文件） |
| `POST` | `/api/files/rename` | 重命名/移动文件 |
| `DELETE` | `/api/files/{fileID}` | 删除单个文件 |
| `DELETE` | `/api/files?prefix=docs/` | 递归删除目录（删除所有匹配前缀的文件） |
| `GET` | `/api/download/dir?prefix=docs/` | ZIP 流式打包下载目录（自动过滤 `.keep`） |

**按虚拟目录列出文件（路径枚举）：**
```bash
# 列出根目录所有文件（递归，含子目录）
curl http://localhost:8080/api/files

# 列出 docs/ 目录下所有文件（递归，含子目录）
curl "http://localhost:8080/api/files?prefix=docs/"

# 文件名约定：docs/report.pdf 表示 docs 目录下的 report.pdf
# 后端 LIKE 'docs/%' ESCAPE '\' 匹配，前端按 / 构建树形展示
```

**分层返回当前目录直接子项（shallow 模式，推荐用于大目录）：**
```bash
# 返回根目录的子目录列表 + 直接文件（不递归子目录）
curl "http://localhost:8080/api/files?shallow=1"
# 响应格式：{"dirs":[{"name":"docs","count":4}],"files":[{...}]}

# 返回 docs/ 目录的子目录列表 + 直接文件
curl "http://localhost:8080/api/files?prefix=docs/&shallow=1"
```
shallow 模式只返回当前目录的直接子项（子目录名+递归文件数 + 直接文件列表），
不递归加载子目录文件。适用于大目录场景，避免一次性返回过多数据。
默认模式（不传 shallow）仍然递归返回所有文件，向后兼容 CLI 客户端。

> 空目录（仅含 `.keep` 占位文件）也会在 `dirs` 中显示（`count=0`），新建文件夹后立即可见。`.keep` 占位文件不会出现在 `files` 列表中，`count` 统计也排除 `.keep`。

**新建目录（创建 .keep 占位文件）：**
```bash
curl -X POST http://localhost:8080/api/files/mkdir \
  -H "Content-Type: application/json" \
  -d '{"path":"docs/sub/"}'
# 响应 200: {"created":true,"path":"docs/sub/"}
# 响应 409: 目录已存在
# 响应 400: 路径非法（含 ..、//、\ 等）
```

**重命名/移动文件：**
```bash
curl -X POST http://localhost:8080/api/files/rename \
  -H "Content-Type: application/json" \
  -d '{"id":"<fileID>","new_filename":"docs/renamed.txt"}'
# 响应 200: {"renamed":true,"old_filename":"...","new_filename":"..."}
# 响应 409: 目标文件名已存在
# 响应 400: 文件名非法
```

**删除单个文件（软删除，移入回收站）：**
```bash
curl -X DELETE http://localhost:8080/api/files/<fileID>
# 响应 200: {"deleted":true,"id":"...","filename":"...","trashed":true}
# 文件移入回收站，保留 30 天可恢复，不删除存储文件
```

**递归删除目录（软删除，移入回收站）：**
```bash
curl -X DELETE "http://localhost:8080/api/files?prefix=docs/"
# 响应 200: {"deleted":true,"prefix":"docs/","files_deleted":N,"trashed":true}
# 目录下所有文件移入回收站，保留 30 天可恢复
```

**ZIP 打包下载目录：**
```bash
curl -o docs.zip "http://localhost:8080/api/download/dir?prefix=docs/"
# 响应 200: Content-Type: application/zip
# 流式打包，ZIP 内文件路径为相对路径（去掉 prefix 前缀）
# 自动过滤 .keep 占位文件
# 响应 404: 目录为空或不存在
```

**文件名路径校验规则（路径枚举方案）：**
- 允许 `/` 作为虚拟目录分隔符（如 `docs/sub/file.txt`）
- 禁止以 `/` 开头（如 `/docs/file.txt`）
- 禁止连续 `//`（如 `docs//file.txt`）
- 禁止 `..` 路径段（如 `../secret.txt`）
- 禁止反斜杠 `\`
- 长度限制 1-1024 字节
- 禁止空文件（file_size <= 0）：前端选择时拦截并提示"文件为空，已跳过"；后端返回 400 `file_size must be greater than 0`

### 回收站

文件删除采用软删除机制，移入回收站保留 30 天可恢复。过期后服务启动时自动清理（物理删除数据库记录 + 删除存储文件）。

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/trash` | 列出回收站文件（admin 可用 `?all=true` 查看所有用户） |
| `POST` | `/api/trash/{id}/restore` | 恢复文件（文件名冲突时返回 409） |
| `DELETE` | `/api/trash/{id}` | 永久删除单个文件（物理删除，不可恢复） |
| `DELETE` | `/api/trash` | 清空回收站（admin 可用 `?all=true` 清空所有用户） |

**列出回收站：**
```bash
curl http://localhost:8080/api/trash
# 响应 200: {"items":[...],"total":N,"retention":30}
# 每个 item 包含 id/filename/size/hash/owner/created_at/deleted_at/expires_at/is_expired
# admin 查看所有用户: curl "http://localhost:8080/api/trash?all=true"
```

**恢复文件：**
```bash
curl -X POST http://localhost:8080/api/trash/<fileID>/restore
# 响应 200: {"restored":true,"id":"...","filename":"..."}
# 响应 409: {"error":"filename_conflict","message":"恢复失败：同名文件已存在，请先重命名现有文件"}
```

**永久删除单个文件：**
```bash
curl -X DELETE http://localhost:8080/api/trash/<fileID>
# 响应 200: {"deleted":true,"id":"...","filename":"..."}
# 物理删除数据库记录 + 删除存储文件，不可恢复
```

**清空回收站：**
```bash
curl -X DELETE http://localhost:8080/api/trash
# 响应 200: {"deleted":true,"count":N,"fail":0}
# admin 清空所有用户: curl -X DELETE "http://localhost:8080/api/trash?all=true"
```

### 认证与账号

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/login` | 登录（支持用户名或邮箱，速率限制 40次/分钟/IP，password 字段 RSA 公钥加密传输） |
| `GET` | `/api/pubkey` | 获取 RSA 公钥（PEM 格式，前端加密 password 用，公开，Cache-Control 1 小时） |
| `POST` | `/api/logout` | 登出 |
| `GET` | `/api/me` | 当前用户信息（需认证） |
| `POST` | `/api/register` | 注册（邮箱+密码+确认密码，速率限制 3次/小时） |
| `GET` | `/api/activate?token=xxx` | 激活账号（从邮件链接点击） |
| `POST` | `/api/resend-activation` | 重新发送激活邮件（速率限制 3次/小时） |
| `POST` | `/api/forgot-password` | 忘记密码，发送验证码（速率限制 3次/小时） |
| `POST` | `/api/reset-password` | 重置密码（验证码+新密码） |

**注册账号：**
```bash
# POST /api/register
curl -X POST https://aistudy.icu/api/register \
  -H "Content-Type: application/json" \
  -d '{"email":"alice@example.com","password":"Pass1234","confirm_password":"Pass1234"}'
# 响应 200: {"success":true,"message":"若邮箱可用，激活邮件已发送，请查收邮箱"}
# 响应 400: 邮箱格式无效 / 密码强度不足 / 两次密码不一致
# 响应 503: 邮件服务未配置
```

**激活账号：**
```bash
# 用户点击邮件中的链接，后端自动处理并重定向
GET https://aistudy.icu/api/activate?token=abc123...
# 激活成功：重定向到 /web/login.html?activated=1
# token 无效：重定向到 /web/activate.html?status=invalid
# token 过期：重定向到 /web/activate.html?status=expired
```

**忘记密码：**
```bash
# POST /api/forgot-password
curl -X POST https://aistudy.icu/api/forgot-password \
  -H "Content-Type: application/json" \
  -d '{"email":"alice@example.com"}'
# 响应 200: {"success":true,"message":"若邮箱可用且已激活，验证码已发送"}
# 无论邮箱是否存在都返回成功（防枚举攻击）
```

**重置密码：**
```bash
# POST /api/reset-password
curl -X POST https://aistudy.icu/api/reset-password \
  -H "Content-Type: application/json" \
  -d '{"email":"alice@example.com","code":"123456","new_password":"NewPass1234","confirm_password":"NewPass1234"}'
# 响应 200: {"success":true,"message":"密码已重置，请登录"}
# 响应 400: 验证码错误/已过期/已使用，密码强度不足，两次密码不一致
```

### 分享与配置同步

| 方法 | 路径 | 说明 | 认证 |
|------|------|------|------|
| `POST` | `/api/share` | 创建分享（文件或目录） | 需认证 |
| `GET` | `/api/share` | 列出当前用户创建的所有分享 | 需认证 |
| `DELETE` | `/api/share/{id}` | 删除分享 | 需认证 |
| `GET` | `/api/s/{id}` | 获取分享公开信息（访客访问） | 公开 |
| `GET` | `/api/s/{id}/download` | 下载分享的文件或目录（目录打包为 ZIP） | 公开 |
| `GET` | `/api/settings` | 获取用户配置（分片大小、并发数） | 需认证 |
| `POST` | `/api/settings` | 保存用户配置 | 需认证 |

**创建分享：**
```bash
# POST /api/share
curl -X POST http://localhost:8080/api/share \
  -H "Content-Type: application/json" \
  -b "token=..." \
  -d '{"share_type":"file","file_id":"abc123...","expires_in":604800}'
# expires_in: 0=永久, 604800=7天, 2592000=30天
# 响应 200: {"id":"xyz789","share_type":"file","url":"/web/share.html?id=xyz789"}
```

**访客访问分享页面：**
```
GET http://<server>/web/share.html?id=xyz789
# 无需登录，页面显示文件名、大小、下载次数、有效期
# 点击下载按钮 → GET /api/s/xyz789/download
```

**下载次数去重机制：**
- 首次访问时设置 `visitor` cookie（30天有效，跨所有分享复用）
- 下载时 `share_downloads` 表的 `UNIQUE(share_id, visitor_id)` 约束保证同一访客只计数一次
- 清除 cookie 或换设备/浏览器后再次下载才会增加计数

**配置同步：**
```bash
# GET /api/settings（无记录时返回默认值 8MB/3）
curl http://localhost:8080/api/settings -b "token=..."
# 响应: {"username":"alice","chunk_size":524288,"concurrency":5,"updated_at":"..."}

# POST /api/settings
curl -X POST http://localhost:8080/api/settings \
  -H "Content-Type: application/json" \
  -b "token=..." \
  -d '{"chunk_size":1048576,"concurrency":8}'
```

### 存储用量与管理员后台

| 方法 | 路径 | 说明 | 认证 |
|------|------|------|------|
| `GET` | `/api/storage-usage` | 当前用户存储用量（admin 可用 `?username=xxx` 查指定用户，`?username=` 查全局） | 需认证 |
| `GET` | `/api/admin/stats` | 系统统计总览（用户/文件/存储/分享/回收站） | 需认证 + admin |
| `GET` | `/api/admin/users` | 用户列表（含各自存储用量） | 需认证 + admin |
| `POST` | `/api/admin/users/{id}/status` | 禁用/启用用户（body: `{"status":"active"\|"disabled"}`） | 需认证 + admin |
| `POST` | `/api/admin/users/{id}/reset-password` | 重置用户密码（body: `{"new_password":"xxx"}`） | 需认证 + admin |
| `GET` | `/api/admin/shares` | 所有分享列表 | 需认证 + admin |
| `DELETE` | `/api/admin/shares/{id}` | 删除分享 | 需认证 + admin |

**存储用量查询：**
```bash
# 普通用户查询自己的用量
curl http://localhost:8080/api/storage-usage -b "token=..."
# 响应: {"used_size":12345678,"file_count":42,"trash_size":1024,"trash_count":3}

# admin 查看指定用户
curl "http://localhost:8080/api/storage-usage?username=alice" -b "token=..."
# admin 查看全局总量
curl "http://localhost:8080/api/storage-usage?username=" -b "token=..."
```

**管理员后台页面：**
```
GET http://<server>/web/admin.html
# 需 admin 权限，非 admin 自动重定向到 /web/index.html
# 4 个 Tab：系统总览 / 用户管理 / 文件管理 / 分享管理
```

**禁用/启用用户：**
```bash
curl -X POST http://localhost:8080/api/admin/users/<userID>/status \
  -H "Content-Type: application/json" \
  -b "token=..." \
  -d '{"status":"disabled"}'
# 防护：不能禁用自己、不能修改其他 admin 账号
```

**重置用户密码：**
```bash
curl -X POST http://localhost:8080/api/admin/users/<userID>/reset-password \
  -H "Content-Type: application/json" \
  -b "token=..." \
  -d '{"new_password":"NewPass@123"}'
# 密码需通过强度校验（至少 8 位，含大小写和数字）
```

### 预览与压缩包解压

| 方法 | 路径 | 说明 | 认证 |
|------|------|------|------|
| `GET` | `/api/preview/{fileID}` | 文件预览元数据（类型、可预览性、各资源 URL） | 需认证 |
| `GET` | `/api/preview/{fileID}/content` | 原始内容流（支持 Range 206，inline 显示） | 需认证 |
| `GET` | `/api/preview/{fileID}/thumb?size=small\|medium\|large` | 图片缩略图（首次生成后落盘缓存） | 需认证 |
| `GET` | `/api/preview/{fileID}/poster` | 视频海报（ffmpeg 截取首帧，缓存） | 需认证 |
| `GET` | `/api/preview/{fileID}/archive` | 列出压缩包内文件树 | 需认证 |
| `GET` | `/api/preview/{fileID}/archive?path=xxx` | 提取压缩包内单个文件（inline 预览） | 需认证 |
| `GET` | `/api/preview/{fileID}/archive?path=xxx&download=1` | 提取压缩包内单个文件（attachment 下载） | 需认证 |

**预览元数据：**
```bash
curl http://localhost:8080/api/preview/<fileID> -b "token=..."
# 响应 200:
{
  "type": "archive",            // image|pdf|text|code|audio|video|office|archive|unsupported
  "filename": "docs.zip",
  "size": 1048576,
  "supported": true,
  "urls": {
    "original": "/api/preview/<fileID>/content",
    "archive_list": "/api/preview/<fileID>/archive",
    "archive_extract": "/api/preview/<fileID>/archive?path="
  }
}
# type=image 时额外返回 thumb_small/thumb_medium/thumb_large
# type=video 时额外返回 poster
```

**列出压缩包内容：**
```bash
curl http://localhost:8080/api/preview/<fileID>/archive -b "token=..."
# 响应 200:
{
  "entries": [
    {"path":"docs/","is_dir":true,"size":0},
    {"path":"docs/readme.md","is_dir":false,"size":1024},
    {"path":"docs/img/logo.png","is_dir":false,"size":20480}
  ],
  "total": 3,
  "truncated": false            // 条目超过 10000 时为 true
}
# 响应 403: 非 owner 且非 admin（仅 owner 或 admin 可预览）
# 响应 413: 压缩包大小超过 2GB
# 响应 500: 解析失败（损坏的压缩包、加密压缩包、不支持的格式）
```

**提取压缩包内单个文件（在线预览）：**
```bash
# inline 预览（浏览器内嵌显示，自动设置 Content-Type）
curl "http://localhost:8080/api/preview/<fileID>/archive?path=docs/readme.md" -b "token=..."
# 响应 200: Content-Type 根据文件扩展名设置
#          Content-Disposition: inline; filename="readme.md"
# 响应 404: 压缩包内无此文件
# 响应 413: 提取文件超过 500MB
```

**提取压缩包内单个文件（下载）：**
```bash
# attachment 下载
curl "http://localhost:8080/api/preview/<fileID>/archive?path=docs/img/logo.png&download=1" -b "token=..."
# 响应 200: Content-Disposition: attachment; filename="logo.png"
```

**支持的压缩包格式与安全限制：**
- 支持格式：`.zip` / `.tar` / `.tar.gz` / `.tgz` / `.tar.bz2` / `.tbz2` / `.gz`（单文件）/ `.bz2`（单文件）
- 包大小限制：≤ 2GB（防临时文件占满磁盘，zip 走临时文件方案）
- 条目数限制：≤ 10000（防压缩炸弹，超出部分截断并 `truncated=true`）
- 单文件提取限制：≤ 500MB
- Zip Slip 防护：拒绝 `..` 路径段和绝对路径
- 加密压缩包拒绝：zip 标准库不支持 AES，返回 500

## 断点续传验证步骤

### 验证上传断点续传

1. **启动服务端**
   ```bash
   ./server.exe
   ```

2. **准备测试文件**（建议 10MB+ 大文件以体现分片效果）
   ```bash
   # Linux/Mac
   dd if=/dev/urandom of=test_large.dat bs=1M count=10

   # Windows PowerShell
   [System.IO.File]::WriteAllBytes('test_large.dat', [byte[]]::new(10485760))
   ```

3. **开始上传，过程中中断**
   ```bash
   # 客户端开始上传（观察分片逐个上传）
   ./client.exe -server http://localhost:8080 upload test_large.dat
   
   # 在分片上传到一半时，按 Ctrl+C 中断程序
   ```

4. **查询上传进度（确认部分分片已上传）**
   ```bash
   # 从日志中找到 Session ID，或者修改客户端代码显示 Session ID
   curl "http://localhost:8080/api/upload/status?session_id=<session_id>"
   # 确认有部分分片已接收
   ```

5. **重新执行上传（自动恢复）**
   ```bash
   # 再次执行上传命令，客户端会自动检测已上传分片并跳过
   ./client.exe -server http://localhost:8080 upload test_large.dat
   # 观察输出：已上传的分片显示 "already uploaded, skipping"
   ```

6. **验证文件完整性**
   ```bash
   ./client.exe -server http://localhost:8080 list
   ./client.exe -server http://localhost:8080 info <file_id>
   # 对比 SHA256 是否一致
   ```

### 验证下载断点续传

1. **开始下载，过程中中断**
   ```bash
   # 开始下载大文件
   ./client.exe -server http://localhost:8080 download <file_id> output.dat
   # 下载到一半时按 Ctrl+C 中断
   ```

2. **确认部分文件存在**
   ```bash
   ls -la output.dat  # 文件大小 < 完整大小
   ```

3. **重新下载（自动从断点恢复）**
   ```bash
   # 再次执行下载命令，自动从断点继续
   ./client.exe -server http://localhost:8080 download <file_id> output.dat
   # 输出显示 "Partial file found: xxx bytes downloaded, resuming..."
   ```

4. **验证下载完成**
   ```bash
   # 文件大小与原始文件一致
   ls -la test_large.dat output.dat
   
   # 或通过客户端查看服务器记录的 SHA256
   ./client.exe -server http://localhost:8080 info <file_id>
   ```

### 验证网络断开恢复

1. 上传过程中断开网络（禁用网卡/WiFi）
2. 客户端会显示错误（`upload chunk N: ...`）
3. 重新连接网络
4. 再次执行上传命令
5. 客户端从断点继续上传

### 验证文件冲突处理

1. **上传一个文件两次**
   ```bash
   echo "hello" > conflict_test.txt
   ./client.exe -server http://localhost:8080 upload conflict_test.txt
   
   # 修改文件内容
   echo "world" > conflict_test.txt
   ./client.exe -server http://localhost:8080 upload conflict_test.txt
   ```

2. **选择冲突策略**
   ```
   Conflict: file 'conflict_test.txt' already exists...
   Strategies: skip, overwrite, rename
   Choose strategy:
   ```
   - `skip`: 取消上传
   - `overwrite`: 强制覆盖
   - `rename`: 上传为新文件

## 配置说明

| 环境变量 | 说明 | 默认值 |
|----------|------|--------|
| `PORT` | 服务端监听端口 | `8080` |
| `DATA_DIR` | 数据存储目录 | `./data` |
| `STORAGE_TYPE` | 存储类型（`local` / `s3`） | `local` |
| `S3_ENDPOINT` | S3 端点地址 | `http://localhost:9000` |
| `S3_REGION` | S3 区域 | `us-east-1` |
| `S3_BUCKET` | S3 Bucket 名称 | `filesync` |
| `S3_ACCESS_KEY` | S3 访问密钥 | - |
| `S3_SECRET_KEY` | S3 密钥 | - |
| `S3_USE_SSL` | 是否使用 SSL | `false` |
| `REDIS_ADDR` | 单机 Redis 地址（如 `127.0.0.1:6379`） | - |
| `REDIS_PASSWORD` | Redis 密码 | - |
| `REDIS_DB` | Redis 数据库编号 | `0` |
| `REDIS_SENTINEL_ADDRS` | Sentinel 地址列表（逗号分隔，启用后优先于单机模式） | - |
| `REDIS_SENTINEL_MASTER` | Sentinel 主节点名称 | `mymaster` |
| `SMTP_HOST` | SMTP 服务器地址（注册/忘记密码功能必填） | `smtp.qiye.aliyun.com` |
| `SMTP_PORT` | SMTP 端口（465=SSL implicit, 587=STARTTLS） | `465` |
| `SMTP_USER` | SMTP 用户名（发件邮箱） | - |
| `SMTP_PASS` | SMTP 授权码 | - |
| `SMTP_FROM` | 发件人显示名 | `FileSync <SMTP_USER>` |
| `APP_BASE_URL` | 应用根地址（用于拼接激活链接） | 根据 DOMAIN 推导为 `https://<DOMAIN>` |
| `JWT_SECRET` | 分享下载 token 的 HMAC-SHA256 密钥（登录 JWT 已改用 RS256 非对称签名，密钥自动生成于 `data/jwt_rsa_private.pem`） | 随机生成（重启失效） |
| `TRUSTED_PROXIES` | 可信代理 IP 列表（逗号分隔，仅这些 IP 的 X-Forwarded-For 被采信，防 XFF 伪造绕过限流） | - |

> Redis 为可选项，未配置时自动降级为纯 SQLite 模式。配置 Sentinel 后优先使用 Sentinel 分布式模式。

### 阿里云 OSS 对象存储配置

本项目已接入阿里云 OSS（S3 兼容协议），启用后新文件写入 OSS，旧文件保留本地（混合存储模式）。完整说明见 [doc/OSS对象存储配置.md](doc/OSS对象存储配置.md)。

**已配置的 Bucket：**

| 属性 | 值 |
|------|-----|
| Bucket 名称 | `aistudy-filesync` |
| 地域 | 华南1（深圳） / `oss-cn-shenzhen` |
| Endpoint | `oss-cn-shenzhen.aliyuncs.com` |
| 存储类型 | 标准存储 / 本地冗余 |
| 读写权限 | 私有（阻止公共访问已开启） |
| RAM 用户 | `aistudy-filesync`（仅授予 `AliyunOSSFullAccess`） |

**启用方式：**

```bash
# 本地开发：在 filesync/.env 中配置（已加入 .gitignore，不入库）
S3_ENDPOINT=oss-cn-shenzhen.aliyuncs.com
S3_REGION=oss-cn-shenzhen
S3_BUCKET=aistudy-filesync
S3_ACCESS_KEY=<your-access-key-id>
S3_SECRET_KEY=<your-access-key-secret>
S3_USE_SSL=true
```

> 同地域 ECS 部署可改用内网 Endpoint `oss-cn-shenzhen-internal.aliyuncs.com` 免流量费。
> 安全提示：禁止将 AccessKey 提交到 git 仓库；高安全场景可改用 STS Token 临时凭证方案。

### Presigned URL 直连 OSS（带宽优化）

启用 S3 存储后，系统自动走 presigned URL 直连模式：客户端直接 PUT/GET OSS，数据不经过应用服务器，节省双倍带宽消耗。

**工作原理：**

| 场景 | 流程 |
|------|------|
| 上传（小文件 <5MB） | InitUpload 返回单个 presigned PUT URL → 客户端直接 PUT 整个文件到 OSS → CompleteUpload 验证对象 |
| 上传（大文件 ≥5MB） | InitUpload 返回多个分片 presigned PUT URL → 客户端并发 PUT 各分片 → CompleteUpload 调用 ComposeObject 服务端合并（数据在 OSS 内部复制） |
| 下载 | DownloadFile 生成 presigned GET URL → 302 重定向到 OSS → 浏览器直接从 OSS 下载 |

**断点续传：** 大文件分片上传时，InitUpload 会通过 ListParts 查询已上传分片，客户端跳过已完成分片。

**CORS 配置（必须）：**

阿里云 OSS Bucket 必须配置 CORS，否则浏览器 PUT 请求会被拦截。通过阿里云控制台或 API 配置：

| 配置项 | 值 |
|--------|-----|
| AllowedOrigin | `https://aistudy.icu`, `http://localhost:8080` |
| AllowedMethod | `PUT`, `GET`, `HEAD` |
| AllowedHeader | `*` |
| ExposeHeader | `ETag` |
| MaxAgeSeconds | `3600` |

> 本地开发环境 origin 为 `http://localhost:8080`，生产环境为 `https://aistudy.icu`。

**降级机制：** presigned URL 生成失败时自动降级为中转模式（数据经应用服务器），保证可用性。

**权限控制：** 分片大小和并发数设置仅管理员可修改，普通用户 select 禁用（仍可读取当前配置）。

## 服务器部署（systemd）

### 1. 编译 Linux 二进制（在 Windows 本地交叉编译）

```bash
# 在项目根目录执行
$env:GOOS="linux"; $env:GOARCH="amd64"; go build -o server_linux ./cmd/server/
$env:GOOS="linux"; $env:GOARCH="amd64"; go build -o bench_linux ./cmd/bench/
```

### 2. 上传到服务器

通过 SCP / SFTP 将 `server_linux` 上传到服务器 `/opt/filesync/server`，并赋予执行权限：

```bash
chmod +x /opt/filesync/server
mkdir -p /opt/filesync/data
```

### 3. 创建 systemd 服务

在 `/etc/systemd/system/filesync.service` 写入：

```ini
[Unit]
Description=FileSync Server
After=network.target redis.service

[Service]
Type=simple
User=root
WorkingDirectory=/opt/filesync
ExecStart=/opt/filesync/server
Environment=PORT=8888
Environment=DATA_DIR=/opt/filesync/data
Environment=STORAGE_TYPE=local
Environment=REDIS_ADDR=127.0.0.1:6379
Environment=REDIS_PASSWORD=your_redis_password
Environment=REDIS_DB=0
Environment=SMTP_HOST=smtp.qiye.aliyun.com
Environment=SMTP_PORT=465
Environment=SMTP_USER=px-ai@aistudy.icu
Environment=SMTP_PASS=your_smtp_password
Environment=SMTP_FROM=FileSync <px-ai@aistudy.icu>
Environment=APP_BASE_URL=https://your-domain.com
Restart=on-failure
RestartSec=5
StandardOutput=append:/opt/filesync/server.log
StandardError=append:/opt/filesync/server.log
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

### 4. 启动并设置开机自启

```bash
systemctl daemon-reload
systemctl enable --now filesync
systemctl status filesync
```

### 5. 验证服务

```bash
# 健康检查（返回 healthy:true 即正常）
curl http://127.0.0.1:8888/api/health

# 公网访问（需确保安全组放行 8888 端口）
curl http://<服务器公网IP>:8888/api/health
```

### 常用运维命令

```bash
systemctl restart filesync         # 重启
systemctl stop filesync            # 停止
journalctl -u filesync -f          # systemd 实时日志
tail -f /opt/filesync/server.log   # 应用日志
```

> **注意**：若 Redis 启用了认证（`requirepass`），务必配置正确的 `REDIS_PASSWORD`，否则 watchdog 会持续报 `WRONGPASS` 并降级到 SQLite 模式。

## Redis Sentinel 高可用集群部署

filesync 支持 Redis Sentinel 模式，实现 Redis 高可用（主节点宕机自动切换）。以下是在服务器上部署 Sentinel 集群的步骤。

### 1. 准备配置目录

```bash
mkdir -p /opt/redis-sentinel && cd /opt/redis-sentinel
```

### 2. 编写 docker-compose.yml（host 网络模式）

> **关键**：使用 `network_mode: host` 避免 Docker 网络隔离问题。若服务器 6379 已被占用，需改用其他端口（如 16379/16380/16381 + 36379/36380/36381）。

```yaml
services:
  redis-master:
    image: redis:7-alpine
    container_name: filesync-redis-master
    restart: unless-stopped
    network_mode: host
    command: >
      redis-server --appendonly yes
      --port 16379 --requirepass redis123

  redis-replica-1:
    image: redis:7-alpine
    container_name: filesync-redis-replica-1
    restart: unless-stopped
    network_mode: host
    command: >
      redis-server --appendonly yes
      --port 16380 --slaveof 127.0.0.1 16379
      --requirepass redis123 --masterauth redis123
    depends_on:
      - redis-master

  redis-replica-2:
    image: redis:7-alpine
    container_name: filesync-redis-replica-2
    restart: unless-stopped
    network_mode: host
    command: >
      redis-server --appendonly yes
      --port 16381 --slaveof 127.0.0.1 16379
      --requirepass redis123 --masterauth redis123
    depends_on:
      - redis-master

  sentinel-1:
    image: redis:7-alpine
    container_name: filesync-sentinel-1
    restart: unless-stopped
    network_mode: host
    command: redis-sentinel /etc/redis/sentinel.conf
    volumes:
      - ./sentinel-1.conf:/etc/redis/sentinel.conf
    depends_on:
      - redis-master
      - redis-replica-1
      - redis-replica-2

  sentinel-2:
    image: redis:7-alpine
    container_name: filesync-sentinel-2
    restart: unless-stopped
    network_mode: host
    command: redis-sentinel /etc/redis/sentinel.conf
    volumes:
      - ./sentinel-2.conf:/etc/redis/sentinel.conf
    depends_on:
      - redis-master
      - redis-replica-1
      - redis-replica-2

  sentinel-3:
    image: redis:7-alpine
    container_name: filesync-sentinel-3
    restart: unless-stopped
    network_mode: host
    command: redis-sentinel /etc/redis/sentinel.conf
    volumes:
      - ./sentinel-3.conf:/etc/redis/sentinel.conf
    depends_on:
      - redis-master
      - redis-replica-1
      - redis-replica-2
```

### 3. 编写 sentinel.conf（3 个文件，端口不同）

**sentinel-1.conf**：
```ini
port 36379
sentinel monitor mymaster 127.0.0.1 16379 2
sentinel auth-pass mymaster redis123
sentinel down-after-milliseconds mymaster 5000
sentinel failover-timeout mymaster 15000
sentinel parallel-syncs mymaster 1
```

**sentinel-2.conf** / **sentinel-3.conf**：同上，但 `port` 分别改为 `36380` / `36381`。

```bash
# 确保配置文件可写（Sentinel 运行时会重写）
chmod 666 sentinel-*.conf
```

### 4. 启动集群

```bash
docker compose up -d
docker compose ps
```

### 5. 验证集群

```bash
# Sentinel quorum 检查（应返回 OK 3 usable Sentinels）
redis-cli -h 127.0.0.1 -p 36379 sentinel ckquorum mymaster

# Master 地址（应返回 127.0.0.1 16379）
redis-cli -h 127.0.0.1 -p 36379 sentinel get-master-addr-by-name mymaster

# 数据同步测试
redis-cli -h 127.0.0.1 -p 16379 -a redis123 set test "ha_ok"
redis-cli -h 127.0.0.1 -p 16380 -a redis123 get test  # 应返回 ha_ok
```

### 6. 配置 filesync 使用 Sentinel

修改 `/etc/systemd/system/filesync.service` 的环境变量：

```ini
# 注释或删除单机模式
# Environment=REDIS_ADDR=127.0.0.1:6379

# 启用 Sentinel 模式
Environment=REDIS_SENTINEL_ADDRS=127.0.0.1:36379,127.0.0.1:36380,127.0.0.1:36381
Environment=REDIS_SENTINEL_MASTER=mymaster
Environment=REDIS_PASSWORD=redis123
Environment=REDIS_DB=0
```

```bash
systemctl daemon-reload
systemctl restart filesync
curl http://127.0.0.1:8888/api/health  # 应返回 healthy:true
```

### 常见问题

| 问题 | 原因 | 解决方案 |
|------|------|----------|
| `NOQUORUM` | Sentinel 之间无法互发现 | 用 `host` 网络模式，不要用 `sentinel announce-ip` |
| `WRONGPASS` | 密码不匹配或连接到错误的 Redis | 确认 `REDIS_PASSWORD` 与 `--requirepass` 一致 |
| Sentinel 返回 Docker 内网 IP | bridge 网络模式下 master 地址不可达 | 改用 `host` 网络模式 |
| `healthy:false` | filesync 无法连接 master | 检查 Sentinel 报告的 master 地址是否宿主机可达 |

## 性能测试

filesync 自带 `cmd/bench` 压测工具，支持 InitUpload / UploadChunk / GetUploadStatus / CompleteUpload 四类接口在多并发度下的 QPS、P50/P90/P99、错误率统计。

### 快速压测

```bash
# 服务器本机回环测试（排除网络延迟）
cd /opt/filesync
./bench -server http://127.0.0.1:8888
# 结果保存到 bench_result_YYYYMMDD_HHMMSS.json
```

### 实测结果（2026-07-05，1.8GB 内存小规格服务器）

| 接口 | 优化前 QPS | Pipeline 优化后 | 限流+回退后 | 最优并发 | 错误率 |
|------|-----------|----------------|------------|----------|--------|
| GetUploadStatus | 8769 | 9964 | **7939** | 1000 | 0% |
| UploadChunk | 5594 | 5368 | **5068** | 1000 | 0% |
| InitUpload | 4328 | 5446 | **5757** | 2000（429 限流 35%） | 0%（429 为预期） |
| CompleteUpload | 842 | 945 | **882** | 30 | 0% |

- 16 个测试用例（并发 5~2000）**零非预期错误**
- Redis Sentinel failover 后服务自动恢复，性能无损
- 最优并发区间：**500-1000**（c>1000 时 InitUpload 触发 429 限流保护）
- **Pipeline 优化**：InitUpload 合并 3 RTT → 1 RTT，QPS +26%
- **限流保护**：InitUpload c=2000 时 429 拒绝 35%，成功请求 QPS +24%，P99 -35%
- **读写分离回退**：实测"写后立即读"场景下副本 miss 导致 fallback，QPS -91%，已回退走 master

详细报告见 [doc/性能测试报告_20260705.md](doc/性能测试报告_20260705.md)。

## 设计要点

### 分片上传流程

```
Client                          Server
  |                                |
  |-- POST /upload/init --------->|  初始化上传，获取 session_id
  |<--- { session_id } -----------|
  |                                |
  |-- POST /upload/chunk --------->|  逐个上传分片
  |   (session_id, chunk_index,   |
  |    chunk_data)                |
  |<--- { received: true } -------|
  |                                |
  | (中断后恢复)                    |
  |-- GET /upload/status --------->|  查询已接收分片
  |<--- { missing_chunks: [] } ---|
  |-- POST /upload/chunk --------->|  仅上传缺失分片
  |   ...                          |
  |                                |
  |-- POST /upload/complete ------>|  合并所有分片
  |<--- { file_id, hash } --------|
```

### 下载断点续传

利用 HTTP 标准 `Range` 请求头，服务端无需额外状态：

```
Client (已下载 1024 字节)
  |
  |-- GET /download/{id} --------->|
  |   Range: bytes=1024-          |
  |<--- 206 Partial Content -------|
  |   Content-Range: bytes 1024-N/N |
  |   (继续传输剩余数据)              |
```

## 技术架构

### 数据库设计

项目使用 SQLite 存储元数据，通过 `modernc.org/sqlite`（纯 Go 实现，零 CGO 依赖）驱动。包含三张核心表：

#### 表结构

**upload_sessions** — 上传会话表，记录每次分片上传的进度：

```sql
CREATE TABLE upload_sessions (
    id TEXT PRIMARY KEY,             -- 会话 ID（32 位随机 hex）
    filename TEXT NOT NULL,          -- 原始文件名
    file_size INTEGER NOT NULL,      -- 文件总大小（字节）
    file_hash TEXT DEFAULT '',       -- 客户端预计算的 SHA256（可选）
    chunk_size INTEGER NOT NULL,     -- 分片大小（默认 512KB）
    total_chunks INTEGER NOT NULL,   -- 总分片数
    status TEXT NOT NULL DEFAULT 'active',  -- active | completed | cancelled
    storage_type TEXT NOT NULL DEFAULT 'local', -- local | s3
    created_at TEXT NOT NULL,        -- ISO 8601 时间戳
    updated_at TEXT NOT NULL
);
```

**chunks** — 分片记录表，记录已接收的每个分片：

```sql
CREATE TABLE chunks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,        -- 关联的上传会话
    chunk_index INTEGER NOT NULL,    -- 分片序号（从 0 开始）
    size INTEGER NOT NULL DEFAULT 0, -- 分片实际大小（字节）
    hash TEXT DEFAULT '',            -- 分片 SHA256（当前未使用，预留）
    created_at TEXT NOT NULL,
    FOREIGN KEY (session_id) REFERENCES upload_sessions(id),
    UNIQUE(session_id, chunk_index)  -- 保证同一分片不重复录入，天然幂等
);
```

**files** — 已完成文件表，上传完成合并后写入：

```sql
CREATE TABLE files (
    id TEXT PRIMARY KEY,             -- 文件 ID（32 位随机 hex）
    filename TEXT NOT NULL,          -- 文件名
    size INTEGER NOT NULL,           -- 文件大小
    hash TEXT DEFAULT '',            -- SHA256 校验值
    storage_path TEXT NOT NULL,      -- 存储路径
    storage_type TEXT NOT NULL DEFAULT 'local',
    chunk_size INTEGER NOT NULL,
    total_chunks INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'completed',
    owner TEXT NOT NULL DEFAULT '',  -- 文件归属用户名（空=历史数据/公共，仅 admin 可操作）
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_files_owner ON files(owner);
```

> **owner 字段说明**：上传时记录发起者用户名（`auth.UsernameFromContext`）。admin 可操作所有文件；普通用户仅能操作 `owner = 自己用户名` 的文件；历史文件 `owner = ''` 仅 admin 可访问。分享链接公开下载时按 `shares.created_by` 过滤，防止同 prefix 下跨用户文件泄露。

#### 数据模型关系

```
┌─────────────────────┐
│   upload_sessions   │
│  (1 个会话)          │
│  id: "abc123..."    │
│  filename: "a.zip"  │
│  total_chunks: 20   │
│  status: "active"   │
└─────────┬───────────┘
          │ 1:N
          ▼
┌─────────────────────┐         ┌─────────────────────┐
│       chunks        │         │       files         │
│  (N 个分片记录)      │  完成   │  (1 个文件记录)      │
│  session_id → FK    │ ──────→ │  id: "def456..."    │
│  chunk_index: 0..19 │  后创建  │  hash: "sha256..."  │
│  UNIQUE 约束保证幂等  │         │  status: completed  │
└─────────────────────┘         └─────────────────────┘
```

**生命周期**：

1. 客户端 `POST /api/upload/init` → 创建 `upload_sessions` 记录
2. 客户端逐片 `POST /api/upload/chunk` → 写入 `chunks` 表（`INSERT OR REPLACE` 幂等）
3. 中断后，客户端 `GET /api/upload/status` → 从 `chunks` 表查询已接收分片，跳过重复
4. 全部完成 `POST /api/upload/complete` → 合并分片，写入 `files` 表，清理 chunks

### 断点续传机制详解

#### 上传断点续传

```
中断前：已上传 chunks [0,1,2,3]，chunks 表已记录
   │
   ▼ 程序/网络中断
   │
恢复后：客户端调用 GET /api/upload/status
   │      返回 received_chunks: [0,1,2,3]，missing_chunks: [4..19]
   │
   ▼ 客户端仅上传分片 4~19
   │      服务端 INSERT OR REPLACE 写入 chunks 表（幂等）
   │
   ▼ POST /api/upload/complete → 合并所有 20 个分片
```

**关键设计**：

- `chunks` 表的 `UNIQUE(session_id, chunk_index)` 约束 + `INSERT OR REPLACE` 语句，保证同一个分片无论上传多少次都不会产生重复记录
- 客户端每次上传前主动查询服务端进度，而非依赖本地缓存，确保状态一致性
- `upload_sessions` 表持久化 session 状态，服务重启后会话仍然有效
- **文件名标记时机**：Redis 集合 `files:names` 仅在 `CompleteUpload` 成功后通过 `MarkFileExists` 标记文件名，**不在 init 阶段提前标记**。这样未完成上传的文件不会触发 409 冲突，用户可重新上传（修复了"刷新中断后再上传报同名冲突"的 bug）

#### 下载断点续传

利用标准 HTTP `Range` 请求头，服务端无需额外维护状态：

```
客户端本地已有 1024 字节 → 检测部分文件存在
   │
   ▼ 发送请求：GET /api/download/{id}
   │           Range: bytes=1024-
   │
   ▼ 服务端 Seek 到 offset 1024，返回 206 Partial Content
   │          Content-Range: bytes 1024-10485759/10485760
   │
   ▼ 客户端以追加模式写入，续传完成
```

**关键设计**：

- 服务端通过 `io.Seeker` 定位到指定偏移量，`io.CopyN` 限制传输大小
- 响应头 `Accept-Ranges: bytes` 告知客户端支持范围请求
- 下载完成后客户端计算本地文件 SHA256，与服务器记录比对（完整性校验）

### 并发安全

| 策略 | 说明 |
|------|------|
| SQLite 单写连接 | `sql.DB.SetMaxOpenConns(1)` 避免并发写冲突 |
| Handler 无状态设计 | Handler 结构体仅持有 `*store.DB` 和 `storage.Storage` 引用，每次请求独立处理 |
| 分片幂等 | `UNIQUE(session_id, chunk_index)` 约束保证同一分片多次上传安全 |
| CORS 全开放 | `Access-Control-Allow-Origin: *` 允许跨域（开发/测试阶段） |

> **生产环境建议**：替换 SQLite 为 PostgreSQL/MySQL 以支持真正的高并发写入；限制 CORS 白名单。

### 存储抽象层

```
┌──────────────────────────────────────────┐
│              Storage 接口                 │
│  SaveChunk / ReadChunk / AssembleFile    │
│  DeleteTemp / ReadFile / FileSize        │
│  BasePath                                │
└──────────────┬───────────────────────────┘
               │
    ┌──────────┴──────────┐
    │                     │
    ▼                     ▼
┌──────────┐      ┌──────────────┐
│ Local    │      │   S3         │
│ 本地文件  │      │  对象存储     │
│ 系统存储  │      │  (hook模式)  │
└──────────┘      └──────────────┘
```

- **LocalStorage**：直接在文件系统 `{DATA_DIR}/` 下操作，分片临时目录为 `{DATA_DIR}/_chunks/{sessionID}/`
- **S3Storage**：分片仍暂存本地，合并完成后通过 `RegisterS3Upload(hook)` 注入的上传函数将完整文件推送至 S3
- 新增存储后端只需实现 `Storage` 接口，无需修改 handler 层代码

### 分片大小选择

| 分片大小 | 适用场景 | 权衡 |
|----------|---------|------|
| 256 KB | 弱网、频繁中断 | 请求次数多，HTTP 开销大 |
| **512 KB**（默认） | **通用场景** | **请求数与单次传输的平衡点** |
| 1 MB+ | 稳定高速网络、超大文件 | 请求少但中断后重传代价大 |

默认 512KB 在大多数网络条件下表现良好。可通过客户端 `chunkSize` 常量调整。

### 错误处理策略

| 场景 | 处理方式 |
|------|---------|
| 分片上传网络超时 | 客户端返回错误提示，session 保留，用户可重新执行上传自动恢复 |
| 服务端磁盘满 | `SaveChunk` 返回错误 → HTTP 500，客户端终止并提示重试 |
| 分片数不完整调用完成 | 服务端拒绝，返回 `"not all chunks received: X/Y"` |
| 文件不存在下载 | HTTP 404 `"file not found"` |
| 同名文件冲突 | HTTP 409 + 三种策略（skip / overwrite / rename） |
| Range 越界 | HTTP 416 `"range not satisfiable"` |

### 性能考量

| 方面 | 说明 |
|------|------|
| 零 CGO 依赖 | `modernc.org/sqlite` 纯 Go 编译，无需 GCC，跨平台编译零配置 |
| 内存控制 | 分片上传逐片读取 512KB 缓冲区，大文件不会撑爆内存 |
| 单二进制部署 | 服务端和客户端均编译为独立 exe，无需运行时依赖 |
| 分片合并 | AssembleFile 逐片 `io.Copy` 拼接，利用系统文件缓存 |
| 目录分层加载 | `?shallow=1` 仅返回当前目录直接子项（子目录名+递归文件数+直接文件），SQL `GROUP BY` 一次性提取子目录。生产实测：根目录 484KB→8.8KB（-98.2%），24s→49ms（-99.8%） |

## 技术栈

- **语言**: Go 1.23+
- **数据库**: SQLite (modernc.org/sqlite，纯 Go 实现，无需 CGO)
- **存储**: 本地文件系统 / S3 兼容对象存储
- **HTTP**: 标准库 `net/http`
- **客户端**: CLI 命令行工具（标准库 `flag` + `net/http`）

## 许可

MIT
