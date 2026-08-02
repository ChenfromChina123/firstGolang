# aistudy\.icu 安全评估报告

> 部分内容由豆包生成
> 
> 

# aistudy\.icu 安全评估报告

> **评估日期**: 2026\-07\-07
> **评估目标**: [https://aistudy\.icu](https://aistudy.icu) \(FileSync 文件同步服务\)
> **评估类型**: 非侵入式黑盒安全测试
> **风险等级**: 中低风险 \(总体安全状况良好，存在一些可改进的安全配置项\)
> 
> 

---

## 一、目标信息概览

|项目|详情|
|---|---|
|**域名**|aistudy\.icu|
|**IP 地址**|8\.138\.174\.80 \(阿里云\)|
|**开放端口**|22 \(SSH\), 80 \(HTTP\), 443 \(HTTPS\)|
|**Web 服务**|FileSync v1\.0\.0 \(文件同步服务\)|
|**TLS 版本**|TLS 1\.3|
|**证书颁发者**|Let's Encrypt|
|**SSH 版本**|OpenSSH 8\.0|

---

## 二、安全评分

|类别|评分|说明|
|---|---|---|
|**传输安全 \(TLS\)**|9/10|TLS 1\.3 \+ 强加密套件，缺少 HSTS|
|**认证安全**|7/10|JWT \+ HttpOnly Cookie，速率限制可能未生效|
|**输入验证**|8/10|路径校验较严格，路径规范化有小问题|
|**安全头配置**|6/10|有基本安全头，缺少多个重要安全头|
|**业务安全**|7/10|分享 ID 随机，下载 token 有签名，分享端无限速|
|**服务器配置**|7/10|未泄露服务器信息，HTTP 方法处理不严谨|
|**总体评分**|**7\.3/10**|整体安全状况良好，建议加固多个配置项|

---

## 三、详细安全发现

### 🔴 高风险问题

*暂无发现高风险漏洞*

---

### 🟡 中风险问题

#### 1\. 缺少 HSTS \(HTTP Strict Transport Security\) 头

**风险等级**: 中
**影响**: 用户可能受到 SSL 剥离攻击，被降级到 HTTP 连接

**现状**:

```
✗ Strict-Transport-Security: 缺失
```

**建议配置**:

```
Strict-Transport-Security: max-age=31536000; includeSubDomains
```

---

#### 2\. 分享端点缺少速率限制

**风险等级**: 中
**影响**: 攻击者可能暴力枚举分享 ID，或通过大量请求消耗服务器资源

**测试结果**:

- 连续 20 次请求 `/api/s/b83cf9b5` 全部返回 200

- 未触发任何限流或 429 响应

**建议**:

- 对公开分享端点添加速率限制（如 60次/分钟/IP）

- 考虑对分享 ID 添加访问频率限制

---

#### 3\. 登录速率限制可能未生效

**风险等级**: 中
**影响**: 攻击者可能对登录接口进行暴力破解

**测试结果**:

- 连续 10 次错误登录尝试，全部返回 401

- 未触发文档中描述的 "5次/分钟/IP" 限流（429 响应）

**可能原因**:

- Redis 未配置（限流依赖 Redis）

- 代理层导致真实 IP 识别问题

- 限流配置与文档描述不一致

**建议**:

- 确认 Redis 是否正常运行

- 检查 `X-Forwarded-For` 头的处理

- 验证限流功能是否正常工作

---

### 🟢 低风险问题

#### 4\. Content\-Security\-Policy 策略不完整

**风险等级**: 低
**现状**:

```
Content-Security-Policy: frame-ancestors 'self'
```

**问题**: 只配置了 `frame-ancestors`，缺少其他重要的 CSP 指令，无法有效防御 XSS 攻击。

**建议配置**:

```
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'self'
```

---

#### 5\. 缺少多个安全响应头

**风险等级**: 低
**缺失的安全头**:

|安全头|状态|建议值|
|---|---|---|
|`Strict-Transport-Security`|✗ 缺失|`max-age=31536000; includeSubDomains`|
|`Permissions-Policy`|✗ 缺失|`geolocation=(), microphone=(), camera=()`|
|`X-Permitted-Cross-Domain-Policies`|✗ 缺失|`none`|
|`X-XSS-Protection`|✗ 缺失|`1; mode=block` \(现代浏览器可省略\)|

---

#### 6\. 路径规范化问题

**风险等级**: 低
**发现**:

- `/web/../api/health` 返回 200（路径被规范化）

- 但其他变体如 `/web/./api/health` 返回 404

**影响**: 理论上可能绕过某些基于路径的访问控制，但目前未发现可利用的场景。

**建议**: 统一路径规范化逻辑，确保所有路径变体都被正确处理。

---

#### 7\. HTTP 方法未正确限制

**风险等级**: 低
**发现**: 对 `/api/health` 端点，所有 HTTP 方法（GET, POST, PUT, DELETE, PATCH, TRACE, CONNECT）都返回相同的 200 响应。

**影响**:

- 不符合 RESTful API 设计规范

- TRACE 方法虽然未发现 XST 漏洞，但建议禁用

**建议**:

- 按接口语义限制允许的 HTTP 方法

- 禁用 TRACE、CONNECT 等不必要的方法

- 对不允许的方法返回 405 Method Not Allowed

---

#### 8\. SSH 版本较旧

**风险等级**: 低
**发现**: `OpenSSH 8.0` \(发布于 2019 年\)

**建议**:

- 定期更新系统和 SSH 版本

- 禁用密码登录，仅使用密钥认证

- 考虑修改默认 SSH 端口或使用 fail2ban

---

#### 9\. 缺少 security\.txt 文件

**风险等级**: 低
**建议**: 在 `/.well-known/security.txt` 提供安全联系信息，方便安全研究者报告漏洞。

**示例内容**:

```
Contact: mailto:security@aistudy.icu
Expires: 2027-01-01T00:00:00.000Z
Preferred-Languages: zh-CN, en
```

---

## 四、安全亮点 ✅

### 1\. 传输安全良好

- 使用 TLS 1\.3 协议

- 强加密套件 `TLS_AES_128_GCM_SHA256`

- HTTP 自动重定向到 HTTPS

- Let's Encrypt 有效证书

### 2\. 认证机制设计合理

- JWT \+ HttpOnly Cookie 认证

- SameSite=Strict 防止 CSRF

- 默认凭证已修改（admin/changeme123 无法登录）

### 3\. 分享功能安全设计

- 分享 ID 随机生成（8 位 hex，不可预测）

- 下载 token 带签名验证（不可伪造）

- 每次请求生成新的下载 token

### 4\. 输入验证较严格

- 路径遍历防护（禁止 `..`、`/` 开头等）

- SQL 注入测试未发现漏洞

- 统一的错误响应格式，不泄露敏感信息

### 5\. 服务器信息隐藏

- 无 `Server` 响应头

- 无 `X-Powered-By` 等技术栈泄露头

### 6\. CORS 配置严格

- 无 `Access-Control-Allow-Origin` 头

- 防止跨域数据窃取

---

## 五、加固建议优先级

### 高优先级（建议尽快修复）

1. ✅ 添加 HSTS 响应头

2. ✅ 验证并确保登录速率限制正常工作

3. ✅ 对公开分享端点添加速率限制

### 中优先级（建议近期修复）

1. 完善 Content\-Security\-Policy 策略

2. 添加 Permissions\-Policy 等安全头

3. 修复 HTTP 方法处理逻辑

4. 统一路径规范化处理

### 低优先级（可择机优化）

1. 添加 security\.txt 文件

2. 更新 SSH 版本

3. 考虑添加 X\-XSS\-Protection 头（兼容旧浏览器）

---

## 六、测试方法说明

本次评估为非侵入式黑盒测试，包括：

- 端口扫描和服务识别

- 安全响应头分析

- SSL/TLS 配置检查

- 目录和文件枚举

- 认证机制测试

- 常见 Web 漏洞初步检测（SQL注入、路径遍历、XSS等）

- 业务逻辑安全测试（分享功能、下载token等）

- 速率限制测试

**注意**: 本次测试未进行深度渗透测试、暴力破解、DDoS 测试等可能影响服务可用性的测试。如需更全面的安全评估，建议进行专业的渗透测试。

---

## 七、总结

`aistudy.icu` \(FileSync 服务\) **整体安全状况良好**，在传输安全、认证设计、输入验证等方面都有不错的表现。主要的改进空间在于安全响应头的完整性、速率限制的确保、以及一些配置层面的优化。

按照建议进行加固后，安全状况可以进一步提升到优秀水平。

---

*报告生成时间: 2026\-07\-07*

> （注：部分内容可能由 AI 生成）
