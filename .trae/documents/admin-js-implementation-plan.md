# admin.js 实施计划（云空间显示与管理员页面开发 - 续）

## Context

承接 `云空间显示与管理员页面开发计划.md`，本计划专门细化 **Task #62：创建 web/admin.js** 的实施细节。

当前状态：
- ✅ 后端全部完成：db.go + settings.go + admin.go + main.go（已编译通过）
- ✅ 前端 index.html + app.js 存储用量显示与管理员入口已完成
- ✅ web/admin.html 已创建（完整 HTML + 内联 CSS，4 Tab 结构）
- ⏳ **web/admin.js 尚未创建** — admin.html 第 206 行已引用 `<script src="/web/admin.js?v=20260710">`

## 后端 API 接口（已实现，admin.js 直接调用）

| 方法 | 路径 | 请求体 | 响应 |
|------|------|--------|------|
| GET | `/api/me` | - | `{user_id, username, role}` |
| GET | `/api/admin/stats` | - | `{total_users, active_users, disabled_users, total_files, total_size, trash_files, trash_size, total_shares}` |
| GET | `/api/admin/users` | - | `{users:[{id,username,email,role,status,created_at,used_size,file_count}], total}` |
| POST | `/api/admin/users/{id}/status` | `{status:"active"\|"disabled"}` | `{success, status}` |
| POST | `/api/admin/users/{id}/reset-password` | `{new_password:"xxx"}` | `{success}` |
| GET | `/api/admin/shares` | - | `{shares:[{id,share_type,file_id,dir_prefix,created_by,created_at,expires_at,download_count,is_active,is_expired,has_password}], total}` |
| DELETE | `/api/admin/shares/{id}` | - | `{success}` |
| GET | `/api/files` | `?prefix=&all=true` | 现有文件列表格式（admin 可看所有） |

**注意**：
- 所有 `/api/admin/*` 需认证 + admin 权限，由 JWT 中间件 + admin.go ServeHTTP 开头双重校验
- 非 admin 调用返回 403
- 时间字段为 Unix 时间戳（int64 秒）

## admin.html DOM 结构（admin.js 需操作）

```
#admin-user-info              - 顶部管理员用户名显示
.tab-btn[data-tab="stats"]    - 系统总览 Tab 按钮
.tab-btn[data-tab="users"]    - 用户管理 Tab 按钮
.tab-btn[data-tab="files"]    - 文件管理 Tab 按钮
.tab-btn[data-tab="shares"]   - 分享管理 Tab 按钮
#pane-stats / #pane-users / #pane-files / #pane-shares  - Tab 内容面板
#stats-grid                   - 系统统计卡片容器
#users-table-wrapper          - 用户表格容器
#files-table-wrapper          - 文件表格容器
#shares-table-wrapper         - 分享表格容器
#reset-pwd-modal              - 重置密码弹窗 overlay
#reset-pwd-username           - 弹窗中显示的用户名
#reset-pwd-input              - 新密码输入框
#toast                        - 提示消息元素
```

admin.html 中已定义的 onclick 回调（admin.js 必须实现为全局函数）：
- `loadUsers()` - 用户管理刷新按钮
- `loadFiles()` - 文件管理刷新按钮
- `loadShares()` - 分享管理刷新按钮
- `closeResetPwdModal()` - 弹窗取消按钮
- `confirmResetPwd()` - 弹窗确认按钮

## 实施方案

### 文件：`web/admin.js`（新建）

使用 IIFE 封装，暴露必要的全局函数（因 admin.html 使用 onclick 内联绑定）。

#### 1. 常量与状态

```javascript
const API = {
    me: '/api/me',
    stats: '/api/admin/stats',
    users: '/api/admin/users',
    shares: '/api/admin/shares',
    files: '/api/files'
};

let resetPwdTarget = { id: '', username: '' };  // 当前重置密码的目标用户
```

#### 2. 工具函数

