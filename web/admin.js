/**
 * FileSync 管理后台 JavaScript
 * 负责：权限守卫、Tab 切换、系统统计/用户管理/文件管理/分享管理 的数据加载与操作
 */
(function () {
    'use strict';

    // === 1. 常量与状态 ===
    const API = {
        me: '/api/me',
        stats: '/api/admin/stats',
        users: '/api/admin/users',
        shares: '/api/admin/shares',
        files: '/api/files'
    };

    // 当前重置密码的目标用户
    let resetPwdTarget = { id: '', username: '' };

    // === 2. 工具函数 ===

    /**
     * apiFetch - 带认证的 fetch，401 时重定向到登录页
     * @param {string} input - 请求 URL
     * @param {Object} init - fetch init
     */
    function apiFetch(input, init) {
        return fetch(input, init).then(res => {
            if (res.status === 401) {
                window.location.href = '/web/login.html?reason=session_expired';
            }
            return res;
        });
    }

    /**
     * fmtSize - 字节格式化为 B/KB/MB/GB
     * @param {number} bytes - 字节数
     * @returns {string} 格式化后的大小
     */
    function fmtSize(bytes) {
        if (!bytes) return '0 B';
        if (bytes < 1024) return bytes + ' B';
        if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
        if (bytes < 1073741824) return (bytes / 1048576).toFixed(2) + ' MB';
        return (bytes / 1073741824).toFixed(2) + ' GB';
    }

    /**
     * fmtDate - 时间格式化（兼容 Unix 秒级时间戳和 RFC3339 字符串）
     * @param {number|string} ts - Unix 时间戳（秒）或 ISO/RFC3339 字符串
     * @returns {string} YYYY-MM-DD HH:mm
     */
    function fmtDate(ts) {
        if (!ts) return '—';
        let d;
        if (typeof ts === 'number') {
            d = new Date(ts * 1000); // 秒 → 毫秒
        } else {
            d = new Date(ts); // ISO 字符串
        }
        if (isNaN(d.getTime())) return '—';
        const pad = n => String(n).padStart(2, '0');
        return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
    }

    /**
     * showToast - 显示提示消息
     * @param {string} msg - 消息内容
     * @param {string} type - 类型：'' | 'success' | 'error'
     */
    function showToast(msg, type) {
        const el = document.getElementById('toast');
        if (!el) return;
        el.textContent = msg;
        el.className = 'toast show ' + (type || '');
        setTimeout(() => { el.className = 'toast'; }, 2500);
    }

    /**
     * escapeHtml - HTML 转义，防止 XSS
     * @param {string} s - 原始字符串
     * @returns {string} 转义后的字符串
     */
    function escapeHtml(s) {
        if (s == null) return '';
        return String(s).replace(/[&<>"']/g, c => ({
            '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
        }[c]));
    }

    // === 3. 权限守卫 ===

    /**
     * checkAdmin - 检查当前用户是否为 admin
     * 非 admin 重定向到 index.html，未登录重定向到 login.html
     * @returns {Promise<Object|null>} 用户信息或 null（已重定向）
     */
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
            const infoEl = document.getElementById('admin-user-info');
            if (infoEl) infoEl.textContent = user.username;
            return user;
        } catch (e) {
            window.location.href = '/web/login.html';
            return null;
        }
    }

    // === 4. Tab 切换 ===

    /**
     * switchTab - 切换 Tab 激活状态
     * @param {string} tabName - Tab 名称：stats|users|files|shares
     */
    function switchTab(tabName) {
        document.querySelectorAll('.tab-btn').forEach(btn => {
            btn.classList.toggle('active', btn.dataset.tab === tabName);
        });
        document.querySelectorAll('.tab-pane').forEach(pane => {
            pane.classList.toggle('active', pane.id === 'pane-' + tabName);
        });
    }

    // === 5. 系统总览 ===

    /**
     * statCard - 生成单个统计卡片的 HTML
     * @param {string} label - 标签
     * @param {string|number} value - 主值
     * @param {string} sub - 副文本（可选）
     * @returns {string} 卡片 HTML
     */
    function statCard(label, value, sub) {
        return `<div class="stat-card">
            <div class="label">${escapeHtml(label)}</div>
            <div class="value">${escapeHtml(value)}</div>
            ${sub ? `<div class="sub">${escapeHtml(sub)}</div>` : ''}
        </div>`;
    }

    /**
     * loadStats - 加载系统统计总览
     * 调用 GET /api/admin/stats，渲染 5 个统计卡片
     */
    async function loadStats() {
        const grid = document.getElementById('stats-grid');
        if (!grid) return;
        grid.innerHTML = '<div class="loading">加载中...</div>';
        try {
            const res = await apiFetch(API.stats);
            if (!res.ok) throw new Error('加载失败 (' + res.status + ')');
            const s = await res.json();
            grid.innerHTML =
                statCard('总用户数', s.total_users, 'active: ' + s.active_users + ' / disabled: ' + s.disabled_users) +
                statCard('总文件数', s.total_files) +
                statCard('总存储大小', fmtSize(s.total_size)) +
                statCard('总分享数', s.total_shares) +
                statCard('回收站文件', s.trash_files, fmtSize(s.trash_size));
        } catch (e) {
            grid.innerHTML = '<div class="empty-state">加载失败：' + escapeHtml(e.message) + '</div>';
        }
    }

    // === 6. 用户管理 ===

    /**
     * statusTag - 生成用户状态标签 HTML
     * @param {string} status - active|disabled|pending
     * @returns {string} 标签 HTML
     */
    function statusTag(status) {
        const map = {
            'active': '<span class="status-tag status-active">active</span>',
            'disabled': '<span class="status-tag status-disabled">disabled</span>',
            'pending': '<span class="status-tag status-pending">pending</span>'
        };
        return map[status] || escapeHtml(status);
    }

    /**
     * userActions - 生成用户操作按钮 HTML（admin 账号不显示操作）
     * @param {Object} u - 用户对象
     * @returns {string} 按钮 HTML
     */
    function userActions(u) {
        if (u.role === 'admin') return '<span style="color:#7a8a9e;">—</span>';
        const toggleBtn = u.status === 'active'
            ? `<button class="btn btn-danger" onclick="toggleUserStatus('${escapeHtml(u.id)}','disabled')">禁用</button>`
            : `<button class="btn btn-primary" onclick="toggleUserStatus('${escapeHtml(u.id)}','active')">启用</button>`;
        const resetBtn = `<button class="btn" onclick="openResetPwdModal('${escapeHtml(u.id)}','${escapeHtml(u.username)}')">重置密码</button>`;
        return toggleBtn + resetBtn;
    }

    /**
     * loadUsers - 加载用户列表
     * 全局函数（admin.html onclick 调用）
     * 调用 GET /api/admin/users，渲染用户表格
     */
    async function loadUsers() {
        const wrapper = document.getElementById('users-table-wrapper');
        if (!wrapper) return;
        wrapper.innerHTML = '<div class="loading">加载中...</div>';
        try {
            const res = await apiFetch(API.users);
            if (!res.ok) throw new Error('加载失败 (' + res.status + ')');
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

    /**
     * toggleUserStatus - 禁用/启用用户
     * @param {string} userId - 用户 ID
     * @param {string} status - 目标状态：active|disabled
     */
    async function toggleUserStatus(userId, status) {
        if (!confirm(`确认${status === 'disabled' ? '禁用' : '启用'}该用户？`)) return;
        try {
            const res = await apiFetch(`${API.users}/${userId}/status`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ status: status })
            });
            const data = await res.json().catch(() => ({}));
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

    // === 7. 重置密码弹窗 ===

    /**
     * openResetPwdModal - 打开重置密码弹窗
     * @param {string} id - 用户 ID
     * @param {string} username - 用户名
     */
    function openResetPwdModal(id, username) {
        resetPwdTarget = { id: id, username: username };
        const nameEl = document.getElementById('reset-pwd-username');
        const inputEl = document.getElementById('reset-pwd-input');
        const modalEl = document.getElementById('reset-pwd-modal');
        if (nameEl) nameEl.textContent = username;
        if (inputEl) inputEl.value = '';
        if (modalEl) modalEl.classList.add('show');
        if (inputEl) inputEl.focus();
    }

    /**
     * closeResetPwdModal - 关闭重置密码弹窗
     * 全局函数（admin.html onclick 调用）
     */
    function closeResetPwdModal() {
        const modalEl = document.getElementById('reset-pwd-modal');
        if (modalEl) modalEl.classList.remove('show');
    }

    /**
     * confirmResetPwd - 确认重置密码
     * 全局函数（admin.html onclick 调用）
     * 调用 POST /api/admin/users/{id}/reset-password
     */
    async function confirmResetPwd() {
        const inputEl = document.getElementById('reset-pwd-input');
        if (!inputEl) return;
        const newPwd = inputEl.value;
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
            const data = await res.json().catch(() => ({}));
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

    // === 8. 文件管理 ===

    /**
     * loadFiles - 加载所有用户文件列表
     * 全局函数（admin.html onclick 调用）
     * 调用 GET /api/files（admin 默认返回所有用户文件）
     */
    async function loadFiles() {
        const wrapper = document.getElementById('files-table-wrapper');
        if (!wrapper) return;
        wrapper.innerHTML = '<div class="loading">加载中...</div>';
        try {
            const res = await apiFetch(API.files);
            if (!res.ok) throw new Error('加载失败 (' + res.status + ')');
            const data = await res.json();
            // /api/files 返回直接数组（非 {files: [...]} 对象），无文件时返回 []
            const files = Array.isArray(data) ? data : (data && data.files) || [];
            if (files.length === 0) {
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
                            <td>${f.status === 'completed' ? '<span class="status-tag status-active">completed</span>' : '<span class="status-tag status-pending">' + escapeHtml(f.status || 'unknown') + '</span>'}</td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>`;
        } catch (e) {
            wrapper.innerHTML = '<div class="empty-state">加载失败：' + escapeHtml(e.message) + '</div>';
        }
    }

    // === 9. 分享管理 ===

    /**
     * shareStatusTag - 生成分享状态标签 HTML
     * @param {Object} s - 分享对象
     * @returns {string} 标签 HTML
     */
    function shareStatusTag(s) {
        if (!s.is_active) return '<span class="status-tag status-disabled">已停用</span>';
        if (s.is_expired) return '<span class="status-tag status-disabled">已过期</span>';
        if (s.has_password) return '<span class="status-tag status-pending">加密</span>';
        return '<span class="status-tag status-active">活跃</span>';
    }

    /**
     * loadShares - 加载所有分享列表
     * 全局函数（admin.html onclick 调用）
     * 调用 GET /api/admin/shares
     */
    async function loadShares() {
        const wrapper = document.getElementById('shares-table-wrapper');
        if (!wrapper) return;
        wrapper.innerHTML = '<div class="loading">加载中...</div>';
        try {
            const res = await apiFetch(API.shares);
            if (!res.ok) throw new Error('加载失败 (' + res.status + ')');
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
                                <a class="btn" href="/api/s/${encodeURIComponent(s.id)}" target="_blank">查看</a>
                                <button class="btn btn-danger" onclick="deleteShare('${escapeHtml(s.id)}')">删除</button>
                            </td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>`;
        } catch (e) {
            wrapper.innerHTML = '<div class="empty-state">加载失败：' + escapeHtml(e.message) + '</div>';
        }
    }

    /**
     * deleteShare - 删除分享
     * @param {string} shareId - 分享 ID
     */
    async function deleteShare(shareId) {
        if (!confirm('确认删除该分享？')) return;
        try {
            const res = await apiFetch(`${API.shares}/${encodeURIComponent(shareId)}`, { method: 'DELETE' });
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

    // === 10. 初始化 ===

    /**
     * init - 页面加载初始化
     * 绑定 Tab 按钮、回车键提交、权限守卫、默认加载系统总览
     */
    async function init() {
        // 绑定 Tab 按钮
        document.querySelectorAll('.tab-btn').forEach(btn => {
            btn.addEventListener('click', () => switchTab(btn.dataset.tab));
        });

        // 绑定回车键提交重置密码
        const pwdInput = document.getElementById('reset-pwd-input');
        if (pwdInput) {
            pwdInput.addEventListener('keypress', e => {
                if (e.key === 'Enter') confirmResetPwd();
            });
        }

        // 权限守卫
        const user = await checkAdmin();
        if (!user) return;

        // 默认加载系统总览
        loadStats();
    }

    // === 11. 全局函数暴露（admin.html onclick 内联绑定需要） ===
    window.loadUsers = loadUsers;
    window.loadFiles = loadFiles;
    window.loadShares = loadShares;
    window.toggleUserStatus = toggleUserStatus;
    window.openResetPwdModal = openResetPwdModal;
    window.closeResetPwdModal = closeResetPwdModal;
    window.confirmResetPwd = confirmResetPwd;
    window.deleteShare = deleteShare;

    document.addEventListener('DOMContentLoaded', init);
})();
