/**
 * FileSync 独立分享页面逻辑（增强版）
 *
 * 功能：
 *   - 文件分享：显示文件信息 + 下载按钮
 *   - 目录分享：目录浏览（支持子目录导航）+ 单文件下载 + 多选批量下载 ZIP
 *   - 转存到我的文件：登录用户可转存分享文件到自己账户
 *
 * 后端接口：
 *   GET  /api/s/{id}                - 获取分享公开信息
 *   GET  /api/s/{id}/download       - 下载文件/整目录 ZIP，?path= 下载单文件
 *   GET  /api/s/{id}/list           - 列出目录内容，?path= 指定子目录
 *   POST /api/s/{id}/batch          - 批量下载 ZIP（Body: {paths:[]})
 *   POST /api/share/save            - 转存到我的文件（需登录）
 *   GET  /api/me                    - 检查登录状态
 */

(function () {
    'use strict';

    // === 配置 ===
    var API_BASE = '/api/s/';
    var SHARE_API = '/api/share';

    // 从 URL 查询参数中解析分享 ID
    var params = new URLSearchParams(window.location.search);
    var shareId = params.get('id');

    // 全局状态
    var currentShareType = '';      // 'file' | 'dir'
    var currentPath = '';           // 当前浏览的子目录（相对分享根）
    var isLoggedIn = false;         // 是否已登录
    var selectedFiles = new Set();  // 选中的文件路径（相对分享根）
    var downloadToken = '';         // 下载签名 token（防盗链，从 /api/s/{id} 获取，30 分钟有效）

    // === 工具函数 ===

    /** 格式化文件大小为人类可读字符串 */
    function fmtSize(bytes) {
        if (!bytes || bytes < 0) return '—';
        if (bytes < 1024) return bytes + ' B';
        if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
        if (bytes < 1073741824) return (bytes / 1048576).toFixed(2) + ' MB';
        return (bytes / 1073741824).toFixed(2) + ' GB';
    }

    /** 格式化 ISO 时间戳为 YYYY-MM-DD HH:mm */
    function fmtDate(iso) {
        if (!iso) return '永久';
        var d = new Date(iso);
        if (isNaN(d.getTime())) return '永久';
        var pad = function (n) { return String(n).padStart(2, '0'); };
        return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate())
            + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
    }

    /** HTML 转义，防止 XSS */
    function escapeHtml(s) {
        if (s == null) return '';
        return String(s)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }

    /** 拼接相对路径（避免 // 重复） */
    function joinPath(base, sub) {
        if (!base) return sub || '';
        if (!sub) return sub || base;
        if (base.endsWith('/')) return base + sub;
        return base + '/' + sub;
    }

    // === 文件类型判断（用于预览按钮显隐和渲染器选择） ===

    /** 从文件名提取小写扩展名（不含点） */
    function getFileExt(name) {
        if (!name) return '';
        var idx = name.lastIndexOf('.');
        if (idx < 0 || idx === name.length - 1) return '';
        return name.slice(idx + 1).toLowerCase();
    }

    /** 根据扩展名判断预览类型，返回 image|pdf|text|audio|video|unsupported */
    function getPreviewType(name) {
        var ext = getFileExt(name);
        var imageExts = ['jpg', 'jpeg', 'png', 'gif', 'webp', 'bmp', 'svg', 'ico', 'avif'];
        var pdfExts = ['pdf'];
        var textExts = ['txt', 'md', 'markdown', 'log', 'csv', 'tsv',
            'js', 'ts', 'jsx', 'tsx', 'mjs', 'cjs',
            'json', 'json5', 'yaml', 'yml', 'toml', 'ini', 'cfg', 'conf',
            'html', 'htm', 'css', 'scss', 'sass', 'less',
            'py', 'java', 'c', 'h', 'cpp', 'hpp', 'cc', 'cxx', 'go', 'rs', 'rb',
            'php', 'pl', 'sh', 'bash', 'zsh', 'bat', 'ps1',
            'sql', 'graphql', 'gql',
            'xml', 'svg', 'vue', 'svelte',
            'kt', 'kts', 'swift', 'dart', 'scala', 'clj', 'lisp', 'el',
            'lua', 'r', 'jl', 'ex', 'exs', 'erl', 'hs', 'ml', 'fs',
            'dockerfile', 'makefile', 'gitignore', 'env'];
        var audioExts = ['mp3', 'wav', 'ogg', 'oga', 'm4a', 'aac', 'flac', 'opus', 'weba'];
        var videoExts = ['mp4', 'webm', 'ogv', 'mov', 'm4v', 'mkv'];
        if (imageExts.indexOf(ext) >= 0) return 'image';
        if (pdfExts.indexOf(ext) >= 0) return 'pdf';
        if (textExts.indexOf(ext) >= 0) return 'text';
        if (audioExts.indexOf(ext) >= 0) return 'audio';
        if (videoExts.indexOf(ext) >= 0) return 'video';
        return 'unsupported';
    }

    // === 状态切换函数 ===

    function showLoading() {
        document.getElementById('share-loading').hidden = false;
        document.getElementById('share-error').hidden = true;
        document.getElementById('share-lock').hidden = true;
        document.getElementById('share-content').hidden = true;
        document.getElementById('share-browser').hidden = true;
    }

    function showError(title, msg) {
        document.getElementById('share-loading').hidden = true;
        var errEl = document.getElementById('share-error');
        errEl.hidden = false;
        document.getElementById('share-error-title').textContent = title;
        document.getElementById('share-error-msg').textContent = msg;
        document.getElementById('share-lock').hidden = true;
        document.getElementById('share-content').hidden = true;
        document.getElementById('share-browser').hidden = true;
    }

    /** 显示密码门（有密码且未认证时） */
    function showLock() {
        document.getElementById('share-loading').hidden = true;
        document.getElementById('share-error').hidden = true;
        document.getElementById('share-lock').hidden = false;
        document.getElementById('share-content').hidden = true;
        document.getElementById('share-browser').hidden = true;
        var pwdInput = document.getElementById('share-lock-password');
        if (pwdInput) pwdInput.focus();
        var errEl = document.getElementById('share-lock-error');
        if (errEl) errEl.hidden = true;
    }

    /** 显示分享信息（文件或目录通用） */
    function showContent(info) {
        document.getElementById('share-loading').hidden = true;
        document.getElementById('share-error').hidden = true;
        document.getElementById('share-lock').hidden = true;
        document.getElementById('share-content').hidden = false;

        currentShareType = info.share_type;

        // 类型徽章
        document.getElementById('share-type-badge').textContent = info.share_type === 'dir' ? '目录' : '文件';
        document.getElementById('share-name').textContent = info.name || '—';
        document.getElementById('share-size').textContent = fmtSize(info.size);

        // 文件数（仅目录显示）
        var countItem = document.getElementById('share-count-item');
        if (info.share_type === 'dir') {
            countItem.hidden = false;
            document.getElementById('share-count').textContent = info.file_count || 0;
        } else {
            countItem.hidden = true;
        }

        document.getElementById('share-downloads').textContent = info.download_count || 0;
        document.getElementById('share-expiry').textContent = fmtDate(info.expires_at);

        // 单文件分享：根据文件类型决定是否显示预览按钮
        var previewBtn = document.getElementById('share-preview-btn');
        if (info.share_type === 'file') {
            var ptype = getPreviewType(info.name);
            if (ptype !== 'unsupported') {
                previewBtn.hidden = false;
                previewBtn.dataset.name = info.name || '';
                previewBtn.dataset.path = ''; // 单文件分享无需 path 参数
            } else {
                previewBtn.hidden = true;
            }
        } else {
            // 目录分享不在顶部显示预览按钮（预览入口在文件行）
            previewBtn.hidden = true;
        }

        // 目录分享：显示目录浏览区域
        var browser = document.getElementById('share-browser');
        if (info.share_type === 'dir') {
            browser.hidden = false;
            loadDir('');
        } else {
            browser.hidden = true;
        }
    }

    // === 登录状态检查 ===

    /** 检查登录状态，已登录则显示转存按钮 */
    async function checkLogin() {
        try {
            var res = await fetch('/api/me');
            if (res.ok) {
                var data = await res.json();
                if (data.username) {
                    isLoggedIn = true;
                    var btn = document.getElementById('save-to-my-files-btn');
                    if (btn) btn.hidden = false;
                }
            }
        } catch (e) {
            // 静默失败，不影响浏览
        }
    }

    // === 目录浏览 ===

    /** 加载目录内容 */
    async function loadDir(path) {
        currentPath = path || '';
        selectedFiles.clear();
        updateBatchButton();
        var selectAllCb = document.getElementById('select-all-cb');
        if (selectAllCb) selectAllCb.checked = false;

        var listEl = document.getElementById('share-filelist');
        listEl.innerHTML = '<div class="share-browser-loading">加载中…</div>';

        try {
            var url = API_BASE + shareId + '/list?token=' + encodeURIComponent(downloadToken);
            if (currentPath) url += '&path=' + encodeURIComponent(currentPath);
            var res = await fetch(url);
            if (!res.ok) {
                listEl.innerHTML = '<div class="share-browser-error">加载失败 (HTTP ' + res.status + ')</div>';
                return;
            }
            var data = await res.json();
            renderBreadcrumb(data.path || '');
            renderTree(data.dirs || [], data.files || []);
        } catch (e) {
            listEl.innerHTML = '<div class="share-browser-error">网络错误: ' + escapeHtml(e.message) + '</div>';
        }
    }

    /** 渲染面包屑导航 */
    function renderBreadcrumb(path) {
        var bcEl = document.getElementById('share-breadcrumb');
        var html = '<span class="crumb crumb-root" data-path="">根目录</span>';
        if (path) {
            var segments = path.split('/').filter(Boolean);
            var accum = '';
            for (var i = 0; i < segments.length; i++) {
                accum = joinPath(accum, segments[i]);
                html += '<span class="crumb-sep">/</span>';
                html += '<span class="crumb" data-path="' + escapeHtml(accum) + '">' + escapeHtml(segments[i]) + '</span>';
            }
        }
        bcEl.innerHTML = html;
    }

    /** 渲染文件树（目录行 + 文件行） */
    function renderTree(dirs, files) {
        var listEl = document.getElementById('share-filelist');
        var html = '';

        if (dirs.length === 0 && files.length === 0) {
            listEl.innerHTML = '<div class="share-browser-empty">此目录为空</div>';
            return;
        }

        // 目录行
        for (var i = 0; i < dirs.length; i++) {
            var d = dirs[i];
            var dirPath = joinPath(currentPath, d.name);
            html += '<div class="tree-row tree-dir" data-dir-path="' + escapeHtml(dirPath) + '">'
                + '<span class="tree-icon" aria-hidden="true">▸</span>'
                + '<span class="tree-name">' + escapeHtml(d.name) + '</span>'
                + '<span class="tree-meta">' + d.file_count + ' 个文件</span>'
                + '</div>';
        }

        // 文件行
        for (var j = 0; j < files.length; j++) {
            var f = files[j];
            var filePath = joinPath(currentPath, f.name);
            // 仅对可预览的文件类型显示预览按钮（图片/PDF/文本/音频/视频）
            var fPreviewType = getPreviewType(f.name);
            var previewBtnHtml = '';
            if (fPreviewType !== 'unsupported') {
                previewBtnHtml = '<button class="tree-preview-btn" type="button" data-path="' + escapeHtml(filePath) + '" data-name="' + escapeHtml(f.name) + '" data-preview-type="' + fPreviewType + '">预览</button>';
            }
            html += '<div class="tree-row tree-file" data-file-path="' + escapeHtml(filePath) + '" data-file-id="' + escapeHtml(f.id) + '">'
                + '<input type="checkbox" class="tree-cb" data-path="' + escapeHtml(filePath) + '" data-id="' + escapeHtml(f.id) + '">'
                + '<span class="tree-icon" aria-hidden="true">📄</span>'
                + '<span class="tree-name">' + escapeHtml(f.name) + '</span>'
                + '<span class="tree-meta">' + fmtSize(f.size) + '</span>'
                + previewBtnHtml
                + '<button class="tree-download-btn" type="button" data-path="' + escapeHtml(filePath) + '">下载</button>'
                + '</div>';
        }

        listEl.innerHTML = html;
    }

    /** 更新批量下载按钮状态 */
    function updateBatchButton() {
        var btn = document.getElementById('batch-download-btn');
        if (btn) btn.disabled = selectedFiles.size === 0;
    }

    // === 下载处理 ===

    /** 下载整个分享（文件或目录 ZIP） */
    function startDownload() {
        window.location.href = API_BASE + shareId + '/download?token=' + encodeURIComponent(downloadToken);
    }

    /** 下载目录内的单个文件 */
    function downloadFile(path) {
        window.location.href = API_BASE + shareId + '/download?token=' + encodeURIComponent(downloadToken) + '&path=' + encodeURIComponent(path);
    }

    /** 批量下载选中文件为 ZIP */
    async function batchDownload() {
        if (selectedFiles.size === 0) return;
        var paths = Array.from(selectedFiles);
        try {
            var res = await fetch(API_BASE + shareId + '/batch?token=' + encodeURIComponent(downloadToken), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ paths: paths })
            });
            if (!res.ok) {
                alert('批量下载失败: HTTP ' + res.status);
                return;
            }
            // 触发 ZIP 文件下载
            var blob = await res.blob();
            var url = URL.createObjectURL(blob);
            var a = document.createElement('a');
            a.href = url;
            a.download = 'batch.zip';
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(url);
        } catch (e) {
            alert('批量下载失败: ' + e.message);
        }
    }

    // === 预览处理 ===

    /**
     * 构造分享预览 URL
     * @param {string} path 子路径（单文件分享传空字符串）
     * @returns {string} 完整预览 URL
     */
    function buildPreviewUrl(path) {
        var url = API_BASE + shareId + '/preview?token=' + encodeURIComponent(downloadToken);
        if (path) url += '&path=' + encodeURIComponent(path);
        return url;
    }

    /**
     * 打开预览 Modal 并渲染对应类型
     * @param {string} path 子路径（单文件分享传空字符串）
     * @param {string} name 文件名（用于标题和类型判断）
     */
    function previewFile(path, name) {
        var type = getPreviewType(name);
        if (type === 'unsupported') {
            alert('此文件类型不支持预览，请下载后查看');
            return;
        }
        var modal = document.getElementById('preview-modal');
        var titleEl = document.getElementById('preview-title');
        var bodyEl = document.getElementById('preview-body');
        titleEl.textContent = name || '预览';
        bodyEl.innerHTML = '<div class="share-preview-loading">加载中…</div>';
        modal.hidden = false;
        renderPreview(type, buildPreviewUrl(path), name);
    }

    /**
     * 根据类型渲染预览内容
     * @param {string} type image|pdf|text|audio|video
     * @param {string} url 预览 URL
     * @param {string} name 文件名
     */
    function renderPreview(type, url, name) {
        var bodyEl = document.getElementById('preview-body');
        switch (type) {
            case 'image':
                bodyEl.innerHTML = '<img class="share-preview-img" src="' + escapeHtml(url) + '" alt="' + escapeHtml(name) + '" />';
                break;
            case 'pdf':
                bodyEl.innerHTML = '<iframe class="share-preview-pdf" src="' + escapeHtml(url) + '" title="' + escapeHtml(name) + '"></iframe>';
                break;
            case 'audio':
                bodyEl.innerHTML = '<audio class="share-preview-audio" src="' + escapeHtml(url) + '" controls preload="metadata"></audio>';
                break;
            case 'video':
                bodyEl.innerHTML = '<video class="share-preview-video" src="' + escapeHtml(url) + '" controls preload="metadata"></video>';
                break;
            case 'text':
                renderTextPreview(url, bodyEl);
                break;
            default:
                bodyEl.innerHTML = '<div class="share-preview-error">不支持的预览类型</div>';
        }
    }

    /**
     * 渲染文本预览（fetch 文本内容后以 <pre> 展示，避免直接 iframe 显示乱码）
     * @param {string} url 预览 URL
     * @param {HTMLElement} bodyEl 预览容器
     */
    async function renderTextPreview(url, bodyEl) {
        try {
            var res = await fetch(url);
            if (!res.ok) {
                bodyEl.innerHTML = '<div class="share-preview-error">加载失败 (HTTP ' + res.status + ')</div>';
                return;
            }
            var text = await res.text();
            // 限制最大显示长度（避免超长文本卡死浏览器）
            var MAX_LEN = 512 * 1024; // 512KB
            var truncated = false;
            if (text.length > MAX_LEN) {
                text = text.slice(0, MAX_LEN);
                truncated = true;
            }
            var html = '<pre class="share-preview-text"><code>' + escapeHtml(text) + '</code></pre>';
            if (truncated) {
                html += '<div class="share-preview-truncated">文件过大，仅显示前 512KB 内容，完整内容请下载查看</div>';
            }
            bodyEl.innerHTML = html;
        } catch (e) {
            bodyEl.innerHTML = '<div class="share-preview-error">网络错误: ' + escapeHtml(e.message) + '</div>';
        }
    }

    /** 关闭预览 Modal 并释放媒体资源 */
    function closePreview() {
        var modal = document.getElementById('preview-modal');
        var bodyEl = document.getElementById('preview-body');
        // 释放音视频资源（暂停播放 + 清空 src）
        var media = bodyEl.querySelector('video, audio');
        if (media) {
            media.pause();
            media.src = '';
        }
        bodyEl.innerHTML = '';
        modal.hidden = true;
    }

    // === 转存到我的文件 ===

    /** 打开转存对话框（未登录跳转登录页） */
    function openSaveModal() {
        if (!isLoggedIn) {
            var returnUrl = window.location.pathname + window.location.search;
            window.location.href = '/web/login.html?redirect=' + encodeURIComponent(returnUrl);
            return;
        }
        if (selectedFiles.size === 0) {
            alert('请先选择要转存的文件');
            return;
        }
        document.getElementById('save-modal').hidden = false;
        document.getElementById('save-result').hidden = true;
        document.getElementById('save-target-dir').value = '';
    }

    /** 确认转存 */
    async function confirmSave() {
        if (selectedFiles.size === 0) return;
        // 收集选中文件的 ID
        var fileIds = [];
        document.querySelectorAll('.tree-cb:checked').forEach(function (cb) {
            fileIds.push(cb.dataset.id);
        });
        if (fileIds.length === 0) {
            alert('未获取到文件 ID');
            return;
        }
        var targetDir = document.getElementById('save-target-dir').value.trim();
        var resultEl = document.getElementById('save-result');
        var confirmBtn = document.getElementById('save-confirm-btn');

        confirmBtn.disabled = true;
        confirmBtn.textContent = '转存中…';
        resultEl.hidden = false;
        resultEl.className = 'modal-result modal-result-loading';
        resultEl.textContent = '正在转存 ' + fileIds.length + ' 个文件…';

        try {
            var res = await fetch(SHARE_API + '/save', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    share_id: shareId,
                    file_ids: fileIds,
                    target_dir: targetDir
                })
            });
            var data = await res.json();
            if (!res.ok) {
                resultEl.className = 'modal-result modal-result-error';
                resultEl.textContent = '转存失败: ' + (data.message || data.error || 'HTTP ' + res.status);
                return;
            }
            resultEl.className = 'modal-result modal-result-success';
            resultEl.textContent = '转存完成: 成功 ' + data.success + ' 个, 失败 ' + data.fail + ' 个';
        } catch (e) {
            resultEl.className = 'modal-result modal-result-error';
            resultEl.textContent = '网络错误: ' + e.message;
        } finally {
            confirmBtn.disabled = false;
            confirmBtn.textContent = '确认转存';
        }
    }

    // === 事件绑定 ===

    function bindEvents() {
        // 整体下载按钮
        document.getElementById('share-download-btn').addEventListener('click', startDownload);

        // 顶部预览按钮（单文件分享）
        document.getElementById('share-preview-btn').addEventListener('click', function (e) {
            var btn = e.currentTarget;
            previewFile(btn.dataset.path || '', btn.dataset.name || '');
        });

        // 批量下载
        document.getElementById('batch-download-btn').addEventListener('click', batchDownload);

        // 全选复选框
        document.getElementById('select-all-cb').addEventListener('change', function (e) {
            var cbs = document.querySelectorAll('.tree-cb');
            cbs.forEach(function (cb) {
                cb.checked = e.target.checked;
                if (e.target.checked) {
                    selectedFiles.add(cb.dataset.path);
                } else {
                    selectedFiles.delete(cb.dataset.path);
                }
            });
            updateBatchButton();
        });

        // 文件列表事件委托（目录点击、文件预览、文件下载、复选框）
        document.getElementById('share-filelist').addEventListener('click', function (e) {
            // 目录点击
            var dirRow = e.target.closest('.tree-dir');
            if (dirRow) {
                loadDir(dirRow.dataset.dirPath);
                return;
            }
            // 文件预览按钮
            if (e.target.classList.contains('tree-preview-btn')) {
                previewFile(e.target.dataset.path, e.target.dataset.name);
                return;
            }
            // 文件下载按钮
            if (e.target.classList.contains('tree-download-btn')) {
                downloadFile(e.target.dataset.path);
                return;
            }
        });

        // 复选框变化
        document.getElementById('share-filelist').addEventListener('change', function (e) {
            if (e.target.classList.contains('tree-cb')) {
                if (e.target.checked) {
                    selectedFiles.add(e.target.dataset.path);
                } else {
                    selectedFiles.delete(e.target.dataset.path);
                }
                updateBatchButton();
            }
        });

        // 面包屑导航
        document.getElementById('share-breadcrumb').addEventListener('click', function (e) {
            if (e.target.classList.contains('crumb')) {
                loadDir(e.target.dataset.path || '');
            }
        });

        // 转存按钮
        document.getElementById('save-to-my-files-btn').addEventListener('click', openSaveModal);
        document.getElementById('save-cancel-btn').addEventListener('click', function () {
            document.getElementById('save-modal').hidden = true;
        });
        document.getElementById('save-confirm-btn').addEventListener('click', confirmSave);

        // 预览 Modal 关闭：关闭按钮、backdrop 点击、ESC 键
        document.getElementById('preview-close').addEventListener('click', closePreview);
        document.querySelector('#preview-modal .modal-backdrop').addEventListener('click', closePreview);
        document.addEventListener('keydown', function (e) {
            if (e.key === 'Escape' && !document.getElementById('preview-modal').hidden) {
                closePreview();
            }
        });
    }

    // === 密码验证 ===

    /** 提交访问密码，验证成功后重新加载分享内容 */
    async function submitPassword(e) {
        e.preventDefault();
        var pwdInput = document.getElementById('share-lock-password');
        var errEl = document.getElementById('share-lock-error');
        var submitBtn = document.getElementById('share-lock-submit');
        var pwd = pwdInput.value;

        if (!pwd) {
            errEl.hidden = false;
            errEl.textContent = '请输入密码';
            return;
        }

        submitBtn.disabled = true;
        submitBtn.textContent = '验证中…';
        errEl.hidden = true;

        try {
            var res = await fetch(API_BASE + shareId + '/auth', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ password: pwd })
            });
            if (res.status === 401) {
                errEl.hidden = false;
                errEl.textContent = '密码错误，请重试';
                pwdInput.select();
                return;
            }
            if (!res.ok) {
                var data = await res.json().catch(function () { return {}; });
                errEl.hidden = false;
                errEl.textContent = data.message || '验证失败 (HTTP ' + res.status + ')';
                return;
            }
            // 验证成功，重新加载分享内容（此时会带上下载 token）
            showLoading();
            await loadShare();
        } catch (e) {
            errEl.hidden = false;
            errEl.textContent = '网络错误: ' + e.message;
        } finally {
            submitBtn.disabled = false;
            submitBtn.textContent = '验证';
        }
    }

    /** 加载分享信息（提取自 init，供密码验证后复用） */
    async function loadShare() {
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
            // 保存下载 token（无密码或已认证时后端才返回）
            downloadToken = info.download_token || '';
            // 有密码且未获得 token → 显示密码门
            if (info.has_password && !downloadToken) {
                showLock();
                return;
            }
            showContent(info);
            // 异步检查登录状态（不阻塞渲染）
            checkLogin();
        } catch (e) {
            showError('网络错误', '无法连接到服务器，请稍后重试。');
        }
    }

    // === 初始化 ===

    async function init() {
        if (!shareId) {
            showError('链接无效', '分享 ID 缺失，请检查链接是否完整。');
            return;
        }

        showLoading();
        bindEvents();

        // 绑定密码门表单提交
        var lockForm = document.getElementById('share-lock-form');
        if (lockForm) lockForm.addEventListener('submit', submitPassword);

        await loadShare();
    }

    init();
})();