```javascript
// apiFetch - 带认证的 fetch，401 时重定向登录
function apiFetch(input, init) {
    return fetch(input, init).then(res => {
        if (res.status === 401) {
            window.location.href = '/web/login.html?reason=session_expired';
        }
        return res;
    });
}

// fmtSize - 字节格式化（复用 app.js 逻辑）
function fmtSize(bytes) {
    if (!bytes) return '0 B';
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
    if (bytes < 1073741824) return (bytes / 1048576).toFixed(2) + ' MB';
    return (bytes / 1073741824).toFixed(2) + ' GB';
}

// fmtDate - Unix 时间戳格式化（注意：admin.go 返回秒级时间戳，不是 ISO）
function fmtDate(ts) {
    if (!ts) return '—';
    const d = new Date(ts * 1000);  // 秒 → 毫秒
    const pad = n => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// showToast - 提示消息
function showToast(msg, type = '') {
    const el = document.getElementById('toast');
    el.textContent = msg;
    el.className = 'toast show ' + type;
    setTimeout(() => el.className = 'toast', 2500);
}

// escapeHtml - 防 XSS
function escapeHtml(s) {
    if (s == null) return '';
    return String(s).replace(/[&<>"']/g, c => ({
        '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'
    }[c]));
}
```

#### 3. 权限守卫

```javascript
// 检查当前用户是否为 admin，否则重定向到 index
async function checkAdmin() {
    try {
        const res = await apiFetch(API.me);
        if (!res.ok) {
            window.location.href = '/web/login.html';
            return null;
        }
        const user = await res.json();
        if (user.role !== 'admin') {
            window.location.href = '/web/index.html';
            return null;
        }
        document.getElementById('admin-user-info').textContent = user.username;
        return user;
    } catch (e) {
        window.location.href = '/web/login.html';
        return null;
    }
}
```

#### 4. Tab 切换

```javascript
// 切换 Tab：切换 .active 类
function switchTab(tabName) {
    document.querySelectorAll('.tab-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.tab === tabName);
    });
    document.querySelectorAll('.tab-pane').forEach(pane => {
        pane.classList.toggle('active', pane.id === 'pane-' + tabName);
    });
}
```

绑定 Tab 按钮点击事件（在 init 中）。

#### 5. 系统总览（loadStats）

```javascript
async function loadStats() {
    const grid = document.getElementById('stats-grid');
    grid.innerHTML = '<div class="loading">加载中...</div>';
    try {
        const res = await apiFetch(API.stats);
        if (!res.ok) throw new Error('加载失败');
        const s = await res.json();
        grid.innerHTML = `
            ${statCard('总用户数', s.total_users, 'active: ' + s.active_users + ' / disabled: ' + s.disabled_users)}
            ${statCard('总文件数', s.total_files)}
            ${statCard('总存储大小', fmtSize(s.total_size))}
            ${statCard('总分享数', s.total_shares)}
            ${statCard('回收站文件', s.trash_files, fmtSize(s.trash_size))}
        `;
    } catch (e) {
        grid.innerHTML = '<div class="empty-state">加载失败：' + escapeHtml(e.message) + '</div>';
    }
}

function statCard(label, value, sub = '') {
    return `<div class="stat-card">
        <div class="label">${label}</div>
        <div class="value">${value}</div>
        ${sub ? `<div class="sub">${sub}</div>` : ''}
    </div>`;
}
```

#### 6. 用户管理（loadUsers）

