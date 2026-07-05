# FileSync - 文件同步服务（断点续传）

一个支持**分片上传**和**断点续传**的文件同步服务，包含 HTTP 服务端和 CLI 客户端。

## 功能特性

- **分片上传**：大文件自动切分为 512KB 分片逐个上传
- **上传断点续传**：网络中断或程序中断后，自动恢复未完成的分片
- **下载断点续传**：支持 HTTP `Range` 请求头，中断后从断点继续下载
- **文件冲突检测**：同名文件上传时提示冲突，支持 `skip`（跳过）/ `overwrite`（覆盖）/ `rename`（重命名）
- **存储后端**：本地文件系统存储（默认）；S3 兼容对象存储（扩展）
- **文件完整性**：上传完成后计算 SHA256 校验

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

## API 文档

### 上传

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/upload/init` | 初始化上传会话 |
| `POST` | `/api/upload/chunk` | 上传单个分片（multipart） |
| `GET` | `/api/upload/status?session_id=xxx` | 查询上传进度 |
| `POST` | `/api/upload/complete` | 完成上传并合并文件 |

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
| `GET` | `/api/files` | 列出所有已完成文件 |
| `GET` | `/api/files/{fileID}` | 查询文件详情 |

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

> Redis 为可选项，未配置时自动降级为纯 SQLite 模式。配置 Sentinel 后优先使用 Sentinel 分布式模式。

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
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
```

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

## 技术栈

- **语言**: Go 1.23+
- **数据库**: SQLite (modernc.org/sqlite，纯 Go 实现，无需 CGO)
- **存储**: 本地文件系统 / S3 兼容对象存储
- **HTTP**: 标准库 `net/http`
- **客户端**: CLI 命令行工具（标准库 `flag` + `net/http`）

## 许可

MIT
