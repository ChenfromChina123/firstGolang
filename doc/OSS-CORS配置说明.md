# OSS CORS 配置说明

## 背景

Presigned URL 直连 OSS 功能要求浏览器直接 PUT/GET OSS，这会触发跨域请求。
若 OSS Bucket 未配置 CORS 规则，浏览器会拦截请求导致上传失败。

## 配置内容

| 配置项 | 值 | 说明 |
|--------|-----|------|
| AllowedOrigin | `https://aistudy.icu` | 生产环境域名 |
| AllowedOrigin | `http://localhost:8080` | 本地开发环境 |
| AllowedMethod | `PUT` | presigned 上传（小文件直传 + 大文件分片） |
| AllowedMethod | `GET` | presigned 下载（302 重定向） |
| AllowedMethod | `HEAD` | 预检请求 |
| AllowedHeader | `*` | 允许所有请求头（Content-Type 等） |
| ExposeHeader | `ETag` | 暴露 ETag 给客户端（用于分片校验） |
| MaxAgeSeconds | `3600` | 预检请求缓存 1 小时，减少 OPTIONS 请求 |

## 配置方式（三选一）

### 方式一：阿里云控制台（推荐）

1. 登录 [阿里云 OSS 控制台](https://oss.console.aliyun.com/)
2. 选择 Bucket `aistudy-filesync`
3. 左侧菜单 → **权限管理** → **跨域设置**
4. 点击 **创建规则**
5. 按上表填写，保存

### 方式二：ossutil 命令行

```bash
# 安装 ossutil 后（参考 https://help.aliyun.com/document_detail/120075.html）
# 使用本目录下的 oss-cors-config.xml 配置文件

# 设置 CORS
ossutil64 cors --method put oss://aistudy-filesync ./doc/oss-cors-config.xml

# 查看 CORS 配置
ossutil64 cors --method get oss://aistudy-filesync

# 删除 CORS 配置（如需重置）
ossutil64 cors --method delete oss://aistudy-filesync
```

### 方式三：REST API

```bash
# 使用 curl 调用 OSS PutBucketCors API
# 需要计算签名，建议用 ossutil 或 SDK
curl -X PUT \
  "https://aistudy-filesync.oss-cn-shenzhen.aliyuncs.com/?cors" \
  -H "Content-Type: application/xml" \
  -H "Authorization: OSS <AccessKeyId>:<Signature>" \
  -H "x-oss-date: <GMT_TIME>" \
  --data-binary @doc/oss-cors-config.xml
```

## 验证

配置完成后，在浏览器开发者工具的 Network 面板中观察：
- 上传时应有 `PUT` 请求到 `*.oss-cn-shenzhen.aliyuncs.com`，状态 200
- 不应有 `OPTIONS` 预检请求失败（403 Forbidden）

## 注意事项

1. **生产环境必须用 HTTPS origin**：`https://aistudy.icu`（不能用通配符 `*`，安全考虑）
2. **本地开发用 HTTP origin**：`http://localhost:8080`
3. **MaxAgeSeconds=3600**：预检结果缓存 1 小时，减少 OPTIONS 请求
4. **修改 CORS 后立即生效**：无需重启 OSS 服务
5. **如有多个域名**：每个 origin 单独一行（不能用逗号分隔）