```javascript
// 全局函数（admin.html onclick 调用）
async function loadUsers() {
    const wrapper = document.getElementById('users-table-wrapper');
    wrapper.innerHTML = '<div class="loading">加载中...</div>';
    try {
        const res = await apiFetch(API.users);
        if (!res.ok) throw new Error('加载失败');
        const data = await res.json();
        const users = data.users || [];
        if (users.length === 0) {
            wrapper.innerHTML = '<div class="empty-state">暂无用户</div>';
            return;
        }
        wrapper.innerHTML = `<table class="data-table">
            <thead><tr>
                <th>用户名</th><th>邮箱</th><th>角色</th><th>状态</th>
                <th>已用空间</th><th>文件数</th><th>创建时间</th><th>操作</th>
            </tr></thead>
            <tbody>
                ${users.map(u => `
                    <tr>
                        <td>${escapeHtml(u.username)}</td>
                        <td>${escapeHtml(u.email || '—')}</td>
                        <td>${u.role === 'admin' ? '<span class="status-tag status-active">admin</span>' : 'user'}</td>
                        <td>${statusTag(u.status)}</td>
                        <td>${fmtSize(u.used_size)}</td>
                        <td>${u.file_count}</td>
                        <td>${fmtDate(u.created_at)}</td>
                        <td>${userActions(u)}</td>
                    </tr>
                `).join('')}
            </tbody>
        </table>`;
    } catch (e) {
        wrapper.innerHTML = '<div class="empty-state">加载失败：' + escapeHtml(e.message) + '</div>';
    }
}

function statusTag(status) {
    const map = {
        'active': '<span class="status-tag status-active">active</span>',
        'disabled': '<span class="status-tag status-disabled">disabled</span>',
        'pending': '<span class="status-tag status-pending">pending</span>'
    };
    return map[status] || status;
}

function userActions(u) {
    if (u.role === 'admin') return '<span style="color:#7a8a9e;">—</span>';
    const toggleBtn = u.status === 'active'
        ? `<button class="btn btn-danger" onclick="toggleUserStatus('${u.id}','disabled')">禁用</button>`
        : `<button class="btn btn-primary" onclick="toggleUserStatus('${u.id}','active')">启用</button>`;
    const resetBtn = `<button class="btn" onclick="openResetPwdModal('${u.id}','${escapeHtml(u.username)}')">重置密码</button>`;
    return toggleBtn + resetBtn;
}

// 禁用/启用用户
async function toggleUserStatus(userId, status) {
    if (!confirm(`确认${status === 'disabled' ? '禁用' : '启用'}该用户？`)) return;
    try {
        const res = await apiFetch(`${API.users}/${userId}/status`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ status })
        });
        const data = await res.json();
        if (!res.ok) {
            showToast(data.message || '操作失败', 'error');
            return;
        }
        showToast('操作成功', 'success');
        loadUsers();
    } catch (e) {
        showToast('网络错误', 'error');
    }
}
```

#### 7. 重置密码弹窗

```javascript
// 打开重置密码弹窗
function openResetPwdModal(id, username) {
    resetPwdTarget = { id, username };
    document.getElementById('reset-pwd-username').textContent = username;
    document.getElementById('reset-pwd-input').value = '';
    document.getElementById('reset-pwd-modal').classList.add('show');
    document.getElementById('reset-pwd-input').focus();
}

// 关闭弹窗（admin.html onclick 调用）
function closeResetPwdModal() {
    document.getElementById('reset-pwd-modal').classList.remove('show');
}

// 确认重置密码（admin.html onclick 调用）
async function confirmResetPwd() {
    const newPwd = document.getElementById('reset-pwd-input').value;
    if (!newPwd) {
        showToast('请输入新密码', 'error');
        return;
    }
    if (newPwd.length < 8) {
        showToast('密码至少 8 位', 'error');
        return;
    }
    try {
        const res = await apiFetch(`${API.users}/${resetPwdTarget.id}/reset-password`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ new_password: newPwd })
        });
        const data = await res.json();
        if (!res.ok) {
            showToast(data.message || '重置失败', 'error');
            return;
        }
        showToast('密码已重置', 'success');
        closeResetPwdModal();
    } catch (e) {
        showToast('网络错误', 'error');
    }
}
```

#### 8. 文件管理（loadFiles）

