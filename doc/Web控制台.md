# FileSync Web 控制台文档

## 概述

filesync 内置一个纯 HTML+CSS+JS 的 Web 控制台，用于文件上传、下载、管理。前端仅做页面展示，所有业务方法走 `/api/*` 后端（规则15）。

## 技术栈

- **HTML**：单页面结构，无框架
- **CSS**：终端美学深色主题，CSS 变量管理配色
- **JavaScript**：原生 ES6+，无依赖，无构建步骤
- **字体**：JetBrains Mono（等宽，数据展示）+ Outfit（无衬线，UI）

## 文件结构

```
filesync/web/
├── index.html   # 主页面结构（顶部状态栏 + 上传区 + 文件列表 + 冲突对话框 + Toast）
├── style.css    # 样式（深色主题，网格背景，扫描线动画）
└── app.js       # 前端逻辑（UploadTask 类、分片上传、断点续传、冲突处理）
```

总大小约 15KB，无外部依赖（仅 Google Fonts CDN）。

## 功能模块

### 1. 健康检查

- 顶部状态栏显示服务健康状态
- 每 10 秒轮询 `GET /api/health`
- 显示 `healthy` / `fail_count` / `ping_interval`

### 2. 分片上传

**流程**：
1. 用户拖拽/选择文件
2. 计算文件 hash（SHA-256 前 8MB，用于断点续传标识）
3. `POST /api/upload/init` 初始化 session（JSON body 传 filename/file_size/chunk_size/storage/file_hash）
4. `GET /api/upload/status?session_id=xxx` 查询已传分片（断点续传）
5. 并发上传缺失分片 `POST /api/upload/chunk`（FormData 传 session_id/chunk_index/chunk_data）
6. `POST /api/upload/complete` 合并分片

**配置项**：
- 分片大小：256KB / 512KB（默认）/ 1MB / 4MB
- 并发数：3 / 5（默认）/ 8 / 12

### 3. 断点续传

- 上传前查询 `GET /api/upload/status` 获取 `received_chunks`
- 跳过已上传的分片，只上传缺失部分
- 进度条从已传分片数开始计算

### 4. 冲突处理

- `POST /api/upload/init` 返回 409 时弹窗
- 三种策略：
  - **跳过**：取消本次上传
  - **覆盖**：重试时 URL 加 `?force=true`
  - **重命名**：重试时 URL 加 `?rename=true`
- 支持"应用到后续所有冲突"复选框

### 5. 文件列表

- `GET /api/files` 获取文件列表
- 表格展示：文件名、大小、分片数、上传时间、存储类型、下载按钮
- 上传完成后自动刷新

### 6. 文件下载

- 点击下载按钮，`<a href="/api/download/{id}" download>` 触发浏览器下载
- 支持大文件（后端支持 Range 请求）

## 后端集成

### 静态文件服务

在 `cmd/server/main.go` 中注册：

```go
webDir := getEnv("WEB_DIR", "./web")
if _, err := os.Stat(webDir); err == nil {
    mux.Handle("/web/", http.StripPrefix("/web/", http.FileServer(http.Dir(webDir))))
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/" {
            http.Redirect(w, r, "/web/index.html", http.StatusFound)
            return
        }
        http.NotFound(w, r)
    })
}
```

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `WEB_DIR` | `./web` | 前端静态文件目录 |

### 路由

| 路径 | 方法 | 说明 |
|------|------|------|
| `/` | GET | 重定向到 `/web/index.html` |
| `/web/` | GET | 静态文件服务（index.html） |
| `/web/style.css` | GET | 样式文件 |
| `/web/app.js` | GET | 前端逻辑 |

## 部署

### 服务器部署

1. 上传 `web/` 目录到 `/opt/filesync/web/`
2. 重启 filesync 服务：`systemctl restart filesync`
3. 访问 `http://<server>:8888/`

### 访问地址

- **本机**：`http://127.0.0.1:8888/web/`
- **公网**：`http://8.138.174.80:8888/web/`

## 设计要点

### 终端美学

- 深炭灰背景（#0a0e14）+ 电光青强调色（#00d9ff）
- 微弱网格背景纹理 + 顶部光晕
- 等宽字体用于数据展示（文件名、大小、时间）
- 进度条扫描线动画（CSS `@keyframes scan`）

### 性能

- 纯静态文件，无 SSR，无构建
- 3 个文件共约 15KB（gzip 后约 5KB）
- 无 JS 框架，无运行时依赖
- 适合 1.8GB 小规格服务器

### 规则合规

- **规则15**：前端仅做页面展示，所有业务方法在后端
- **规则8**：JS 函数添加函数级注释
- **规则2**：独立 `web/` 目录，无特殊依赖

## 测试

### API 端到端测试

使用 `web/test_api.sh` 脚本测试完整上传流程：

```bash
bash /tmp/test_api.sh
```

测试覆盖：
1. 健康检查
2. 初始化上传
3. 上传分片
4. 查询状态
5. 完成上传
6. 文件列表
7. 前端页面可访问性

### 已知限制

- 无法在无 Chrome 环境下进行浏览器 UI 自动化测试
- API 层已通过 curl 端到端测试验证
