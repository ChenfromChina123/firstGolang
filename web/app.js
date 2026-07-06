/**
 * FileSync 前端逻辑
 * 纯 JS 实现：分片上传、断点续传、文件列表、下载、冲突处理
 * 所有业务方法在后端，前端仅做页面展示与 API 调用（规则15）
 */

(function () {
    'use strict';

    // === 配置 ===
    const API = {
        init: '/api/upload/init',
        chunk: '/api/upload/chunk',
        status: '/api/upload/status',
        complete: '/api/upload/complete',
        files: '/api/files',
        filesMkdir: '/api/files/mkdir',
        filesRename: '/api/files/rename',
        filesMoveDir: '/api/files/move-dir',
        filesBatchDelete: '/api/files/batch-delete',
        download: '/api/download',
        downloadDir: '/api/download/dir',
        health: '/api/health',
        me: '/api/me',
        logout: '/api/logout',
        settings: '/api/settings',
        share: '/api/share',
    };

    // === 认证：路由守卫 + 401 拦截 ===

    /**
     * 跳转到登录页（带 redirect 参数，登录后跳回当前页）
     * @param {string} [reason] - 跳转原因（用于登录页提示，可选）
     */
    function redirectToLogin(reason) {
        const current = window.location.pathname + window.location.search;
        const url = '/web/login.html?redirect=' + encodeURIComponent(current);
        window.location.href = url;
    }

    /**
     * 带 401 拦截的 fetch 封装。
     * 收到 401 时自动跳转登录页，避免用户在 token 过期后继续操作无响应。
     * 其余行为与原生 fetch 一致。
     */
    function apiFetch(input, init) {
        return fetch(input, init).then(res => {
            if (res.status === 401) {
                redirectToLogin('session_expired');
            }
            return res;
        });
    }

    /**
     * 检查登录状态：调用 /api/me
     * - 401：立即跳转登录页（路由守卫）
     * - 成功：显示用户名和角色，显示登出按钮
     */
    async function checkAuth() {
        try {
            const res = await fetch(API.me, { credentials: 'same-origin' });
            if (res.status === 401) {
                redirectToLogin('not_authenticated');
                return false;
            }
            if (!res.ok) {
                toast('获取用户信息失败: HTTP ' + res.status, 'err');
                return false;
            }
            const user = await res.json();
            const infoEl = document.getElementById('user-info');
            const nameEl = document.getElementById('user-name');
            const roleEl = document.getElementById('user-role');
            const logoutBtn = document.getElementById('logout-btn');
            if (infoEl && nameEl) {
                nameEl.textContent = user.username || '—';
                if (roleEl && user.role) roleEl.textContent = user.role;
                infoEl.hidden = false;
            }
            if (logoutBtn) logoutBtn.hidden = false;
            return true;
        } catch (e) {
            // 网络错误等：不跳转，仅提示（可能是后端未启动）
            console.error('checkAuth error:', e);
            return false;
        }
    }

    /** 登出：调用 /api/logout 清除 Cookie，跳转登录页 */
    async function logout() {
        try {
            await fetch(API.logout, {
                method: 'POST',
                credentials: 'same-origin',
            });
        } catch (e) {
            // 忽略网络错误，仍然跳转登录页
        }
        window.location.href = '/web/login.html';
    }

    // === 工具函数 ===

    /** 格式化文件大小为人类可读字符串 */
    function fmtSize(bytes) {
        if (bytes < 1024) return bytes + ' B';
        if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
        if (bytes < 1073741824) return (bytes / 1048576).toFixed(2) + ' MB';
        return (bytes / 1073741824).toFixed(2) + ' GB';
    }

    /** 格式化时间戳为 YYYY-MM-DD HH:mm */
    function fmtDate(iso) {
        if (!iso) return '—';
        const d = new Date(iso);
        const pad = n => String(n).padStart(2, '0');
        return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
    }

    /** 简单 SHA-256 计算（用于断点续传标识，失败时降级为 name+size）
     *  注：file.slice().arrayBuffer() 与 crypto.subtle.digest 本身异步，
     *  但对 8MB 数据计算仍可能阻塞主线程几百毫秒。在两个耗时阶段之间
     *  插入 setTimeout(0) 让出主线程，让 UI 有机会响应。 */
    async function calcFileHash(file) {
        try {
            const buf = await file.slice(0, Math.min(file.size, 8 * 1024 * 1024)).arrayBuffer();
            // 让出主线程一次，避免 8MB ArrayBuffer 切片阻塞 UI 渲染
            await new Promise(r => setTimeout(r, 0));
            const hashBuf = await crypto.subtle.digest('SHA-256', buf);
            return Array.from(new Uint8Array(hashBuf)).slice(0, 16).map(b => b.toString(16).padStart(2, '0')).join('');
        } catch (e) {
            return file.name + '_' + file.size;
        }
    }

    /** Toast 提示 */
    function toast(msg, type = '') {
        const wrap = document.getElementById('toast-wrap');
        const el = document.createElement('div');
        el.className = 'toast ' + type;
        el.textContent = msg;
        wrap.appendChild(el);
        setTimeout(() => {
            el.style.opacity = '0';
            el.style.transform = 'translateX(20px)';
            el.style.transition = 'all 0.25s';
            setTimeout(() => el.remove(), 250);
        }, 3000);
    }

    /** sleep 工具 */
    const sleep = ms => new Promise(r => setTimeout(r, ms));

    // === 健康检查 ===

    /** 轮询服务健康状态，更新顶部状态栏 */
    async function checkHealth() {
        const badge = document.getElementById('health-badge');
        const dot = document.getElementById('health-dot');
        const text = document.getElementById('health-text');
        const meta = document.getElementById('health-meta');
        try {
            const res = await fetch(API.health);
            const data = await res.json();
            const healthy = data.healthy === true || data.status === 'ok';
            badge.className = 'health ' + (healthy ? 'healthy' : 'unhealthy');
            text.textContent = healthy ? '服务正常' : '服务异常';
            if (data.fail_count !== undefined) {
                meta.textContent = `fail=${data.fail_count} · ${data.ping_interval || ''}`;
            } else if (data.redis === 'disabled') {
                meta.textContent = 'redis=off';
            }
        } catch (e) {
            badge.className = 'health unhealthy';
            text.textContent = '连接失败';
            meta.textContent = '';
        }
    }

    // === 上传管理 ===

    /**
     * 单个文件的上传任务
     * 支持：分片上传、断点续传、冲突处理、并发控制
     */
    class UploadTask {
        constructor(file, opts) {
            this.file = file;
            this.chunkSize = opts.chunkSize;
            this.concurrency = opts.concurrency;
            this.strategy = opts.strategy || null; // 'skip' | 'overwrite' | 'rename' | null
            this.targetDir = opts.targetDir || ''; // 目标目录前缀（如 "docs/"）
            this.sessionId = null;
            this.totalChunks = 0;
            this.received = new Set(); // 已上传分片索引
            this.uploaded = 0;
            this.status = 'pending'; // pending | uploading | done | error | paused
            this.error = null;
            this.dom = null;
            this._domUpdatePending = false; // requestAnimationFrame 节流标志
        }

        /** 构造上传到后端的完整文件名（含目录前缀） */
        getUploadFilename() {
            return this.targetDir ? this.targetDir + this.file.name : this.file.name;
        }

        /** 获取用于 UI 展示的文件名（含目录前缀） */
        getDisplayName() {
            return this.getUploadFilename();
        }

        /** 初始化上传 session，处理冲突 */
        async init() {
            const fileHash = await calcFileHash(this.file);
            // force/rename 通过 query 参数传递（后端 fastQueryParam 解析）
            let url = API.init;
            if (this.strategy === 'overwrite') url += '?force=true';
            if (this.strategy === 'rename') url += '?rename=true';

            // 文件元信息通过 JSON body 传递（后端 InitUploadRequest 解析）
            const uploadName = this.getUploadFilename();
            const res = await apiFetch(url, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    filename: uploadName,
                    file_size: this.file.size,
                    chunk_size: this.chunkSize,
                    storage: 'local',
                    file_hash: fileHash,
                }),
            });
            if (res.status === 409) {
                // 冲突：需要用户决策
                const data = await res.json().catch(() => ({}));
                const strategy = await askConflict(uploadName, data);
                if (strategy === 'skip') {
                    this.status = 'done';
                    this.error = '已跳过';
                    return false;
                }
                this.strategy = strategy;
                return this.init(); // 重试
            }
            if (res.status === 429) {
                throw new Error('服务端限流，请稍后重试');
            }
            if (!res.ok) {
                throw new Error(`init 失败: HTTP ${res.status}`);
            }
            const data = await res.json();
            this.sessionId = data.session_id;
            this.totalChunks = data.total_chunks;
            return true;
        }

        /** 查询已上传分片（断点续传） */
        async checkResumable() {
            if (!this.sessionId) return;
            try {
                const res = await apiFetch(`${API.status}?session_id=${this.sessionId}`);
                if (!res.ok) return;
                const data = await res.json();
                if (data.received_chunks && Array.isArray(data.received_chunks)) {
                    this.received = new Set(data.received_chunks);
                    this.uploaded = this.received.size * this.chunkSize;
                    if (this.uploaded > this.file.size) this.uploaded = this.file.size;
                }
            } catch (e) { /* 忽略，全量重传 */ }
        }

        /** 上传单个分片 */
        async uploadChunk(idx) {
            const start = idx * this.chunkSize;
            const end = Math.min(start + this.chunkSize, this.file.size);
            const blob = this.file.slice(start, end);

            const form = new FormData();
            form.append('session_id', this.sessionId);
            form.append('chunk_index', idx);
            form.append('chunk_data', blob);

            const res = await apiFetch(API.chunk, { method: 'POST', body: form });
            if (!res.ok) throw new Error(`chunk ${idx} 失败: HTTP ${res.status}`);
            this.received.add(idx);
            // 用 received.size 计算总上传量，而非 (idx+1)*chunkSize。
            // 并发上传时 chunks 乱序完成，(idx+1) 会导致进度条回弹
            // （如 chunk 2 先完成时 uploaded=3*chunkSize，之后 chunk 0 完成时 uploaded=1*chunkSize）。
            // received.size 只增不减，保证进度条单调递增。
            this.uploaded = Math.min(this.received.size * this.chunkSize, this.file.size);
            this.updateDom();
        }

        /** 并发上传所有缺失分片 */
        async uploadAll(onProgress) {
            const missing = [];
            for (let i = 0; i < this.totalChunks; i++) {
                if (!this.received.has(i)) missing.push(i);
            }
            if (missing.length === 0) return;

            // 并发控制：限制同时进行的分片上传数
            let cursor = 0;
            const workers = [];
            const worker = async () => {
                while (cursor < missing.length) {
                    const idx = missing[cursor++];
                    await this.uploadChunk(idx);
                }
            };
            for (let i = 0; i < this.concurrency; i++) {
                workers.push(worker());
            }
            await Promise.all(workers);
        }

        /** 完成上传，合并分片 */
        async complete() {
            const res = await apiFetch(API.complete, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ session_id: this.sessionId }),
            });
            if (!res.ok) {
                const txt = await res.text().catch(() => '');
                throw new Error(`complete 失败: HTTP ${res.status} ${txt}`);
            }
            this.status = 'done';
            this.uploaded = this.file.size;
            this.updateDom();
        }

        /** 完整上传流程 */
        async run() {
            try {
                this.status = 'uploading';
                this.updateDom();
                const ok = await this.init();
                if (!ok) return; // 跳过
                await this.checkResumable();
                this.updateDom();
                await this.uploadAll();
                await this.complete();
                toast(`「${this.getDisplayName()}」上传完成`, 'ok');
            } catch (e) {
                this.status = 'error';
                this.error = e.message;
                this.updateDom();
                toast(`「${this.getDisplayName()}」上传失败: ${e.message}`, 'err');
            }
        }

        /** 创建队列 DOM 节点 */
        createDom() {
            const li = document.createElement('li');
            li.className = 'queue-item uploading';
            li.innerHTML = `
                <div class="qi-head">
                    <span class="qi-name">${escapeHtml(this.getDisplayName())}</span>
                    <span class="qi-status uploading">上传中</span>
                </div>
                <div class="qi-progress"><div class="qi-progress-fill" style="width:0%"></div></div>
                <div class="qi-meta">
                    <span class="qi-progress-text">0% · 0 / ${this.file.size ? fmtSize(this.file.size) : '?'}</span>
                    <span class="qi-actions">
                        <button class="qi-cancel">取消</button>
                    </span>
                </div>
            `;
            this.dom = li;
            return li;
        }

        /** 更新队列 DOM 进度
         *  上传过程中每个 chunk 上传成功都会调用，大文件会产生数千次调用。
         *  使用 requestAnimationFrame 节流：同一帧内多次调用只渲染一次，
         *  避免频繁 DOM 操作导致主线程卡顿。
         *  注意：done/error 是终态，必须立即渲染以保证用户看到最终状态。 */
        updateDom() {
            if (!this.dom) return;
            // 终态立即更新，确保用户看到最终状态
            if (this.status === 'done' || this.status === 'error') {
                this._domUpdatePending = false;
                this._renderDom();
                return;
            }
            // uploading 状态：rAF 节流，同帧多次调用合并为一次渲染
            if (this._domUpdatePending) return;
            this._domUpdatePending = true;
            requestAnimationFrame(() => {
                this._domUpdatePending = false;
                this._renderDom();
            });
        }

        /** 实际执行 DOM 渲染（私有方法） */
        _renderDom() {
            if (!this.dom) return;
            const pct = this.file.size > 0 ? (this.uploaded / this.file.size * 100) : 0;
            const fill = this.dom.querySelector('.qi-progress-fill');
            const text = this.dom.querySelector('.qi-progress-text');
            const status = this.dom.querySelector('.qi-status');
            fill.style.width = pct.toFixed(1) + '%';
            text.textContent = `${pct.toFixed(1)}% · ${fmtSize(this.uploaded)} / ${fmtSize(this.file.size)}`;
            this.dom.className = 'queue-item ' + this.status;
            if (this.status === 'done') {
                status.textContent = this.error || '完成';
                status.className = 'qi-status done';
            } else if (this.status === 'error') {
                status.textContent = '失败';
                status.className = 'qi-status error';
            } else if (this.status === 'uploading') {
                status.textContent = '上传中';
                status.className = 'qi-status uploading';
            }
        }
    }

    /** HTML 转义，防止 XSS */
    function escapeHtml(s) {
        return String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
    }

    // === 冲突处理 ===

    let conflictResolver = null;
    let globalStrategy = null; // "应用到后续所有冲突" 选定后缓存

    /** 弹出冲突对话框，返回用户选择的策略 */
    function askConflict(filename, data) {
        if (globalStrategy) return Promise.resolve(globalStrategy);
        document.getElementById('conflict-name').textContent = filename;
        const modal = document.getElementById('conflict-modal');
        modal.hidden = false;
        return new Promise(resolve => {
            conflictResolver = (strategy) => {
                modal.hidden = true;
                if (document.getElementById('apply-all').checked) {
                    globalStrategy = strategy;
                }
                resolve(strategy);
            };
        });
    }

    // === 文件列表（树形：路径枚举方案） ===

    // 当前所在目录路径（末尾带 /，空字符串表示根目录）
    let currentPath = '';

    /**
     * 从扁平文件列表中提取当前目录的直接子项。
     * 路径枚举方案：filename 中的 / 作为虚拟目录分隔符。
     * @param {Array} files - 后端返回的扁平文件列表（已按 prefix 过滤）
     * @param {string} prefix - 当前目录前缀（如 "docs/" 或 ""）
     * @returns {{dirs: Map<string, number>, files: Array}} 子目录名→文件数，子文件列表
     */
    function buildChildren(files, prefix) {
        const dirs = new Map();
        const fileList = [];
        for (const f of files) {
            const rel = prefix ? f.filename.slice(prefix.length) : f.filename;
            if (!rel) continue;
            const slashIdx = rel.indexOf('/');
            if (slashIdx === -1) {
                // 根目录下的文件：跳过 .keep 占位文件
                if (rel === '.keep') continue;
                fileList.push(f);
            } else {
                // 子目录项：识别目录名（含 .keep 占位文件也要识别目录存在）
                const dirName = rel.slice(0, slashIdx);
                const rest = rel.slice(slashIdx + 1);
                // 只有非 .keep 文件才计入目录的文件数
                if (rest && rest !== '.keep' && !rest.endsWith('/.keep')) {
                    dirs.set(dirName, (dirs.get(dirName) || 0) + 1);
                } else if (!dirs.has(dirName)) {
                    // .keep 占位文件：只标记目录存在，文件数为 0
                    dirs.set(dirName, 0);
                }
            }
        }
        return { dirs, files: fileList };
    }

    /** 渲染面包屑导航（根目录 > docs > sub） */
    function renderBreadcrumb() {
        const bc = document.getElementById('breadcrumb');
        const parts = currentPath.split('/').filter(Boolean);
        let html = `<span class="crumb${parts.length === 0 ? ' current' : ''}" data-path="">根目录</span>`;
        let acc = '';
        for (let i = 0; i < parts.length; i++) {
            acc += parts[i] + '/';
            const isLast = i === parts.length - 1;
            html += `<span class="crumb-sep">/</span>`;
            html += `<span class="crumb${isLast ? ' current' : ''}" data-path="${escapeHtml(acc)}">${escapeHtml(parts[i])}</span>`;
        }
        bc.innerHTML = html;
        bc.querySelectorAll('.crumb').forEach(el => {
            el.addEventListener('click', () => {
                currentPath = el.dataset.path || '';
                loadFiles();
            });
        });
    }

    /** 加载并渲染当前目录的子项（目录 + 文件） */
    async function loadFiles() {
        const tree = document.getElementById('file-tree');
        renderBreadcrumb();
        // 同步上传目标目录显示：跟随用户当前所在目录（currentPath）
        const targetDirEl = document.getElementById('target-dir');
        if (targetDirEl) targetDirEl.textContent = currentPath || '根目录';
        tree.innerHTML = '<div class="tree-empty">加载中…</div>';
        try {
            const url = currentPath
                ? `${API.files}?prefix=${encodeURIComponent(currentPath)}`
                : API.files;
            const res = await apiFetch(url);
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const files = await res.json();
            if (!files || files.length === 0) {
                tree.innerHTML = '<div class="tree-empty">暂无文件，请上传</div>';
                return;
            }
            const { dirs, files: fileList } = buildChildren(files, currentPath);
            renderTree(dirs, fileList);
        } catch (e) {
            tree.innerHTML = `<div class="tree-empty" style="color:var(--err)">加载失败: ${escapeHtml(e.message)}</div>`;
        }
    }

    /**
     * 渲染树形列表：子目录在前，文件在后
     * @param {Map<string, number>} dirs - 子目录名→文件数
     * @param {Array} files - 子文件列表
     */
    function renderTree(dirs, files) {
        const tree = document.getElementById('file-tree');
        const items = [];
        for (const [name, count] of dirs) {
            const dirPrefix = currentPath + name + '/';
            items.push(`
                <div class="tree-row dir" data-dir="${escapeHtml(name)}" tabindex="0">
                    <span class="tree-icon" aria-hidden="true">▸</span>
                    <span class="tree-name">${escapeHtml(name)}/</span>
                    <span class="tree-meta">${count} 个文件</span>
                    <span class="tree-meta"></span>
                    <span class="tree-meta"></span>
                    <span class="tree-ops">
                        <button class="op-btn" data-action="zip" data-prefix="${escapeHtml(dirPrefix)}" title="打包下载 ZIP">ZIP</button>
                        <button class="op-btn" data-action="share-dir" data-prefix="${escapeHtml(dirPrefix)}" data-name="${escapeHtml(name)}" title="分享目录">分享</button>
                        <button class="op-btn" data-action="move-dir" data-prefix="${escapeHtml(dirPrefix)}" data-name="${escapeHtml(name)}" title="移动目录">移动</button>
                        <button class="op-btn danger" data-action="rmdir" data-prefix="${escapeHtml(dirPrefix)}" data-name="${escapeHtml(name)}" title="删除目录">删除</button>
                    </span>
                </div>
            `);
        }
        for (const f of files) {
            items.push(`
                <div class="tree-row file" data-id="${escapeHtml(f.id)}" data-filename="${escapeHtml(f.filename)}">
                    <input type="checkbox" class="file-check" data-id="${escapeHtml(f.id)}" aria-label="选择文件">
                    <span class="tree-name">${escapeHtml(f.filename.split('/').pop())}</span>
                    <span class="tree-meta num">${fmtSize(f.size)}</span>
                    <span class="tree-meta">${fmtDate(f.created_at)}</span>
                    <span class="tree-meta"><span class="fstore">${escapeHtml(f.storage_type || 'local')}</span></span>
                    <span class="tree-ops">
                        <a class="op-btn" href="${API.download}/${f.id}" download="${escapeHtml(f.filename)}" title="下载文件">下载</a>
                        <button class="op-btn" data-action="share-file" data-id="${escapeHtml(f.id)}" data-name="${escapeHtml(f.filename)}" title="分享文件">分享</button>
                        <button class="op-btn" data-action="rename" data-id="${escapeHtml(f.id)}" data-filename="${escapeHtml(f.filename)}" title="重命名/移动">重命名</button>
                        <button class="op-btn danger" data-action="rmfile" data-id="${escapeHtml(f.id)}" data-name="${escapeHtml(f.filename.split('/').pop())}" title="删除文件">删除</button>
                    </span>
                </div>
            `);
        }
        tree.innerHTML = items.join('');

        // 目录行：点击进入目录（操作按钮 stopPropagation）
        tree.querySelectorAll('.tree-row.dir').forEach(el => {
            const enter = () => {
                currentPath += el.dataset.dir + '/';
                loadFiles();
            };
            el.addEventListener('click', e => {
                if (e.target.closest('.tree-ops')) return; // 点击操作按钮不进入目录
                enter();
            });
            el.addEventListener('keydown', e => {
                if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); enter(); }
            });
        });

        // 操作按钮事件
        tree.querySelectorAll('.op-btn[data-action]').forEach(btn => {
            btn.addEventListener('click', e => {
                e.stopPropagation();
                const action = btn.dataset.action;
                if (action === 'zip') {
                    window.location.href = `${API.downloadDir}?prefix=${encodeURIComponent(btn.dataset.prefix)}`;
                } else if (action === 'move-dir') {
                    openMoveDirDialog(btn.dataset.prefix, btn.dataset.name);
                } else if (action === 'rmdir') {
                    confirmDeleteDir(btn.dataset.prefix, btn.dataset.name);
                } else if (action === 'rmfile') {
                    confirmDeleteFile(btn.dataset.id, btn.dataset.name);
                } else if (action === 'rename') {
                    openRenameDialog(btn.dataset.id, btn.dataset.filename);
                } else if (action === 'share-dir') {
                    openShareDialog(null, btn.dataset.prefix, btn.dataset.name || '目录', 'dir');
                } else if (action === 'share-file') {
                    openShareDialog(btn.dataset.id, null, btn.dataset.name || '文件', 'file');
                }
            });
        });

        // checkbox 多选：change 时更新批量操作栏
        tree.querySelectorAll('.file-check').forEach(cb => {
            cb.addEventListener('change', updateBatchOps);
            cb.addEventListener('click', e => e.stopPropagation()); // 防止点击 checkbox 触发目录进入
        });
    }

    // === 分享功能 ===

    /** 打开分享对话框
     * @param {string|null} fileId - 文件 ID（文件分享时非空）
     * @param {string|null} dirPrefix - 目录前缀（目录分享时非空）
     * @param {string} displayName - 显示名称
     * @param {string} shareType - "file" | "dir"
     */
    function openShareDialog(fileId, dirPrefix, displayName, shareType) {
        const modal = document.getElementById('share-modal');
        const nameEl = document.getElementById('share-name');
        const expirySelect = document.getElementById('share-expiry');
        const createBtn = document.getElementById('share-create-btn');
        const resultEl = document.getElementById('share-result');
        const linkInput = document.getElementById('share-link');
        const copyBtn = document.getElementById('share-copy-btn');

        nameEl.textContent = displayName;
        expirySelect.value = '0'; // 默认永久
        resultEl.hidden = true;
        linkInput.value = '';
        modal.hidden = false;

        // 创建分享按钮
        const onCreate = async () => {
            const expiresIn = parseInt(expirySelect.value, 10);
            const body = { share_type: shareType, expires_in: expiresIn };
            if (shareType === 'file') body.file_id = fileId;
            else body.dir_prefix = dirPrefix;

            createBtn.disabled = true;
            createBtn.textContent = '创建中...';
            try {
                const res = await apiFetch(API.share, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(body),
                });
                if (!res.ok) {
                    const data = await res.json().catch(() => ({}));
                    throw new Error(data.message || `HTTP ${res.status}`);
                }
                const data = await res.json();
                const fullURL = window.location.origin + data.url;
                linkInput.value = fullURL;
                resultEl.hidden = false;
                toast('分享链接已创建', 'ok');
            } catch (e) {
                toast('创建分享失败: ' + e.message, 'err');
            } finally {
                createBtn.disabled = false;
                createBtn.textContent = '创建分享';
            }
        };

        // 复制链接按钮
        const onCopy = () => {
            if (!linkInput.value) return;
            linkInput.select();
            document.execCommand('copy');
            toast('链接已复制到剪贴板', 'ok');
        };

        // 清理旧事件监听器（避免重复绑定）
        const newCreateBtn = createBtn.cloneNode(true);
        createBtn.parentNode.replaceChild(newCreateBtn, createBtn);
        newCreateBtn.addEventListener('click', onCreate);

        const newCopyBtn = copyBtn.cloneNode(true);
        copyBtn.parentNode.replaceChild(newCopyBtn, copyBtn);
        newCopyBtn.addEventListener('click', onCopy);
    }

    /** 打开分享管理对话框：列出当前用户的所有分享 */
    async function openShareManageDialog() {
        const modal = document.getElementById('share-manage-modal');
        const listEl = document.getElementById('share-manage-list');
        modal.hidden = false;
        listEl.innerHTML = '<div class="loading">加载中...</div>';

        try {
            const res = await apiFetch(API.share);
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            const shares = await res.json();
            if (!shares || shares.length === 0) {
                listEl.innerHTML = '<div class="empty">暂无分享</div>';
                return;
            }
            listEl.innerHTML = shares.map(s => {
                const created = new Date(s.created_at * 1000).toLocaleString('zh-CN');
                const expiry = s.expires_at ? new Date(s.expires_at * 1000).toLocaleString('zh-CN') : '永久';
                const status = s.is_expired ? '<span class="share-status expired">已过期</span>' :
                              (s.is_active ? '<span class="share-status active">有效</span>' : '<span class="share-status">已禁用</span>');
                const fullURL = window.location.origin + s.url;
                return `
                    <div class="share-item" data-id="${escapeHtml(s.id)}">
                        <div class="share-item-head">
                            <span class="share-item-name">${escapeHtml(s.name)}</span>
                            <span class="share-item-type">${s.share_type === 'file' ? '文件' : '目录'}</span>
                            ${status}
                        </div>
                        <div class="share-item-meta">
                            <span>创建: ${created}</span>
                            <span>过期: ${expiry}</span>
                            <span>下载: ${s.download_count} 次</span>
                        </div>
                        <div class="share-item-link">
                            <input type="text" value="${escapeHtml(fullURL)}" readonly class="share-link-input">
                            <button class="op-btn copy-share-link" data-url="${escapeHtml(fullURL)}">复制</button>
                            <button class="op-btn danger delete-share" data-id="${escapeHtml(s.id)}">删除</button>
                        </div>
                    </div>
                `;
            }).join('');

            // 绑定复制和删除按钮
            listEl.querySelectorAll('.copy-share-link').forEach(btn => {
                btn.addEventListener('click', () => {
                    const url = btn.dataset.url;
                    const input = btn.previousElementSibling;
                    input.select();
                    document.execCommand('copy');
                    toast('链接已复制', 'ok');
                });
            });
            listEl.querySelectorAll('.delete-share').forEach(btn => {
                btn.addEventListener('click', async () => {
                    const id = btn.dataset.id;
                    if (!confirm('确认删除此分享？')) return;
                    try {
                        const res = await apiFetch(`${API.share}/${id}`, { method: 'DELETE' });
                        if (!res.ok) throw new Error(`HTTP ${res.status}`);
                        toast('分享已删除', 'ok');
                        btn.closest('.share-item').remove();
                    } catch (e) {
                        toast('删除失败: ' + e.message, 'err');
                    }
                });
            });
        } catch (e) {
            listEl.innerHTML = `<div class="error">加载失败: ${escapeHtml(e.message)}</div>`;
        }
    }

    // === 文件/目录操作 ===

    /** 删除文件确认 */
    function confirmDeleteFile(fileId, filename) {
        openConfirmDialog(`确认删除文件「${filename}」？此操作不可恢复。`, async () => {
            try {
                const res = await apiFetch(`${API.files}/${fileId}`, { method: 'DELETE' });
                if (!res.ok) throw new Error('HTTP ' + res.status);
                toast(`「${filename}」已删除`, 'ok');
                loadFiles();
            } catch (e) {
                toast(`删除失败: ${e.message}`, 'err');
            }
        });
    }

    /** 删除目录确认（递归删除所有文件） */
    function confirmDeleteDir(prefix, dirName) {
        openConfirmDialog(`确认删除目录「${dirName}」及其所有内容？此操作不可恢复。`, async () => {
            try {
                const res = await apiFetch(`${API.files}?prefix=${encodeURIComponent(prefix)}`, { method: 'DELETE' });
                if (!res.ok) throw new Error('HTTP ' + res.status);
                const data = await res.json().catch(() => ({}));
                toast(`目录「${dirName}」已删除（${data.files_deleted || 0} 个文件）`, 'ok');
                loadFiles();
            } catch (e) {
                toast(`删除目录失败: ${e.message}`, 'err');
            }
        });
    }

    /** 打开重命名/移动对话框 */
    function openRenameDialog(fileId, currentFilename) {
        const modal = document.getElementById('rename-modal');
        const input = document.getElementById('rename-input');
        input.value = currentFilename;
        modal.hidden = false;
        input.focus();
        input.select();

        const confirmBtn = document.getElementById('rename-confirm');
        const cancelBtn = document.getElementById('rename-cancel');
        const submit = async () => {
            const newFilename = input.value.trim();
            if (!newFilename) { toast('文件名不能为空', 'err'); return; }
            if (newFilename === currentFilename) { cleanup(); return; }
            try {
                const res = await apiFetch(API.filesRename, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ id: fileId, new_filename: newFilename }),
                });
                if (res.status === 409) { toast('文件名已存在', 'err'); return; }
                if (res.status === 400) {
                    const txt = await res.text();
                    toast(`文件名非法: ${txt}`, 'err'); return;
                }
                if (!res.ok) throw new Error('HTTP ' + res.status);
                toast(`已重命名为「${newFilename}」`, 'ok');
                cleanup();
                loadFiles();
            } catch (e) {
                toast(`重命名失败: ${e.message}`, 'err');
            }
        };
        const cleanup = () => {
            modal.hidden = true;
            confirmBtn.onclick = null;
            cancelBtn.onclick = null;
            input.onkeydown = null;
        };
        cancelBtn.onclick = cleanup;
        confirmBtn.onclick = submit;
        // Enter 键提交，Esc 键取消
        input.onkeydown = e => {
            if (e.key === 'Enter') { e.preventDefault(); submit(); }
            else if (e.key === 'Escape') { e.preventDefault(); cleanup(); }
        };
    }

    /** 打开通用确认对话框 */
    function openConfirmDialog(message, onConfirm) {
        const modal = document.getElementById('confirm-modal');
        const msg = document.getElementById('confirm-message');
        const okBtn = document.getElementById('confirm-ok');
        const cancelBtn = document.getElementById('confirm-cancel');
        msg.textContent = message;
        modal.hidden = false;
        const cleanup = () => {
            modal.hidden = true;
            okBtn.onclick = null;
            cancelBtn.onclick = null;
        };
        cancelBtn.onclick = cleanup;
        okBtn.onclick = () => { cleanup(); onConfirm(); };
    }

    /** 新建目录 */
    async function mkdir() {
        const modal = document.getElementById('mkdir-modal');
        const input = document.getElementById('mkdir-input');
        input.value = currentPath; // 默认当前目录
        modal.hidden = false;
        input.focus();
        // 选中末尾，方便用户在当前目录下追加子目录名
        input.setSelectionRange(input.value.length, input.value.length);

        const confirmBtn = document.getElementById('mkdir-confirm');
        const cancelBtn = document.getElementById('mkdir-cancel');
        const submit = async () => {
            const rawPath = input.value.trim();
            if (!rawPath) { toast('目录名不能为空', 'err'); return; }
            try {
                const res = await apiFetch(API.filesMkdir, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ path: rawPath }),
                });
                if (res.status === 409) { toast('目录已存在', 'err'); return; }
                if (res.status === 400) {
                    const txt = await res.text();
                    toast(`目录名非法: ${txt}`, 'err'); return;
                }
                if (!res.ok) throw new Error('HTTP ' + res.status);
                toast(`目录「${rawPath}」已创建`, 'ok');
                cleanup();
                loadFiles();
            } catch (e) {
                toast(`新建目录失败: ${e.message}`, 'err');
            }
        };
        const cleanup = () => {
            modal.hidden = true;
            confirmBtn.onclick = null;
            cancelBtn.onclick = null;
            input.onkeydown = null;
        };
        cancelBtn.onclick = cleanup;
        confirmBtn.onclick = submit;
        // Enter 键提交，Esc 键取消
        input.onkeydown = e => {
            if (e.key === 'Enter') { e.preventDefault(); submit(); }
            else if (e.key === 'Escape') { e.preventDefault(); cleanup(); }
        };
    }

    /**
     * 打开移动目录对话框。
     * 后端 MoveDir handler 会批量更新目录下所有文件的 filename 前缀，
     * storage_path 不变（仅改虚拟路径），目标目录已有文件时返回 409 冲突。
     * @param {string} prefix - 源目录完整前缀（如 "docs/sub/"）
     * @param {string} dirName - 目录显示名（如 "sub"）
     */
    function openMoveDirDialog(prefix, dirName) {
        const modal = document.getElementById('move-dir-modal');
        const nameEl = document.getElementById('move-dir-name');
        const input = document.getElementById('move-dir-input');
        const confirmBtn = document.getElementById('move-dir-confirm');
        const cancelBtn = document.getElementById('move-dir-cancel');

        nameEl.textContent = dirName + '/';
        // 默认填入源目录去掉末尾 / 的路径，方便用户在原基础上修改
        input.value = prefix.replace(/\/$/, '');
        modal.hidden = false;
        input.focus();
        input.select();

        const submit = async () => {
            const rawPath = input.value.trim();
            if (!rawPath) { toast('目录路径不能为空', 'err'); return; }
            if (rawPath === prefix.replace(/\/$/, '')) { toast('新路径与原路径相同', 'err'); return; }
            try {
                const res = await apiFetch(API.filesMoveDir, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ old_prefix: prefix, new_prefix: rawPath }),
                });
                if (res.status === 409) {
                    const txt = await res.text().catch(() => '目标目录已有文件');
                    toast(`移动失败: ${txt}`, 'err');
                    return;
                }
                if (res.status === 404) { toast('源目录为空或不存在', 'err'); return; }
                if (res.status === 400) {
                    const txt = await res.text();
                    toast(`路径非法: ${txt}`, 'err'); return;
                }
                if (!res.ok) throw new Error('HTTP ' + res.status);
                const data = await res.json().catch(() => ({}));
                toast(`目录已移动到「${rawPath}」（成功 ${data.success || 0}，失败 ${data.fail || 0}）`, 'ok');
                cleanup();
                loadFiles();
            } catch (e) {
                toast(`移动目录失败: ${e.message}`, 'err');
            }
        };
        const cleanup = () => {
            modal.hidden = true;
            confirmBtn.onclick = null;
            cancelBtn.onclick = null;
            input.onkeydown = null;
        };
        cancelBtn.onclick = cleanup;
        confirmBtn.onclick = submit;
        input.onkeydown = e => {
            if (e.key === 'Enter') { e.preventDefault(); submit(); }
            else if (e.key === 'Escape') { e.preventDefault(); cleanup(); }
        };
    }

    /**
     * 更新批量操作栏状态。
     * 统计当前选中的 .file-check 数量，更新 batch-count 文本，
     * 选中数 > 0 时显示 batch-ops 栏，否则隐藏。
     * 每次 checkbox 状态变化时调用。
     */
    function updateBatchOps() {
        const checked = document.querySelectorAll('.file-check:checked');
        const bar = document.getElementById('batch-ops');
        const countEl = document.getElementById('batch-count');
        if (checked.length > 0) {
            bar.hidden = false;
            countEl.textContent = `已选 ${checked.length} 项`;
        } else {
            bar.hidden = true;
        }
    }

    /**
     * 批量删除选中文件。
     * 收集所有选中 checkbox 的 data-id，弹出确认对话框，
     * 确认后调用 POST /api/files/batch-delete。
     * 后端逐个删除（存储 + 数据库 + Redis），返回成功/失败计数。
     */
    function batchDelete() {
        const checked = document.querySelectorAll('.file-check:checked');
        if (checked.length === 0) { toast('请先选择文件', 'err'); return; }
        const ids = Array.from(checked).map(cb => cb.dataset.id);

        openConfirmDialog(`确认批量删除选中的 ${ids.length} 个文件？此操作不可恢复。`, async () => {
            try {
                const res = await apiFetch(API.filesBatchDelete, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ ids }),
                });
                if (!res.ok) throw new Error('HTTP ' + res.status);
                const data = await res.json().catch(() => ({}));
                toast(`批量删除完成（成功 ${data.success || 0}，失败 ${data.fail || 0}）`, 'ok');
                loadFiles();
            } catch (e) {
                toast(`批量删除失败: ${e.message}`, 'err');
            }
        });
    }

    // === 事件绑定 ===

    /** 从服务器拉取用户配置并覆盖本地（跨浏览器同步）
     *  仅当服务器有记录时才覆盖，避免覆盖用户刚做的本地修改。
     *  静默失败：网络错误不影响用户体验。 */
    async function syncSettingsFromServer(chunkSizeSelect, concurrencySelect) {
        try {
            const res = await apiFetch(API.settings);
            if (!res.ok) return;
            const s = await res.json();
            if (s.chunk_size) {
                chunkSizeSelect.value = s.chunk_size;
                localStorage.setItem('filesync:chunkSize', s.chunk_size);
            }
            if (s.concurrency) {
                concurrencySelect.value = s.concurrency;
                localStorage.setItem('filesync:concurrency', s.concurrency);
            }
        } catch (e) {
            // 静默失败：网络错误不影响用户体验
        }
    }

    /** 异步同步当前配置到服务器（跨浏览器同步）
     *  静默失败：网络错误不影响用户体验。 */
    async function syncSettingsToServer() {
        try {
            const chunkSize = parseInt(document.getElementById('chunk-size').value, 10);
            const concurrency = parseInt(document.getElementById('concurrency').value, 10);
            await apiFetch(API.settings, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ chunk_size: chunkSize, concurrency }),
            });
        } catch (e) {
            // 静默失败：网络错误不影响用户体验
        }
    }

    /** 绑定选择文件、拖拽上传、持久化配置等事件。
     *  选择文件按钮用 JS 触发 fileInput.click()，比 label[for] 更可靠（兼容所有浏览器）。
     *  拖拽绑定到整个 upload-panel（而非已移除的 dropzone）。
     *  分片大小和并发数持久化到 localStorage，下次打开保留用户选择。 */
    function bindEvents() {
        const uploadPanel = document.querySelector('.upload-panel');
        const fileInput = document.getElementById('file-input');

        // 选择文件按钮：JS 触发 fileInput.click()（移除 label[for] 避免双重触发）
        const pickBtn = document.getElementById('pick-btn');
        if (pickBtn) {
            pickBtn.addEventListener('click', () => {
                fileInput.click();
            });
        }

        // 拖拽：绑定到整个 upload-panel
        if (uploadPanel) {
            ['dragenter', 'dragover'].forEach(ev => {
                uploadPanel.addEventListener(ev, e => {
                    e.preventDefault();
                    uploadPanel.classList.add('dragover');
                });
            });
            ['dragleave', 'drop'].forEach(ev => {
                uploadPanel.addEventListener(ev, e => {
                    e.preventDefault();
                    // dragleave 时检查是否真的离开了面板（避免子元素进出误触发）
                    if (ev === 'dragleave' && e.relatedTarget && uploadPanel.contains(e.relatedTarget)) return;
                    uploadPanel.classList.remove('dragover');
                });
            });
            uploadPanel.addEventListener('drop', e => {
                const files = Array.from(e.dataTransfer.files);
                if (files.length) handleFiles(files);
            });
        }

        // 选择文件
        fileInput.addEventListener('change', e => {
            const files = Array.from(e.target.files);
            if (files.length) handleFiles(files);
            fileInput.value = ''; // 允许重复选择同一文件
        });

        // 持久化分片大小和并发数：localStorage 即时响应 + 服务器跨浏览器同步
        const chunkSizeSelect = document.getElementById('chunk-size');
        const concurrencySelect = document.getElementById('concurrency');
        // 1. 先读 localStorage 立即渲染（避免等待网络）
        const savedChunkSize = localStorage.getItem('filesync:chunkSize');
        const savedConcurrency = localStorage.getItem('filesync:concurrency');
        if (savedChunkSize) chunkSizeSelect.value = savedChunkSize;
        if (savedConcurrency) concurrencySelect.value = savedConcurrency;
        // 2. 后台从服务器拉取覆盖（跨浏览器同步）
        syncSettingsFromServer(chunkSizeSelect, concurrencySelect);
        // 3. 变更时同时写 localStorage 和服务器
        chunkSizeSelect.addEventListener('change', () => {
            localStorage.setItem('filesync:chunkSize', chunkSizeSelect.value);
            syncSettingsToServer();
        });
        concurrencySelect.addEventListener('change', () => {
            localStorage.setItem('filesync:concurrency', concurrencySelect.value);
            syncSettingsToServer();
        });

        // 冲突对话框按钮
        document.querySelectorAll('.opt-btn').forEach(btn => {
            btn.addEventListener('click', () => {
                if (conflictResolver) conflictResolver(btn.dataset.strategy);
            });
        });

        // 刷新文件列表
        document.getElementById('refresh-files').addEventListener('click', loadFiles);

        // 新建目录
        document.getElementById('mkdir-btn').addEventListener('click', mkdir);

        // 批量删除选中文件
        const batchDeleteBtn = document.getElementById('batch-delete-btn');
        if (batchDeleteBtn) {
            batchDeleteBtn.addEventListener('click', batchDelete);
        }

        // 分享管理
        const shareManageBtn = document.getElementById('share-manage-btn');
        if (shareManageBtn) {
            shareManageBtn.addEventListener('click', openShareManageDialog);
        }

        // 分享对话框关闭按钮
        const shareCloseBtn = document.getElementById('share-close-btn');
        if (shareCloseBtn) {
            shareCloseBtn.addEventListener('click', () => {
                document.getElementById('share-modal').hidden = true;
            });
        }
        const shareManageCloseBtn = document.getElementById('share-manage-close-btn');
        if (shareManageCloseBtn) {
            shareManageCloseBtn.addEventListener('click', () => {
                document.getElementById('share-manage-modal').hidden = true;
            });
        }

        // 清除已完成
        document.getElementById('clear-done').addEventListener('click', () => {
            document.querySelectorAll('.queue-item.done').forEach(el => el.remove());
            const list = document.getElementById('queue-list');
            if (!list.children.length) document.getElementById('queue').hidden = true;
        });

        // 登出按钮
        const logoutBtn = document.getElementById('logout-btn');
        if (logoutBtn) {
            logoutBtn.addEventListener('click', () => {
                logout();
            });
        }
    }

    /** 处理选择的文件列表，加入上传队列。
     *  文件级并发控制：限制同时上传的文件数为 MAX_PARALLEL_FILES，
     *  避免 100+ 文件同时启动导致 hash 计算阻塞主线程、HTTP 请求排队、DOM 卡顿。
     *  每个 task 内部仍有 chunk 级并发（concurrency），两层并发控制互不干扰。 */
    async function handleFiles(files) {
        const queue = document.getElementById('queue');
        const list = document.getElementById('queue-list');
        queue.hidden = false;

        const chunkSize = parseInt(document.getElementById('chunk-size').value, 10);
        const concurrency = parseInt(document.getElementById('concurrency').value, 10);
        const targetDir = currentPath;

        // 先创建所有任务的 DOM（让用户看到排队状态），再控制并发启动
        const tasks = [];
        for (const file of files) {
            const task = new UploadTask(file, { chunkSize, concurrency, targetDir });
            const dom = task.createDom();
            list.appendChild(dom);
            tasks.push(task);
        }

        // 文件级并发控制：MAX_PARALLEL_FILES 个 worker 串行处理任务队列
        // 100+ 文件同时启动会导致：hash 计算阻塞 + 100+ HTTP 请求 + DOM 卡顿
        const MAX_PARALLEL_FILES = 3;
        let cursor = 0;
        const runOne = async () => {
            while (cursor < tasks.length) {
                const task = tasks[cursor++];
                await task.run();
                if (task.status === 'done') loadFiles();
            }
        };
        const workers = [];
        for (let i = 0; i < MAX_PARALLEL_FILES && i < tasks.length; i++) {
            workers.push(runOne());
        }
        await Promise.all(workers);
    }

    // === 初始化 ===

    /**
     * 入口：先检查登录状态（路由守卫），通过后再加载主界面。
     * 顺序：checkAuth → bindEvents + checkHealth + loadFiles
     * 未登录时 checkAuth 会自动跳转 login.html，后续代码不执行。
     */
    async function init() {
        const ok = await checkAuth();
        if (!ok) return; // 已跳转登录页
        bindEvents();
        checkHealth();
        setInterval(checkHealth, 10000); // 每 10 秒检查一次健康
        loadFiles();
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();