```javascript
// 全局函数（admin.html onclick 调用）
async function loadFiles() {
    const wrapper = document.getElementById('files-table-wrapper');
    wrapper.innerHTML = '<div class="loading">加载中...</div>';
    try {
        // admin 调用 /api/files?all=true 返回所有用户文件
        const res = await apiFetch(API.files + '?all=true');
        if (!res.ok) throw new Error('加载失败');
        const data = await res.json();
        const files = data.files || data || [];
        if (!Array.isArray(files) || files.length === 0) {
            wrapper.innerHTML = '<div class="empty-state">暂无文件</div>';
            return;
        }
        wrapper.innerHTML = `<table class="data-table">
            <thead><tr>
                <th>文件名</th><th>所有者</th><th>大小</th><th>创建时间</th><th>状态</th>
            </tr></thead>
            <tbody>
                ${files.map(f => `
                    <tr>
                        <td>${escapeHtml(f.filename || f.name || '—')}</td>
                        <td>${escapeHtml(f.owner || '—')}</td>
                        <td>${fmtSize(f.size)}</td>
                        <td>${fmtDate(f.created_at)}</td>
                        <td>${f.status === 'completed' ? '<span class="status-tag status-active">completed</span>' : '<span class="status-tag status-pending">' + escapeHtml(f.status) + '</span>'}</td>
                    </tr>
                `).join('')}
            </tbody>
        </table>`;
    } catch (e) {
        wrapper.innerHTML = '<div class="empty-state">加载失败：' + escapeHtml(e.message) + '</div>';
    }
}
```

**注意**：文件列表的返回格式需要根据 file.go 实际响应调整。先按 `data.files` 数组处理，若格式不符再调整。

#### 9. 分享管理（loadShares）

```javascript
// 全局函数（admin.html onclick 调用）
async function loadShares() {
    const wrapper = document.getElementById('shares-table-wrapper');
    wrapper.innerHTML = '<div class="loading">加载中...</div>';
    try {
        const res = await apiFetch(API.shares);
        if (!res.ok) throw new Error('加载失败');
        const data = await res.json();
        const shares = data.shares || [];
        if (shares.length === 0) {
            wrapper.innerHTML = '<div class="empty-state">暂无分享</div>';
            return;
        }
        wrapper.innerHTML = `<table class="data-table">
            <thead><tr>
                <th>分享ID</th><th>创建者</th><th>类型</th><th>目标</th>
                <th>下载数</th><th>有效期</th><th>状态</th><th>操作</th>
            </tr></thead>
            <tbody>
                ${shares.map(s => `
                    <tr>
                        <td><code>${escapeHtml(s.id)}</code></td>
                        <td>${escapeHtml(s.created_by)}</td>
                        <td>${s.share_type === 'file' ? '文件' : '目录'}</td>
                        <td>${escapeHtml(s.file_id || s.dir_prefix || '—')}</td>
                        <td>${s.download_count}</td>
                        <td>${s.expires_at ? fmtDate(s.expires_at) : '永久'}</td>
                        <td>${shareStatusTag(s)}</td>
                        <td>
                            <a class="btn" href="/api/s/${s.id}" target="_blank">查看</a>
                            <button class="btn btn-danger" onclick="deleteShare('${s.id}')">删除</button>
                        </td>
                    </tr>
                `).join('')}
            </tbody>
        </table>`;
    } catch (e) {
        wrapper.innerHTML = '<div class="empty-state">加载失败：' + escapeHtml(e.message) + '</div>';
    }
}

function shareStatusTag(s) {
    if (!s.is_active) return '<span class="status-tag status-disabled">已停用</span>';
    if (s.is_expired) return '<span class="status-tag status-disabled">已过期</span>';
    if (s.has_password) return '<span class="status-tag status-pending">加密</span>';
    return '<span class="status-tag status-active">活跃</span>';
}

// 删除分享
async function deleteShare(shareId) {
    if (!confirm('确认删除该分享？')) return;
    try {
        const res = await apiFetch(`${API.shares}/${shareId}`, { method: 'DELETE' });
        if (!res.ok) {
            showToast('删除失败', 'error');
            return;
        }
        showToast('已删除', 'success');
        loadShares();
    } catch (e) {
        showToast('网络错误', 'error');
    }
}
```

