# 阿里云 OSS 对象存储配置

> 本文档记录 FileSync 项目接入阿里云 OSS 对象存储的完整配置说明。
> 创建日期：2026-07-08
> 配置方式：S3 兼容协议（项目使用 minio-go 客户端）

## 一、Bucket 信息

| 属性 | 值 |
|------|-----|
| Bucket 名称 | `aistudy-filesync` |
| 地域 | 华南1（深圳） |
| Endpoint | `oss-cn-shenzhen.aliyuncs.com` |
| 内网 Endpoint | `oss-cn-shenzhen-internal.aliyuncs.com`（仅同地域 ECS 可用，免流量费） |
| 存储类型 | 标准存储 |
| 存储冗余 | 本地冗余存储（ZRS） |
| 读写权限 | 私有（推荐，后端签名访问） |
| 阻止公共访问 | 已开启 |
| 版本控制 | 关闭 |
| 服务端加密 | 无 |

> Bucket 名称、地域、冗余类型创建后不可修改。

## 二、AccessKey 信息

**强烈建议使用 RAM 子账号 AccessKey，不要使用主账号 AccessKey。**

| 属性 | 值 |
|------|-----|
| RAM 用户登录名 | `aistudy-filesync@1947356891717358.onaliyun.com` |
| 显示名称 | `aistudy-filesync-app` |
| 主体 ID | `205695583447984598` |
| AccessKey ID | `***REMOVED_AK_ID***` |
| AccessKey Secret | 见部署配置文件 `.env` / `filesync.service`（不在此明文记录） |
| 权限策略 | `AliyunOSSFullAccess`（系统策略，管理 OSS 全部权限） |
| 资源范围 | 账号级别 |

> AccessKey Secret 仅在创建时显示一次，丢失后只能重置。请妥善保管。

## 三、项目集成配置

### 3.1 环境变量

项目通过环境变量启用 OSS，配置后新上传文件会写入 OSS（混合存储模式：旧文件保留本地）。

| 环境变量 | 值 | 说明 |
|----------|-----|------|
| `S3_ENDPOINT` | `oss-cn-shenzhen.aliyuncs.com` | OSS 访问域名（同地域 ECS 可改用 `-internal` 后缀） |
| `S3_REGION` | `oss-cn-shenzhen` | 地域 ID |
| `S3_BUCKET` | `aistudy-filesync` | Bucket 名称 |
| `S3_ACCESS_KEY` | `***REMOVED_AK_ID***` | RAM 用户 AccessKey ID |
| `S3_SECRET_KEY` | `<secret>` | RAM 用户 AccessKey Secret |
| `S3_USE_SSL` | `true` | 是否启用 HTTPS |

### 3.2 配置位置

| 用途 | 文件 |
|------|------|
| 本地开发 | `filesync/.env`（已加入 .gitignore，不入库） |
| 生产部署 | `filesync/deploy/mysql/filesync.service`（systemd Environment） |

### 3.3 启用/禁用

- **启用 OSS**：设置 `S3_ENDPOINT` 环境变量为非空值
- **禁用 OSS**：清空 `S3_ENDPOINT` 环境变量（仅用本地存储）

启用后启动日志会输出：`Storage: s3(oss) -> bucket=aistudy-filesync endpoint=oss-cn-shenzhen.aliyuncs.com`

## 四、存储路径设计

### 4.1 对象键命名

```
<fileID前2字符>/<fileID第3-4字符>/<fileID>.<ext>
例：fileID="abcdef1234567890", filename="report.pdf" → "ab/cd/abcdef1234567890.pdf"
```

- 与本地存储命名规则一致，便于混合存储路由
- 两级分片避免单目录文件过多（每目录最多 65536 个子目录）
- `fileID` 为 UUID，与 `filename` 解耦（重命名不影响物理路径）

### 4.2 分片临时对象

上传分片临时存放于 `_chunks/<sessionID>/chunk_00000x`，组装完成后自动清理。

### 4.3 数据库 storage_path 前缀

数据库 `files.storage_path` 列携带后端前缀自描述：
- `local:/abs/path` → 本地存储
- `s3:object/key` → OSS 对象键
- 无前缀 → 历史数据，视为本地存储

读取链路通过前缀路由到对应后端，写入链路通过 `storageType` 选择后端。

## 五、安全注意事项

1. **AccessKey 保管**：禁止提交到 git 仓库、禁止硬编码在源码中、禁止通过日志输出
2. **权限最小化**：仅授予 `AliyunOSSFullAccess`，不要授予 `AdministratorAccess`
3. **Bucket 私有**：保持 Bucket 读写权限为私有，前端通过后端签名 URL 访问
4. **阻止公共访问**：保持开启，防止误配置导致数据泄露
5. **STS 临时凭证**：高安全场景可改用 STS Token 方案（无需长期 AccessKey）
6. **密钥轮换**：建议每 90 天轮换一次 AccessKey

## 六、相关代码

| 文件 | 说明 |
|------|------|
| `internal/storage/s3.go` | S3/OSS 存储后端实现（minio-go 客户端） |
| `internal/storage/storage.go` | 存储接口定义 + ShardPath 分片命名 |
| `internal/storage/router.go` | 读写路由器（按 storage_path 前缀路由到 local/s3） |
| `cmd/server/main.go` | 启动时读取环境变量初始化 S3 后端（第 76-92 行） |

## 七、创建过程

1. 阿里云控制台 → 对象存储 OSS → Bucket 列表 → 创建 Bucket
2. 填写：名称 `aistudy-filesync`、地域 `华南1（深圳）`、存储类型 `标准存储`、冗余 `本地冗余`、读写 `私有`
3. 阻止公共访问默认开启（推荐保留）
4. RAM 访问控制 → 用户 → 创建用户 `aistudy-filesync`，勾选「使用永久 AccessKey 访问」
5. 保存 AccessKey ID 和 Secret（CSV 下载，仅显示一次）
6. RAM 访问控制 → 权限管理 → 授权 → 选择主体 `aistudy-filesync` + 策略 `AliyunOSSFullAccess` → 确认新增授权
