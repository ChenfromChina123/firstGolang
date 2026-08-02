# FileSync API 文档

> **版本**: 1.0.0  
> **协议**: HTTP/1.1  
> **数据格式**: JSON (请求/响应) + multipart/form-data (分片上传)  
> **基础 URL**: `http://<host>:<port>` 或 `https://<domain>`  
> **最后更新**: 2026-07-06

---

## 目录

- [1. 概述](#1-概述)
- [2. 认证](#2-认证)
- [3. 公共端点](#3-公共端点)
  - [3.1 健康检查](#31-健康检查)
  - [3.2 登录](#32-登录)
  - [3.3 登出](#33-登出)
- [4. 认证端点](#4-认证端点)
  - [4.1 当前用户信息](#41-当前用户信息)
- [5. 上传 API](#5-上传-api)
  - [5.1 初始化上传](#51-初始化上传)
  - [5.2 上传分片](#52-上传分片)
  - [5.3 查询上传进度](#53-查询上传进度)
  - [5.4 完成上传](#54-完成上传)
- [6. 下载 API](#6-下载-api)
  - [6.1 下载文件](#61-下载文件)
  - [6.2 ZIP 打包下载目录](#62-zip-打包下载目录)
- [7. 文件管理 API](#7-文件管理-api)
  - [7.1 文件列表](#71-文件列表)
  - [7.2 文件详情](#72-文件详情)
  - [7.3 新建目录](#73-新建目录)
  - [7.4 重命名/移动文件](#74-重命名移动文件)
  - [7.5 删除文件](#75-删除文件)
  - [7.6 删除目录](#76-删除目录)
- [8. 数据模型](#8-数据模型)
- [9. 错误码](#9-错误码)
- [10. 环境变量配置](#10-环境变量配置)

---

## 1. 概述

FileSync 是一个支持**分片上传**和**断点续传**的文件同步服务。所有业务端点（除登录/登出/健康检查外）均需携带 JWT 认证 Cookie。

### 文件名路径规则（路径枚举方案）

| 规则 | 说明 | 示例 |
|------|------|------|
| `/` 作为虚拟目录分隔符 | 用 `/` 分隔目录和文件 | `docs/report.pdf` |
| 禁止以 `/` 开头 | 防止路径穿越 | ❌ `/etc/passwd` |
| 禁止 `..` 段 | 防止目录遍历攻击 | ❌ `../../secret.txt` |
| 禁止反斜杠 `\` | Windows 兼容 | ❌ `docs\file.txt` |
| 禁止连续 `//` | 路径规范化 | ❌ `docs//file.txt` |
| 长度限制 | 1~1024 字节 | - |

---

## 2. 认证

### 认证方式：JWT HttpOnly Cookie

登录成功后，服务端设置名为 `filesync_token` 的 HttpOnly Cookie。后续所有受保护端点通过此 Cookie 自动认证。

| Cookie 属性 | 值 |
|-------------|-----|
| Name | `filesync_token` |
| HttpOnly | `true` |
| Secure | 仅 HTTPS 时启用 |
| SameSite | `Strict` |
| MaxAge | 24 小时 |

### 登录速率限制

- `/api/login` 端点速率限制为 **5 次/分钟/IP**
- 超出限制返回 HTTP `429 Too Many Requests`
- 响应头包含 `Retry-After: 60`

### 初始管理员

首次启动（用户表为空）时自动创建初始管理员：

| 环境变量 | 默认值 |
|----------|--------|
| `FILESYNC_INITIAL_USERNAME` | `admin` |
| `FILESYNC_INITIAL_PASSWORD` | `changeme123` |

---

## 3. 公共端点

### 3.1 健康检查

检查服务运行状态及 Redis 健康状态。

```
GET /api/health
```

#### 请求示例

```
GET http://localhost:8080/api/health
```

#### 响应（200 OK）

```json
{
  "healthy": true,
  "fail_count": 0,
  "max_retries": 3,
  "ping_interval": "5s",
  "service": "filesync"
}
```

Redis 未启用时：

```json
{
  "status": "ok",
  "service": "filesync",
  "redis": "disabled"
}
```

---

### 3.2 登录

用户登录，成功后设置 JWT HttpOnly Cookie。

```
POST /api/login
```

#### 速率限制

- **5 次/分钟/IP**（由 `LoginRateLimiter` 中间件控制）
- 超出限制返回 HTTP `429`

#### 请求头

| 头 | 值 |
|----|-----|
| Content-Type | `application/json` |

#### 请求体

```json
{
  "username": "admin",
  "password": "changeme123"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `username` | string | 是 | 用户名 |
| `password` | string | 是 | 密码 |

#### 响应（200 OK）

```json
{
  "success": true,
  "username": "admin",
  "role": "admin"
}
```

同时设置 Cookie: `filesync_token=<JWT>; Path=/; HttpOnly; SameSite=Strict`

#### 响应（400 Bad Request）

```json
{
  "error": "invalid_request",
  "message": "username and password required"
}
```

#### 响应（401 Unauthorized）

```json
{
  "error": "invalid_credentials",
  "message": "invalid username or password"
}
```

#### 响应（429 Too Many Requests）

```json
{
  "error": "rate_limited",
  "message": "too many login attempts, try again later"
}
```

---

### 3.3 登出

清除 JWT Cookie，结束当前会话。

```
POST /api/logout
```

#### 请求示例

```
POST http://localhost:8080/api/logout
Cookie: filesync_token=<JWT>
```

#### 响应（200 OK）

```json
{
  "success": true
}
```

---

## 4. 认证端点

### 4.1 当前用户信息

获取当前登录用户的信息。

```
GET /api/me
```

#### 请求示例

```
GET http://localhost:8080/api/me
Cookie: filesync_token=<JWT>
```

#### 响应（200 OK）

```json
{
  "user_id": "a1b2c3d4e5f6...",
  "username": "admin",
  "role": "admin"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `user_id` | string | 用户唯一标识 |
| `username` | string | 用户名 |
| `role` | string | 角色: `admin` / `user` |

#### 响应（401 Unauthorized）

```json
{
  "error": "unauthorized",
  "message": "login required"
}
```

---

## 5. 上传 API

### 5.1 初始化上传

创建一个新的分片上传会话。如果文件名已存在，返回冲突信息。

```
POST /api/upload/init
```

#### 请求体

```json
{
  "filename": "photo.jpg",
  "file_size": 10485760,
  "chunk_size": 524288,
  "file_hash": "sha256hex...",
  "storage": "local"
}
```

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `filename` | string | 是 | - | 文件名（支持 `/` 虚拟目录） |
| `file_size` | integer (int64) | 是 | - | 文件总大小（字节） |
| `chunk_size` | integer (int64) | 否 | `524288` (512KB) | 每个分片的大小（字节） |
| `file_hash` | string | 否 | - | 客户端预计算的 SHA256（用于断点续传匹配） |
| `storage` | string | 否 | `"local"` | 存储后端: `local` / `s3` |

#### 查询参数

| 参数 | 值 | 说明 |
|------|-----|------|
| `force` | `true` | 覆盖已存在的文件（跳过冲突检查） |
| `rename` | `true` | 自动重命名新文件避免冲突 |

#### 响应（201 Created / 200 OK）

```json
{
  "session_id": "a1b2c3d4e5f678901234567890abcdef",
  "filename": "photo.jpg",
  "chunk_size": 524288,
  "total_chunks": 20,
  "storage_type": "local"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `session_id` | string | 上传会话 ID（32 字符 hex） |
| `filename` | string | 文件名 |
| `chunk_size` | integer | 分片大小（字节） |
| `total_chunks` | integer | 总分片数 |
| `storage_type` | string | 存储类型: `local` / `s3` |

#### 响应（409 Conflict）- 文件名冲突

```json
{
  "conflict": true,
  "message": "file 'photo.jpg' already exists",
  "strategies": ["skip", "overwrite", "rename"],
  "existing": {
    "id": "file-abc-123",
    "filename": "photo.jpg",
    "size": 10485760,
    "hash": "sha256hex...",
    "storage_path": "ab/cd/abcdef...photo.jpg",
    "storage_type": "local",
    "created_at": "2026-07-05T10:30:00Z"
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `conflict` | boolean | 始终为 `true` |
| `message` | string | 冲突描述 |
| `strategies` | string[] | 可选策略: `skip` / `overwrite` / `rename` |
| `existing` | object | 已有文件信息（可选，有完整记录时返回） |

#### 响应（429 Too Many Requests）

```json
{
  "error": "too many concurrent init uploads"
}
```

Redis 模式下，当并发 InitUpload 超过 1000 时触发限流保护。

#### 响应（400 Bad Request）

文件名校验失败时：

```json
{
  "error": "filename contains '..' path segment"
}
```

---

### 5.2 上传分片

上传单个文件分片。

```
POST /api/upload/chunk
```

#### 请求格式: `multipart/form-data`

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `session_id` | string | 是 | 上传会话 ID |
| `chunk_index` | int | 是 | 分片序号（从 0 开始） |
| `chunk_data` | file (binary) | 是 | 分片二进制数据 |

#### 请求示例

```
POST /api/upload/chunk
Content-Type: multipart/form-data; boundary=----WebKitFormBoundary

------WebKitFormBoundary
Content-Disposition: form-data; name="session_id"

a1b2c3d4e5f678901234567890abcdef
------WebKitFormBoundary
Content-Disposition: form-data; name="chunk_index"

0
------WebKitFormBoundary
Content-Disposition: form-data; name="chunk_data"; filename="chunk.bin"
Content-Type: application/octet-stream

<binary data>
------WebKitFormBoundary--
```

#### 响应（200 OK）

```json
{
  "session_id": "a1b2c3d4e5f678901234567890abcdef",
  "chunk_index": 0,
  "received": true
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `session_id` | string | 上传会话 ID |
| `chunk_index` | int | 已接收的分片序号 |
| `received` | boolean | 始终为 `true` |

> **注意**: 如果分片已上传过（重复上传），服务端同样返回 `received: true`（幂等设计）。

#### 响应（400 Bad Request）

```json
{
  "error": "session_id and chunk_index are required"
}
```

#### 响应（404 Not Found）

```json
{
  "error": "session not found"
}
```

#### 响应（409 Conflict）

```json
{
  "error": "session is not active"
}
```

会话已关闭（已完成/已取消）时返回。

---

### 5.3 查询上传进度

查询指定上传会话的进度，获取已接收和缺失的分片列表。

```
GET /api/upload/status?session_id=<session_id>
```

#### 查询参数

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `session_id` | string | 是 | 上传会话 ID |

#### 请求示例

```
GET http://localhost:8080/api/upload/status?session_id=a1b2c3d4e5f678901234567890abcdef
```

#### 响应（200 OK）

```json
{
  "session_id": "a1b2c3d4e5f678901234567890abcdef",
  "filename": "photo.jpg",
  "file_size": 10485760,
  "chunk_size": 524288,
  "total_chunks": 20,
  "received_chunks": [0, 1, 2, 3],
  "missing_chunks": [4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19],
  "progress": "20.0%",
  "status": "active"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `session_id` | string | 上传会话 ID |
| `filename` | string | 文件名 |
| `file_size` | integer | 文件总大小 |
| `chunk_size` | integer | 分片大小 |
| `total_chunks` | integer | 总分片数 |
| `received_chunks` | int[] | 已接收的分片序号列表 |
| `missing_chunks` | int[] | 缺失的分片序号列表（用于断点续传） |
| `progress` | string | 进度百分比字符串，如 `"20.0%"` |
| `status` | string | 会话状态: `active` / `completed` / `cancelled` |

#### 响应（400 Bad Request）

```json
{
  "error": "session_id required"
}
```

#### 响应（404 Not Found）

```json
{
  "error": "session not found"
}
```

---

### 5.4 完成上传

将所有已上传的分片合并为完整文件。完成后会清理临时分片数据。

```
POST /api/upload/complete
```

#### 请求体

```json
{
  "session_id": "a1b2c3d4e5f678901234567890abcdef"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `session_id` | string | 是 | 上传会话 ID |

#### 响应（200 OK）

```json
{
  "file_id": "file-abc-def-456-789",
  "filename": "photo.jpg",
  "size": 10485760,
  "hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "storage_path": "ab/cd/abcdef1234567890.jpg"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `file_id` | string | 文件唯一 ID（32 字符 hex） |
| `filename` | string | 文件名 |
| `size` | integer | 文件大小（字节） |
| `hash` | string | SHA256 校验值（hex） |
| `storage_path` | string | 存储路径（两级分片: `前2字符/第3-4字符/fileID.ext`） |

#### 响应（400 Bad Request）

```json
{
  "error": "not all chunks received: 15/20"
}
```

#### 响应（404 Not Found）

```json
{
  "error": "session not found"
}
```

#### 响应（409 Conflict）

```json
{
  "error": "assembly already in progress"
}
```

Redis 模式下，同一 session 的并发 CompleteUpload 会被分布式锁拒绝。

---

## 6. 下载 API

### 6.1 下载文件

下载已完成文件，支持 HTTP Range 断点续传。

```
GET /api/download/{fileID}
```

#### 路径参数

| 参数 | 类型 | 说明 |
|------|------|------|
| `fileID` | string | 文件 ID |

#### 请求头

| 头 | 值 | 说明 |
|----|-----|------|
| `Range` | `bytes=<start>-<end>` | （可选）范围请求，用于断点续传 |

#### 响应头（200 OK / 206 Partial Content）

| 头 | 说明 |
|----|------|
| `Content-Disposition` | `attachment; filename="<原始文件名>"` |
| `Content-Type` | `application/octet-stream` |
| `Accept-Ranges` | `bytes` |
| `Content-Length` | 响应体大小 |
| `Content-Range` | （仅 206 时）范围描述: `bytes <start>-<end>/<total>` |

#### 请求示例

完整下载：

```
GET http://localhost:8080/api/download/abc123def456
```

断点续传（已下载 1024 字节，从中断处继续）：

```
GET http://localhost:8080/api/download/abc123def456
Range: bytes=1024-
```

指定范围：

```
GET http://localhost:8080/api/download/abc123def456
Range: bytes=0-1023
```

#### 响应（400 Bad Request）

```json
{
  "error": "file_id required"
}
```

#### 响应（404 Not Found）

```json
{
  "error": "file not found"
}
```

#### 响应（416 Range Not Satisfiable）

```json
{
  "error": "range not satisfiable"
}
```

---

### 6.2 ZIP 打包下载目录

将指定虚拟目录下的所有文件递归打包为 ZIP 流式下载。

```
GET /api/download/dir?prefix=<prefix>
```

#### 查询参数

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `prefix` | string | 是 | 虚拟目录路径前缀，如 `docs/` |

#### 响应

| 属性 | 值 |
|------|-----|
| Content-Type | `application/zip` |
| Content-Disposition | `attachment; filename="<目录名>.zip"` |

#### 请求示例

```
GET http://localhost:8080/api/download/dir?prefix=docs/
```

ZIP 内文件路径为相对路径（去掉 `prefix` 前缀），保留虚拟目录结构。自动过滤 `.keep` 占位文件。

#### 响应（400 Bad Request）

```json
{
  "error": "prefix required"
}
```

#### 响应（404 Not Found）

```json
{
  "error": "directory is empty or not found"
}
```

---

## 7. 文件管理 API

### 7.1 文件列表

列出所有已完成文件，支持按虚拟目录前缀过滤。

```
GET /api/files
GET /api/files?prefix=<prefix>
```

#### 查询参数

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `prefix` | string | 否 | 虚拟目录路径前缀（递归匹配子目录） |

#### 请求示例

```
# 列出所有文件
GET http://localhost:8080/api/files

# 列出 docs/ 目录下的所有文件（递归）
GET http://localhost:8080/api/files?prefix=docs/
```

#### 响应（200 OK）

```json
[
  {
    "id": "abc123def456...",
    "filename": "docs/report.pdf",
    "size": 2048000,
    "hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "storage_path": "ab/cd/abc123def456....pdf",
    "storage_type": "local",
    "created_at": "2026-07-05T10:30:00Z"
  }
]
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | string | 文件唯一 ID |
| `filename` | string | 文件名（含虚拟目录路径） |
| `size` | integer | 文件大小（字节） |
| `hash` | string | SHA256 校验值 |
| `storage_path` | string | 存储路径（两级分片） |
| `storage_type` | string | 存储类型: `local` / `s3` |
| `created_at` | string | 创建时间 ISO 8601 |

---

### 7.2 文件详情

获取单个文件的详细信息。

```
GET /api/files/{fileID}
```

#### 路径参数

| 参数 | 类型 | 说明 |
|------|------|------|
| `fileID` | string | 文件 ID |

#### 请求示例

```
GET http://localhost:8080/api/files/abc123def456
```

#### 响应（200 OK）

```json
{
  "id": "abc123def456...",
  "filename": "photo.jpg",
  "size": 10485760,
  "hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "storage_path": "ab/cd/abc123def456....jpg",
  "storage_type": "local",
  "created_at": "2026-07-05T10:30:00Z"
}
```

#### 响应（404 Not Found）

```json
{
  "error": "file not found"
}
```

---

### 7.3 新建目录

创建一个虚拟目录（通过创建 `.keep` 占位文件实现）。

```
POST /api/files/mkdir
```

#### 请求体

```json
{
  "path": "docs/sub/"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `path` | string | 是 | 目录路径（末尾自动补 `/`） |

#### 响应（201 Created）

```json
{
  "created": true,
  "path": "docs/sub/"
}
```

#### 响应（400 Bad Request）

```json
{
  "error": "path required"
}
```

文件名校验失败时返回具体错误。

#### 响应（409 Conflict）

```json
{
  "error": "directory already exists"
}
```

---

### 7.4 重命名/移动文件

重命名文件或移动到其他虚拟目录（仅修改 filename 字段，不移动物理存储）。

```
POST /api/files/rename
```

#### 请求体

```json
{
  "id": "abc123def456...",
  "new_filename": "docs/renamed_report.pdf"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `id` | string | 是 | 文件 ID |
| `new_filename` | string | 是 | 新文件名（含虚拟目录路径） |

#### 响应（200 OK）

```json
{
  "renamed": true,
  "id": "abc123def456...",
  "old_filename": "report.pdf",
  "new_filename": "docs/renamed_report.pdf"
}
```

#### 响应（400 Bad Request）

```json
{
  "error": "id and new_filename required"
}
```

#### 响应（404 Not Found）

```json
{
  "error": "file not found"
}
```

#### 响应（409 Conflict）

```json
{
  "error": "file 'docs/renamed_report.pdf' already exists"
}
```

---

### 7.5 删除文件

删除单个文件（包含存储文件和数据库记录）。

```
DELETE /api/files/{fileID}
```

#### 路径参数

| 参数 | 类型 | 说明 |
|------|------|------|
| `fileID` | string | 文件 ID |

#### 请求示例

```
DELETE http://localhost:8080/api/files/abc123def456
```

#### 响应（200 OK）

```json
{
  "deleted": true,
  "id": "abc123def456...",
  "filename": "photo.jpg"
}
```

> 存储文件删除后，空的父目录会被递归清理。

#### 响应（400 Bad Request）

```json
{
  "error": "file_id required"
}
```

#### 响应（404 Not Found）

```json
{
  "error": "file not found"
}
```

---

### 7.6 删除目录

递归删除指定前缀下的所有文件。

```
DELETE /api/files?prefix=<prefix>
```

#### 查询参数

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `prefix` | string | 是 | 虚拟目录路径前缀 |

#### 请求示例

```
DELETE http://localhost:8080/api/files?prefix=docs/
```

#### 响应（200 OK）

```json
{
  "deleted": true,
  "prefix": "docs/",
  "files_deleted": 5,
  "storage_errors": 0
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `deleted` | boolean | 操作结果 |
| `prefix` | string | 删除的前缀 |
| `files_deleted` | integer | 删除的文件数 |
| `storage_errors` | integer | 存储删除失败数（非阻塞） |

#### 响应（400 Bad Request）

```json
{
  "error": "prefix required"
}
```

---

## 8. 数据模型

### 8.1 UploadSession（上传会话）

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | string | 会话 ID（32 字符 hex） |
| `filename` | string | 文件名 |
| `file_size` | int64 | 文件总大小 |
| `file_hash` | string | 客户端预计算 SHA256（可选） |
| `chunk_size` | int64 | 分片大小 |
| `total_chunks` | int | 总分片数 |
| `received_chunks` | int[] | 已接收分片序号 |
| `status` | string | 状态: `active` / `completed` / `cancelled` |
| `storage_type` | string | 存储后端: `local` / `s3` |

### 8.2 FileRecord（文件记录）

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | string | 文件 ID（32 字符 hex） |
| `filename` | string | 文件名（含虚拟目录路径） |
| `size` | int64 | 文件大小 |
| `hash` | string | SHA256 校验值 |
| `storage_path` | string | 存储路径（两级分片） |
| `storage_type` | string | 存储后端: `local` / `s3` |
| `chunk_size` | int64 | 上传时的分片大小 |
| `total_chunks` | int | 总分片数 |
| `status` | string | 状态: `completed` / `failed` |

### 8.3 User（用户）

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | string | 用户 ID（32 字符 hex） |
| `username` | string | 用户名（唯一） |
| `role` | string | 角色: `admin` / `user` |
| `created_at` | time.Time | 创建时间 |

### 8.4 通用错误响应

```json
{
  "error": "<error_code>",
  "message": "<human_readable_message>"
}
```

---

## 9. 错误码

| HTTP 状态码 | 说明 | 常见场景 |
|-------------|------|----------|
| `200 OK` | 请求成功 | - |
| `201 Created` | 资源创建成功 | 初始化上传、新建目录 |
| `206 Partial Content` | 部分内容（范围请求） | 下载断点续传 |
| `400 Bad Request` | 请求参数错误 | 缺少必填字段、文件名校验失败 |
| `401 Unauthorized` | 未认证/认证失败 | 未登录、Token 过期 |
| `404 Not Found` | 资源不存在 | Session 未找到、文件未找到 |
| `409 Conflict` | 资源冲突 | 文件名冲突、会话已关闭、目录已存在 |
| `416 Range Not Satisfiable` | 范围请求无法满足 | Range 越界 |
| `429 Too Many Requests` | 请求过多 | 登录频率限制、InitUpload 并发限流 |
| `500 Internal Server Error` | 服务器内部错误 | 文件写入失败、数据库错误 |

---

## 10. 环境变量配置

| 环境变量 | 说明 | 默认值 |
|----------|------|--------|
| `PORT` | 服务监听端口 | `8080` |
| `DATA_DIR` | 数据存储目录 | `./data` |
| `STORAGE_TYPE` | 存储后端: `local` / `s3` | `local` |
| `S3_ENDPOINT` | S3 端点地址 | `localhost:9000` |
| `S3_REGION` | S3 区域 | `us-east-1` |
| `S3_BUCKET` | S3 Bucket | `filesync` |
| `S3_ACCESS_KEY` | S3 访问密钥 | - |
| `S3_SECRET_KEY` | S3 密钥 | - |
| `S3_USE_SSL` | S3 是否启用 SSL | `false` |
| `REDIS_ADDR` | 单机 Redis 地址 | - |
| `REDIS_PASSWORD` | Redis 密码 | - |
| `REDIS_DB` | Redis 数据库编号 | `0` |
| `REDIS_SENTINEL_ADDRS` | Sentinel 地址列表（逗号分隔） | - |
| `REDIS_SENTINEL_MASTER` | Sentinel 主节点名称 | `mymaster` |
| `JWT_SECRET` | JWT 签名密钥（至少 32 字节 hex） | 随机生成（重启失效） |
| `FILESYNC_INITIAL_USERNAME` | 初始管理员用户名 | `admin` |
| `FILESYNC_INITIAL_PASSWORD` | 初始管理员密码 | `changeme123` |
| `DOMAIN` | 域名（设置后启用 HTTPS + autocert） | - |
| `WEB_DIR` | Web 控制台静态文件目录 | `./web` |

---

## API 总览速查表

| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| `GET` | `/api/health` | 否 | 健康检查 |
| `POST` | `/api/login` | 否 | 登录（5次/分限流） |
| `POST` | `/api/logout` | 否 | 登出 |
| `GET` | `/api/me` | 是 | 当前用户信息 |
| `POST` | `/api/upload/init` | 是 | 初始化分片上传 |
| `POST` | `/api/upload/chunk` | 是 | 上传分片（multipart） |
| `GET` | `/api/upload/status` | 是 | 查询上传进度 |
| `POST` | `/api/upload/complete` | 是 | 完成上传（合并分片） |
| `GET` | `/api/download/{fileID}` | 是 | 下载文件（Range 支持） |
| `GET` | `/api/download/dir` | 是 | ZIP 打包下载目录 |
| `GET` | `/api/files` | 是 | 文件列表（prefix 过滤） |
| `GET` | `/api/files/{fileID}` | 是 | 文件详情 |
| `POST` | `/api/files/mkdir` | 是 | 新建虚拟目录 |
| `POST` | `/api/files/rename` | 是 | 重命名/移动文件 |
| `DELETE` | `/api/files/{fileID}` | 是 | 删除文件 |
| `DELETE` | `/api/files` | 是 | 删除目录（递归） |