#### 10. 初始化

```javascript
// 页面加载初始化
async function init() {
    // 绑定 Tab 按钮
    document.querySelectorAll('.tab-btn').forEach(btn => {
        btn.addEventListener('click', () => switchTab(btn.dataset.tab));
    });

    // 绑定回车键提交重置密码
    document.getElementById('reset-pwd-input').addEventListener('keypress', e => {
        if (e.key === 'Enter') confirmResetPwd();
    });

    // 权限守卫
    const user = await checkAdmin();
    if (!user) return;

    // 默认加载系统总览
    loadStats();
}

document.addEventListener('DOMContentLoaded', init);
```

#### 11. 全局函数暴露

由于 admin.html 使用 `onclick="loadUsers()"` 等内联绑定，需要将这些函数暴露到全局：

```javascript
window.loadUsers = loadUsers;
window.loadFiles = loadFiles;
window.loadShares = loadShares;
window.toggleUserStatus = toggleUserStatus;
window.openResetPwdModal = openResetPwdModal;
window.closeResetPwdModal = closeResetPwdModal;
window.confirmResetPwd = confirmResetPwd;
window.deleteShare = deleteShare;
```

## 实施步骤

1. 创建 `web/admin.js` 文件，按上述 11 个部分实现完整逻辑
2. 本地编译验证后端（确认无回归）
3. 本地启动服务器（SQLite 模式）
4. 浏览器验证：
   - 非 admin 访问 admin.html → 重定向到 index.html
   - admin 登录 → 进入 admin.html
   - 4 个 Tab 切换正常
   - 系统总览：统计卡片正确显示
   - 用户管理：列表加载、禁用/启用、重置密码
   - 文件管理：列表加载（所有用户文件）
   - 分享管理：列表加载、删除分享
5. 修复验证中发现的问题
6. git commit（admin.js + 相关改动）
7. 更新 README.md
8. 创建会话存档

## 验证清单

- [ ] admin.js 文件创建完成，无语法错误
- [ ] 非 admin 访问 admin.html 重定向到 /web/index.html
- [ ] 未登录访问 admin.html 重定向到 /web/login.html
- [ ] 系统总览 Tab：5 个统计卡片正确显示
- [ ] 用户管理 Tab：用户列表加载，含用户名/邮箱/角色/状态/已用空间/文件数/创建时间
- [ ] 禁用用户：弹出确认框，确认后用户状态变为 disabled
- [ ] 启用用户：状态变为 active
- [ ] 管理员账号不显示禁用/重置密码按钮
- [ ] 重置密码弹窗：输入新密码 → 确认 → 提示成功
- [ ] 密码强度校验：< 8 位提示错误
- [ ] 文件管理 Tab：显示所有用户文件列表
- [ ] 分享管理 Tab：分享列表加载，含创建者/类型/下载数/有效期/状态
- [ ] 删除分享：弹出确认框，确认后从列表消失
- [ ] Tab 切换不丢失数据（已加载的数据保持）
- [ ] Toast 提示正常显示（成功绿色、失败粉色）
- [ ] XSS 防护：用户名/邮箱含 HTML 字符时正确转义

## 风险与注意事项

1. **文件列表 API 响应格式**：`/api/files?all=true` 的返回格式需实际测试确认。如果返回的是 `{files: [...]}` 还是直接数组，需调整 loadFiles 中的解析逻辑。
2. **时间戳处理**：admin.go 返回 Unix 秒级时间戳，fmtDate 需 `* 1000` 转毫秒。这与 app.js 中的 fmtDate（接受 ISO 字符串）不同。
3. **XSS 防护**：所有用户输入内容（用户名、邮箱、文件名）必须 escapeHtml。
4. **CORS**：admin.js 与 admin.html 同源，无需特殊处理。
5. **onclick 内联绑定**：必须将函数暴露到 window 全局，否则 onclick 无法调用。
