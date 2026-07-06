/**
 * FileSync 独立分享页面逻辑
 *
 * 访客无需登录即可查看分享的文件/目录信息并下载。
 * 使用原生 fetch（非 app.js 的 apiFetch），因为分享页面不涉及认证，
 * 不应触发 401 跳转登录页的行为。
 *
 * 后端接口：
 *   GET /api/s/{id}          - 获取分享公开信息（SharePublicInfo）
 *   GET /api/s/{id}/download - 下载文件或目录（目录打包为 ZIP）
 */

(function () {
    'use strict';

    // === 配置 ===
    var API_BASE = '/api/s/';

    // 从 URL 查询参数中解析分享 ID（如 ?id=abc123 → abc123）
    var params = new URLSearchParams(window.location.search);
    var shareId = params.get('id');

    // === 工具函数 ===

    /**
     * 格式化文件大小为人类可读字符串
     * @param {number} bytes - 字节数
     * @returns {string} 格式化后的大小，如 "1.50 MB"
     */
    function fmtSize(bytes) {
        if (!bytes || bytes < 0) return '—';
        if (bytes < 1024) return bytes + ' B';
        if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
        if (bytes < 1073741824) return (bytes / 1048576).toFixed(2) + ' MB';
        return (bytes / 1073741824).toFixed(2) + ' GB';
    }

    /**
     * 格式化 ISO 时间戳为 YYYY-MM-DD HH:mm
     * @param {string} iso - ISO 8601 时间字符串
     * @returns {string} 格式化后的时间，如 "2026-07-06 15:30"
     */
    function fmtDate(iso) {
        if (!iso) return '永久';
        var d = new Date(iso);
        if (isNaN(d.getTime())) return '永久';
        var pad = function (n) { return String(n).padStart(2, '0'); };
        return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate())
            + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
    }

    // === 状态切换函数 ===

    /**
     * 显示加载中状态（隐藏其他状态）
     */
    function showLoading() {
        document.getElementById('share-loading').hidden = false;
        document.getElementById('share-error').hidden = true;
        document.getElementById('share-content').hidden = true;
    }

    /**
     * 显示错误状态
     * @param {string} title - 错误标题
     * @param {string} msg - 错误描述
     */
    function showError(title, msg) {
        document.getElementById('share-loading').hidden = true;
        var errEl = document.getElementById('share-error');
        errEl.hidden = false;
        document.getElementById('share-error-title').textContent = title;
        document.getElementById('share-error-msg').textContent = msg;
        document.getElementById('share-content').hidden = true;
    }

    /**
     * 显示分享内容（正常状态）
     * @param {Object} info - SharePublicInfo 对象
     */
    function showContent(info) {
        document.getElementById('share-loading').hidden = true;
        document.getElementById('share-error').hidden = true;
        document.getElementById('share-content').hidden = false;

        // 类型徽章
        var typeBadge = document.getElementById('share-type-badge');
        typeBadge.textContent = info.share_type === 'dir' ? '目录' : '文件';

        // 名称
        document.getElementById('share-name').textContent = info.name || '—';

        // 大小
        document.getElementById('share-size').textContent = fmtSize(info.size);

        // 文件数（仅目录显示）
        var countItem = document.getElementById('share-count-item');
        if (info.share_type === 'dir') {
            countItem.hidden = false;
            document.getElementById('share-count').textContent = info.file_count || 0;
        } else {
            countItem.hidden = true;
        }

        // 下载次数
        document.getElementById('share-downloads').textContent = info.download_count || 0;

        // 有效期
        document.getElementById('share-expiry').textContent = fmtDate(info.expires_at);
    }

    // === 下载处理 ===

    /**
     * 触发下载：浏览器直接跳转到下载 URL
     * 由后端设置 Content-Disposition 触发文件保存对话框，
     * 避免 fetch 在内存中缓存整个文件。
     */
    function startDownload() {
        window.location.href = API_BASE + shareId + '/download';
    }

    // === 初始化 ===

    /**
     * 初始化：校验 ID → 请求分享信息 → 渲染对应状态
     */
    async function init() {
        if (!shareId) {
            showError('链接无效', '分享 ID 缺失，请检查链接是否完整。');
            return;
        }

        showLoading();

        try {
            var res = await fetch(API_BASE + shareId);
            if (res.status === 404) {
                showError('分享不存在', '该分享链接无效或已被删除。');
                return;
            }
            if (res.status === 410) {
                showError('分享已过期', '该分享链接已超过有效期。');
                return;
            }
            if (!res.ok) {
                showError('加载失败', '服务器返回错误: HTTP ' + res.status);
                return;
            }
            var info = await res.json();
            if (info.is_expired) {
                showError('分享已过期', '该分享链接已超过有效期。');
                return;
            }
            showContent(info);
        } catch (e) {
            showError('网络错误', '无法连接到服务器，请稍后重试。');
        }
    }

    // 绑定下载按钮事件
    document.getElementById('share-download-btn').addEventListener('click', startDownload);

    // 启动初始化
    init();
})();
