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
        check: '/api/upload/check', // 秒传检查（上传前检查哈希是否已存在）
        trash: '/api/trash', // 回收站（列出/清空）
        trashRestore: '/api/trash', // 回收站恢复：/api/trash/{id}/restore（前缀，拼接用）
        trashDelete: '/api/trash', // 回收站永久删除：/api/trash/{id}（前缀，拼接用）
        preview: '/api/preview', // 文件预览（元数据/缩略图/原始内容流）
        storageUsage: '/api/storage-usage', // 存储用量查询
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
            // admin 显示管理后台入口
            if (user.role === 'admin') {
                const adminLink = document.getElementById('admin-link');
                if (adminLink) adminLink.hidden = false;
            }
            // 获取存储用量
            fetchStorageUsage();
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

    /**
     * 获取存储用量并更新导航栏显示
     * 在登录成功、上传完成、删除文件后调用
     */
    async function fetchStorageUsage() {
        try {
            const res = await apiFetch(API.storageUsage);
            if (!res.ok) return;
            const usage = await res.json();
            const el = document.getElementById('user-storage');
            if (el) {
                el.textContent = '已用 ' + fmtSize(usage.used_size || 0);
                el.hidden = false;
            }
        } catch (e) {
            console.error('fetchStorageUsage error:', e);
        }
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

    /**
     * 计算完整文件 SHA256（用于秒传检查）。
     * 使用 Web Worker 在后台线程计算，避免阻塞主线程 UI。
     * Worker 文件：/web/sha256.worker.js（纯 JS 实现 FIPS 180-4 增量 SHA256）。
     * 失败时抛出异常，由调用方降级为正常上传。
     * @param {File} file - 待计算文件
     * @param {(percent:number)=>void} [onProgress] - 进度回调（0-100）
     * @returns {Promise<string>} 64 字符 hex SHA256
     */
    function calcFullFileHash(file, onProgress) {
        return new Promise((resolve, reject) => {
            let worker;
            try {
                worker = new Worker('/web/sha256.worker.js');
            } catch (e) {
                reject(new Error('Worker 创建失败: ' + e.message));
                return;
            }
            const chunkSize = 4 * 1024 * 1024; // 4MB 分块读取
            const timeoutMs = 10 * 60 * 1000; // 10 分钟超时（超大文件兜底）
            const timer = setTimeout(() => {
                worker.terminate();
                reject(new Error('SHA256 计算超时'));
            }, timeoutMs);
            worker.onmessage = function (e) {
                const msg = e.data;
                if (!msg) return;
                if (msg.type === 'progress' && typeof onProgress === 'function') {
                    onProgress(msg.percent);
                } else if (msg.type === 'done') {
                    clearTimeout(timer);
                    worker.terminate();
                    resolve(msg.hash);
                } else if (msg.type === 'error') {
                    clearTimeout(timer);
                    worker.terminate();
                    reject(new Error(msg.error || 'SHA256 计算失败'));
                }
            };
            worker.onerror = function (e) {
                clearTimeout(timer);
                worker.terminate();
                reject(new Error('Worker 错误: ' + (e.message || 'unknown')));
            };
            worker.postMessage({ type: 'hash', file: file, chunkSize: chunkSize }, [file]);
        });
    }

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
            this.fullHash = null; // 完整文件 SHA256（秒传检查用，null=未计算）
        }

        /** 构造上传到后端的完整文件名（含目录前缀）
         *  文件夹上传场景：file.webkitRelativePath 含相对路径（如 "subdir/file.txt"），
         *  普通文件 webkitRelativePath 为空，回退到 file.name。
         *  targetDir 是当前浏览的目录前缀（如 "3/"），最终 filename = targetDir + relativePath。 */
        getUploadFilename() {
            const relativePath = this.file.webkitRelativePath || this.file.name;
            return this.targetDir ? this.targetDir + relativePath : relativePath;
        }

        /** 获取用于 UI 展示的文件名（含目录前缀） */
        getDisplayName() {
            return this.getUploadFilename();
        }

        /**
         * 秒传检查：调用 /api/upload/check 检查文件哈希是否已存在。
         * 命中时后端会复制存储文件并创建新记录，整个上传流程被跳过。
         * @returns {Promise<boolean>} true=秒传成功，false=未命中（需正常上传）
         */
        async tryInstantUpload() {
            if (!this.fullHash) return false;
            const uploadName = this.getUploadFilename();
            const res = await apiFetch(API.check, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    filename: uploadName,
                    file_size: this.file.size,
                    file_hash: this.fullHash,
                }),
            });
            // 409 表示目标文件名已存在（非秒传源文件冲突），降级为正常上传让 init 处理冲突
            if (res.status === 409) return false;
            if (!res.ok) {
                const txt = await res.text().catch(() => '');
                throw new Error(`秒传检查失败: HTTP ${res.status} ${txt}`);
            }
            const data = await res.json();
            return data.instant_upload === true;
        }

        /** 初始化上传 session，处理冲突 */
        async init() {
            // 兜底校验：即使文件选择校验被绕过，也阻止空文件发起请求
            if (this.file.size <= 0) {
                throw new Error(`文件 "${this.file.name}" 为空，无法上传`);
            }
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
                const errText = await res.text().catch(() => '');
                throw new Error(`init 失败: HTTP ${res.status} - ${errText || '未知错误'}`);
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

                // === 秒传检查 ===
                // 计算完整文件 SHA256（Worker 后台线程，不阻塞 UI），然后调用 /api/upload/check。
                // 命中则跳过整个上传流程；失败（Worker 不支持、哈希不匹配、网络错误）降级为正常上传。
                // 注：秒传检查失败是非致命的，不应中断上传流程。
                try {
                    this.fullHash = await calcFullFileHash(this.file, (percent) => {
                        // 进度回调：更新 UI 显示"计算哈希中..."
                        if (this.dom && this.status === 'uploading') {
                            const text = this.dom.querySelector('.qi-progress-text');
                            if (text) text.textContent = `计算哈希 ${percent.toFixed(0)}%`;
                        }
                    });
                    const instantOk = await this.tryInstantUpload();
                    if (instantOk) {
                        this.status = 'done';
                        this.uploaded = this.file.size;
                        this.updateDom();
                        toast(`「${this.getDisplayName()}」秒传成功`, 'ok');
                        return;
                    }
                } catch (e) {
                    console.warn('[Upload] 秒传检查失败，降级为正常上传:', e.message);
                }

                // === 正常上传流程 ===
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

    // 目录数据缓存：path → { dirs, files, ts }
    // 二次切换同一目录时瞬间渲染，后台异步刷新（stale-while-revalidate）
    const dirCache = new Map();
    const DIR_CACHE_TTL = 30000; // 缓存有效期 30 秒，超过则后台刷新

    /**
     * 使目录缓存失效（修改操作后调用）。
     * @param {string} [path] - 指定目录路径；不传则清空所有缓存
     */
    function invalidateCache(path) {
        if (path === undefined) {
            dirCache.clear();
        } else {
            dirCache.delete(path);
        }
    }

    /** 修改操作后刷新：清空缓存并重新加载当前目录 */
    function refreshAfterModify() {
        invalidateCache();
        loadFiles();
    }

    // === 选择状态管理 ===
    // selectedItems: key → { type:'file'|'dir', id?, prefix?, name, filename? }
    // key 唯一标识：文件用 'file:'+id，目录用 'dir:'+prefix
    const selectedItems = new Map();
    // 多选模式标志（长按触发后置 true，用户取消后置 false）
    let selectionMode = false;
    // touch 长按计时器和起点（用于移动端长按多选）
    let touchTimer = null;
    let touchStartXY = null;
    let touchStartRow = null;
    // shift 范围选择起点
    let shiftAnchorRow = null;

    /**
     * 根据文件名扩展名返回对应的 SVG 图标 symbol id。
     * @param {string} filename - 文件名（含扩展名）
     * @returns {string} icon-image|icon-video|icon-audio|icon-pdf|icon-code|icon-text|icon-archive|icon-file
     */
    function getIconForFile(filename) {
        const dot = filename.lastIndexOf('.');
        if (dot < 0) return 'icon-file';
        const ext = filename.slice(dot + 1).toLowerCase();
        if (['jpg', 'jpeg', 'png', 'gif', 'svg', 'webp', 'bmp', 'ico', 'tiff'].includes(ext)) return 'icon-image';
        if (['mp4', 'avi', 'mkv', 'webm', 'mov', 'flv', 'wmv', 'm4v'].includes(ext)) return 'icon-video';
        if (['mp3', 'wav', 'flac', 'aac', 'ogg', 'm4a', 'opus', 'wma'].includes(ext)) return 'icon-audio';
        if (ext === 'pdf') return 'icon-pdf';
        if (['js', 'ts', 'py', 'go', 'java', 'c', 'cpp', 'h', 'html', 'css', 'json', 'xml', 'sh', 'rb', 'php', 'rs', 'kt', 'swift', 'yml', 'yaml', 'toml'].includes(ext)) return 'icon-code';
        if (['txt', 'md', 'log', 'csv', 'rtf', 'doc', 'docx'].includes(ext)) return 'icon-text';
        if (['zip', 'rar', '7z', 'tar', 'gz', 'bz2', 'xz', 'tgz'].includes(ext)) return 'icon-archive';
        return 'icon-file';
    }

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

    /** 加载并渲染当前目录的子项（目录 + 文件）。
     *  使用 stale-while-revalidate 缓存策略：命中缓存立即渲染，后台异步刷新。 */
    async function loadFiles() {
        const tree = document.getElementById('file-tree');
        const reqPath = currentPath; // 捕获请求时的路径，防止后台刷新完成时用户已切换目录
        renderBreadcrumb();
        // 同步上传目标目录显示：跟随用户当前所在目录（currentPath）
        const targetDirEl = document.getElementById('target-dir');
        if (targetDirEl) targetDirEl.textContent = currentPath || '根目录';

        const cached = dirCache.get(reqPath);
        const now = Date.now();
        const isFresh = cached && (now - cached.ts < DIR_CACHE_TTL);

        // 命中缓存：立即渲染（消除停顿感）
        if (cached) {
            renderTree(cached.dirs, cached.files);
        } else {
            tree.innerHTML = '<div class="tree-empty">加载中…</div>';
        }

        // 缓存仍新鲜：不再后台刷新
        if (isFresh) return;

        // 后台刷新（stale-while-revalidate：即使有旧缓存也拉取最新数据）
        try {
            const url = reqPath
                ? `${API.files}?prefix=${encodeURIComponent(reqPath)}`
                : API.files;
            const res = await apiFetch(url);
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const files = await res.json();
            // 用户可能已切换目录：仅更新缓存，不渲染（避免覆盖当前界面）
            if (!files || files.length === 0) {
                dirCache.set(reqPath, { dirs: new Map(), files: [], ts: Date.now() });
                if (currentPath === reqPath) {
                    tree.innerHTML = '<div class="tree-empty">暂无文件，请上传</div>';
                }
                return;
            }
            const { dirs, files: fileList } = buildChildren(files, reqPath);
            dirCache.set(reqPath, { dirs, files: fileList, ts: Date.now() });
            // 仅当用户仍在同一目录时才更新渲染
            if (currentPath === reqPath) {
                renderTree(dirs, fileList);
            }
        } catch (e) {
            if (!cached && currentPath === reqPath) {
                tree.innerHTML = `<div class="tree-empty" style="color:var(--err)">加载失败: ${escapeHtml(e.message)}</div>`;
            }
        }
    }

    /**
     * 渲染树形列表：子目录在前，文件在后。
     * 改为"选择→操作"模式：移除行内按钮，统一用 checkbox 选择，
     * 选中后顶部工具栏显示对应操作，也可右键弹出上下文菜单。
     * @param {Map<string, number>} dirs - 子目录名→文件数
     * @param {Array} files - 子文件列表
     */
    function renderTree(dirs, files) {
        const tree = document.getElementById('file-tree');
        const items = [];
        for (const [name, count] of dirs) {
            const dirPrefix = currentPath + name + '/';
            items.push(`
                <div class="tree-row dir" data-type="dir" data-prefix="${escapeHtml(dirPrefix)}" data-name="${escapeHtml(name)}" tabindex="0">
                    <svg class="tree-icon"><use href="#icon-folder"></use></svg>
                    <span class="tree-name">${escapeHtml(name)}</span>
                    <span class="tree-meta">${count} 个文件</span>
                    <span class="tree-meta"></span>
                    <span class="tree-meta"></span>
                    <input type="checkbox" class="row-check" data-type="dir" data-prefix="${escapeHtml(dirPrefix)}" data-name="${escapeHtml(name)}" aria-label="选择目录 ${escapeHtml(name)}">
                </div>
            `);
        }
        for (const f of files) {
            const baseName = f.filename.split('/').pop();
            const iconClass = getIconForFile(baseName);
            items.push(`
                <div class="tree-row file" data-type="file" data-id="${escapeHtml(f.id)}" data-filename="${escapeHtml(f.filename)}" data-name="${escapeHtml(baseName)}" tabindex="0">
                    <svg class="tree-icon ${iconClass}"><use href="#${iconClass}"></use></svg>
                    <span class="tree-name">${escapeHtml(baseName)}</span>
                    <span class="tree-meta num">${fmtSize(f.size)}</span>
                    <span class="tree-meta">${fmtDate(f.created_at)}</span>
                    <span class="tree-meta"><span class="fstore">${escapeHtml(f.storage_type || 'local')}</span></span>
                    <input type="checkbox" class="row-check" data-type="file" data-id="${escapeHtml(f.id)}" data-name="${escapeHtml(baseName)}" aria-label="选择文件 ${escapeHtml(baseName)}">
                </div>
            `);
        }
        tree.innerHTML = items.join('');
        // 列表刷新后选择已失效，清空选中状态
        clearSelection();
        // 绑定行事件：click/contextmenu/touch（统一处理）
        tree.querySelectorAll('.tree-row').forEach(row => bindRowEvents(row));
    }

    // === 选择管理 ===

    /** 生成选择项的唯一 key */
    function selectionKey(type, idOrPrefix) {
        return type === 'file' ? 'file:' + idOrPrefix : 'dir:' + idOrPrefix;
    }

    /** 从 DOM 行元素提取选择项信息 */
    function rowToItem(row) {
        const type = row.dataset.type;
        if (type === 'file') {
            return {
                key: selectionKey('file', row.dataset.id),
                type: 'file',
                id: row.dataset.id,
                name: row.dataset.name,
                filename: row.dataset.filename,
                row,
            };
        }
        return {
            key: selectionKey('dir', row.dataset.prefix),
            type: 'dir',
            prefix: row.dataset.prefix,
            name: row.dataset.name,
            row,
        };
    }

    /** 切换某行的选中状态（单选模式：清空其他）
     * @param {HTMLElement} row - 树形行元素
     * @param {boolean} [addToExisting] - 是否追加到已选（Shift/Ctrl+click 时为 true）
     */
    function toggleSelection(row, addToExisting) {
        const item = rowToItem(row);
        const cb = row.querySelector('.row-check');
        if (selectedItems.has(item.key)) {
            // 已选中 → 取消
            selectedItems.delete(item.key);
            row.classList.remove('selected');
            if (cb) cb.checked = false;
        } else {
            // 未选中 → 选中
            if (!addToExisting) {
                // 单选模式：清空其他
                clearSelection(false);
            }
            selectedItems.set(item.key, item);
            row.classList.add('selected');
            if (cb) cb.checked = true;
        }
        updateToolbar();
    }

    /** 范围选择：从 anchor 到当前行的所有行全部选中
     * @param {HTMLElement} targetRow - Shift+click 的目标行
     */
    function selectRange(targetRow) {
        const tree = document.getElementById('file-tree');
        const rows = Array.from(tree.querySelectorAll('.tree-row'));
        const anchorIdx = shiftAnchorRow ? rows.indexOf(shiftAnchorRow) : -1;
        const targetIdx = rows.indexOf(targetRow);
        if (anchorIdx < 0 || targetIdx < 0) {
            // 无 anchor 或找不到：退化为单选
            toggleSelection(targetRow, false);
            shiftAnchorRow = targetRow;
            return;
        }
        const [from, to] = anchorIdx < targetIdx ? [anchorIdx, targetIdx] : [targetIdx, anchorIdx];
        for (let i = from; i <= to; i++) {
            const r = rows[i];
            const item = rowToItem(r);
            if (!selectedItems.has(item.key)) {
                selectedItems.set(item.key, item);
                r.classList.add('selected');
                const cb = r.querySelector('.row-check');
                if (cb) cb.checked = true;
            }
        }
        updateToolbar();
    }

    /** 清空所有选中状态
     * @param {boolean} [updateUi=true] - 是否同步更新工具栏 UI
     */
    function clearSelection(updateUi) {
        selectedItems.forEach(item => {
            if (item.row) {
                item.row.classList.remove('selected');
                const cb = item.row.querySelector('.row-check');
                if (cb) cb.checked = false;
            }
        });
        selectedItems.clear();
        selectionMode = false;
        shiftAnchorRow = null;
        if (updateUi !== false) updateToolbar();
    }

    /**
     * 更新顶部工具栏：根据选中项的类型和数量显示/隐藏对应操作按钮。
     * - 0 项：显示 default-ops，隐藏 selection-ops
     * - 1 项文件：显示 下载/分享/重命名/删除
     * - 1 项目录：显示 ZIP 下载/分享/移动/删除
     * - N≥2 项：显示 ZIP 下载/删除（仅当全为目录时可 ZIP；含文件时也支持 ZIP 打包）
     */
    function updateToolbar() {
        const defaultOps = document.getElementById('default-ops');
        const selectionOps = document.getElementById('selection-ops');
        const countEl = document.getElementById('selection-count');
        const n = selectedItems.size;
        if (n === 0) {
            if (defaultOps) defaultOps.hidden = false;
            if (selectionOps) selectionOps.hidden = true;
            return;
        }
        if (defaultOps) defaultOps.hidden = true;
        if (selectionOps) selectionOps.hidden = false;
        if (countEl) countEl.textContent = `已选 ${n} 项`;
        // 统计类型
        const items = Array.from(selectedItems.values());
        const files = items.filter(it => it.type === 'file');
        const dirs = items.filter(it => it.type === 'dir');
        const selDownload = document.getElementById('sel-download');
        const selZip = document.getElementById('sel-zip');
        const selShare = document.getElementById('sel-share');
        const selRename = document.getElementById('sel-rename');
        const selMove = document.getElementById('sel-move');
        // 下载：含文件时显示（单文件直接下载，多文件批量下载）
        if (selDownload) selDownload.hidden = files.length === 0;
        // ZIP 下载：仅当选中 1 个目录时显示（后端无批量 ZIP API）
        if (selZip) selZip.hidden = !(n === 1 && dirs.length === 1);
        // 分享：仅当选中恰好 1 项时显示
        if (selShare) selShare.hidden = n !== 1;
        // 重命名：仅当选中恰好 1 个文件时显示
        if (selRename) selRename.hidden = !(n === 1 && files.length === 1);
        // 移动：选中 ≥1 项时显示（单目录用 MoveDir 重命名路径，多项批量移动）
        if (selMove) {
            selMove.hidden = n === 0;
            // 动态文案：单选目录 → "重命名目录"；多选 → "移动 N 项"；其他 → "移动"
            if (n === 1 && dirs.length === 1) {
                selMove.textContent = '重命名目录';
            } else if (n > 1) {
                selMove.textContent = `移动 ${n} 项`;
            } else {
                selMove.textContent = '移动';
            }
        }
    }

    /** 批量删除选中项（支持文件和目录混合）
     *  文件用 batch-delete API，目录用 DELETE /api/files?prefix= 逐个删除 */
    function batchDeleteSelected() {
        if (selectedItems.size === 0) { toast('请先选择要删除的项目', 'err'); return; }
        const items = Array.from(selectedItems.values());
        const files = items.filter(it => it.type === 'file');
        const dirs = items.filter(it => it.type === 'dir');
        const msg = `确认删除选中的 ${items.length} 项（${files.length} 个文件 + ${dirs.length} 个目录）？文件将移入回收站，30 天内可恢复。`;
        openConfirmDialog(msg, async () => {
            let success = 0, fail = 0;
            // 1. 批量删除文件
            if (files.length > 0) {
                try {
                    const res = await apiFetch(API.filesBatchDelete, {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ ids: files.map(f => f.id) }),
                    });
                    if (res.ok) {
                        const data = await res.json().catch(() => ({}));
                        success += data.success || files.length;
                        fail += data.fail || 0;
                    } else {
                        fail += files.length;
                    }
                } catch (e) { fail += files.length; }
            }
            // 2. 逐个删除目录（递归删除）
            for (const d of dirs) {
                try {
                    const res = await apiFetch(`${API.files}?prefix=${encodeURIComponent(d.prefix)}`, { method: 'DELETE' });
                    if (res.ok) success++;
                    else fail++;
                } catch (e) { fail++; }
            }
            toast(`批量删除完成（成功 ${success}，失败 ${fail}）`, fail > 0 ? 'err' : 'ok');
            refreshAfterModify();
        });
    }

    // === 行事件绑定 ===

    /**
     * 为单个树形行绑定事件：click、contextmenu、touch（长按多选）、keydown。
     * @param {HTMLElement} row - .tree-row 元素
     */
    function bindRowEvents(row) {
        const cb = row.querySelector('.row-check');

        // click：checkbox 切换选中 / 目录行空白处进入子目录 / Shift+click 范围选择
        row.addEventListener('click', e => {
            // 点击 checkbox 由其自身 change 事件处理，这里不重复
            if (e.target === cb) return;
            if (e.shiftKey && shiftAnchorRow && selectionMode) {
                // Shift+click 范围选择
                e.preventDefault();
                selectRange(row);
                return;
            }
            if (selectionMode || e.ctrlKey || e.metaKey) {
                // 多选模式或 Ctrl/Cmd+click：切换选中
                e.preventDefault();
                shiftAnchorRow = row;
                toggleSelection(row, true);
                return;
            }
            // 普通点击：目录行进入子目录，文件行切换选中
            if (row.dataset.type === 'dir') {
                currentPath += row.dataset.name + '/';
                loadFiles();
            } else {
                // 文件行单击：切换选中
                shiftAnchorRow = row;
                toggleSelection(row, e.ctrlKey || e.metaKey);
            }
        });

        // 双击文件行：打开预览（目录双击无效果，单击已进入子目录）
        if (row.dataset.type === 'file') {
            row.addEventListener('dblclick', e => {
                e.preventDefault();
                e.stopPropagation();
                previewFile(row.dataset.id, row.dataset.filename);
            });
        }

        // checkbox change：切换选中（不影响目录进入）
        if (cb) {
            cb.addEventListener('click', e => e.stopPropagation());
            cb.addEventListener('change', () => {
                shiftAnchorRow = row;
                const item = rowToItem(row);
                if (cb.checked) {
                    selectedItems.set(item.key, item);
                    row.classList.add('selected');
                    selectionMode = true;
                } else {
                    selectedItems.delete(item.key);
                    row.classList.remove('selected');
                    if (selectedItems.size === 0) selectionMode = false;
                }
                updateToolbar();
            });
        }

        // 键盘：Enter/Space 进入目录或切换选中
        row.addEventListener('keydown', e => {
            if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                if (row.dataset.type === 'dir' && !selectionMode) {
                    currentPath += row.dataset.name + '/';
                    loadFiles();
                } else {
                    toggleSelection(row, true);
                }
            }
        });

        // 右键菜单：preventDefault + 弹出上下文菜单
        row.addEventListener('contextmenu', e => {
            e.preventDefault();
            // 右键时自动选中该行（如果未选中）
            if (!selectedItems.has(rowToItem(row).key)) {
                clearSelection(false);
                toggleSelection(row, false);
            }
            const items = buildContextMenuItems();
            showContextMenu(e.clientX, e.clientY, items);
        });

        // 长按多选（移动端）：touchstart 记录起点，500ms 后进入多选模式
        row.addEventListener('touchstart', e => {
            touchStartXY = { x: e.touches[0].clientX, y: e.touches[0].clientY };
            touchStartRow = row;
            touchTimer = setTimeout(() => {
                // 长按 500ms 触发：进入多选模式并选中当前行
                selectionMode = true;
                if (navigator.vibrate) navigator.vibrate(30); // 触觉反馈
                const item = rowToItem(row);
                if (!selectedItems.has(item.key)) {
                    selectedItems.set(item.key, item);
                    row.classList.add('selected');
                    const cbx = row.querySelector('.row-check');
                    if (cbx) cbx.checked = true;
                    updateToolbar();
                }
            }, 500);
        }, { passive: true });

        row.addEventListener('touchmove', e => {
            if (!touchStartXY || !touchTimer) return;
            // 移动距离超过 10px 取消长按（避免误触）
            const dx = e.touches[0].clientX - touchStartXY.x;
            const dy = e.touches[0].clientY - touchStartXY.y;
            if (Math.hypot(dx, dy) > 10) {
                clearTimeout(touchTimer);
                touchTimer = null;
            }
            // 多选模式下：滑动经过的行自动选中
            if (selectionMode) {
                const t = e.touches[0];
                const el = document.elementFromPoint(t.clientX, t.clientY);
                const r = el && el.closest('.tree-row');
                if (r && r !== touchStartRow) {
                    const item = rowToItem(r);
                    if (!selectedItems.has(item.key)) {
                        selectedItems.set(item.key, item);
                        r.classList.add('selected');
                        const cbx = r.querySelector('.row-check');
                        if (cbx) cbx.checked = true;
                        updateToolbar();
                    }
                    touchStartRow = r;
                }
            }
        }, { passive: true });

        row.addEventListener('touchend', () => {
            if (touchTimer) {
                clearTimeout(touchTimer);
                touchTimer = null;
            }
            touchStartXY = null;
            touchStartRow = null;
        });

        row.addEventListener('touchcancel', () => {
            if (touchTimer) {
                clearTimeout(touchTimer);
                touchTimer = null;
            }
            touchStartXY = null;
            touchStartRow = null;
        });
    }

    // === 上下文菜单 ===

    /**
     * 显示上下文菜单（固定定位 + 边界检测）。
     * @param {number} x - 屏幕 x 坐标（clientX）
     * @param {number} y - 屏幕 y 坐标（clientY）
     * @param {Array} items - 菜单项数组 [{label, action, danger, disabled, separator}]
     */
    function showContextMenu(x, y, items) {
        const menu = document.getElementById('ctx-menu');
        if (!menu) return;
        menu.innerHTML = items.map(it => {
            if (it.separator) return '<div class="ctx-separator"></div>';
            const cls = it.danger ? 'ctx-item danger' : 'ctx-item';
            return `<button type="button" class="${cls}"${it.disabled ? ' disabled' : ''}>${escapeHtml(it.label)}</button>`;
        }).join('');
        menu.hidden = false;
        // 边界检测：避免菜单超出视口
        const rect = menu.getBoundingClientRect();
        const winW = window.innerWidth, winH = window.innerHeight;
        let left = x, top = y;
        if (x + rect.width > winW - 8) left = winW - rect.width - 8;
        if (y + rect.height > winH - 8) top = winH - rect.height - 8;
        menu.style.left = Math.max(8, left) + 'px';
        menu.style.top = Math.max(8, top) + 'px';
        // 绑定菜单项点击：同步遍历 items 和 DOM（跳过 separator）
        const btns = menu.querySelectorAll('.ctx-item');
        let btnIdx = 0;
        for (const it of items) {
            if (it.separator) continue;
            const btn = btns[btnIdx++];
            if (!btn || it.disabled) continue;
            btn.addEventListener('click', () => {
                hideContextMenu();
                if (typeof it.action === 'function') it.action();
            });
        }
    }

    /** 隐藏上下文菜单 */
    function hideContextMenu() {
        const menu = document.getElementById('ctx-menu');
        if (menu) { menu.hidden = true; menu.innerHTML = ''; }
    }

    /**
     * 根据当前选中项构建上下文菜单项。
     * @returns {Array} 菜单项数组
     */
    function buildContextMenuItems() {
        const items = Array.from(selectedItems.values());
        const n = items.length;
        const files = items.filter(it => it.type === 'file');
        const dirs = items.filter(it => it.type === 'dir');
        const menu = [];
        if (n === 1) {
            // 单选：根据类型显示完整操作
            const it = items[0];
            if (it.type === 'file') {
                menu.push({ label: '预览', action: () => previewFile(it.id, it.filename) });
                menu.push({ label: '下载', action: () => downloadSelected() });
                menu.push({ label: '分享', action: () => shareSelected() });
                menu.push({ label: '重命名 / 移动', action: () => renameSelected() });
                menu.push({ separator: true });
                menu.push({ label: '删除', danger: true, action: () => batchDeleteSelected() });
            } else {
                menu.push({ label: 'ZIP 下载', action: () => zipDownloadSelected() });
                menu.push({ label: '分享', action: () => shareSelected() });
                menu.push({ label: '重命名目录', action: () => moveSelected() });
                menu.push({ separator: true });
                menu.push({ label: '删除', danger: true, action: () => batchDeleteSelected() });
            }
        } else if (n > 1) {
            // 多选：显示批量操作
            if (files.length > 0) {
                menu.push({ label: `下载 ${files.length} 个文件`, action: () => downloadSelected() });
            }
            menu.push({ label: `移动 ${n} 项`, action: () => moveSelected() });
            menu.push({ separator: true });
            menu.push({ label: `删除 ${n} 项`, danger: true, action: () => batchDeleteSelected() });
        }
        return menu;
    }

    // === 操作执行（基于当前选中项） ===

    // === 文件预览 ===

    /**
     * 打开文件预览 Modal。
     * 调用 /api/preview/{fileID} 获取元数据，根据 type 分发到对应渲染器。
     * @param {string} fileID - 文件 ID
     * @param {string} filename - 文件名（用于显示）
     */
    async function previewFile(fileID, filename) {
        const modal = document.getElementById('preview-modal');
        const body = document.getElementById('preview-body');
        const nameEl = document.getElementById('preview-name');
        const qualitySel = document.getElementById('preview-quality');
        const downloadBtn = document.getElementById('preview-download');

        if (!modal) return;
        nameEl.textContent = filename || '—';
        body.innerHTML = '<div class="preview-loading">加载中…</div>';
        qualitySel.hidden = true;
        qualitySel.onchange = null;
        downloadBtn.dataset.fileId = fileID || '';
        modal.hidden = false;

        try {
            const res = await apiFetch(`${API.preview}/${fileID}`);
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const meta = await res.json();

            if (!meta.supported) {
                body.innerHTML = `<div class="preview-empty">此文件类型暂不支持预览<br><span style="font-size:12px;color:var(--fg-3)">${escapeHtml(meta.filename)}</span></div>`;
                return;
            }

            switch (meta.type) {
                case 'image': renderImage(meta); break;
                case 'pdf': renderPdf(meta); break;
                case 'text':
                case 'code': renderText(meta); break;
                case 'audio': renderAudio(meta); break;
                case 'video': renderVideo(meta, fileID); break;
                case 'archive': renderArchive(meta); break;
                default:
                    body.innerHTML = `<div class="preview-empty">暂不支持 ${escapeHtml(meta.type)} 类型预览</div>`;
            }
        } catch (e) {
            body.innerHTML = `<div class="preview-empty" style="color:var(--err)">预览失败: ${escapeHtml(e.message)}</div>`;
        }
    }

    /** 关闭预览 Modal 并清空内容（停止视频/音频播放 + 清理转码轮询定时器） */
    function closePreview() {
        const modal = document.getElementById('preview-modal');
        const body = document.getElementById('preview-body');
        const qualitySel = document.getElementById('preview-quality');
        if (!modal) return;
        // 清理视频转码轮询定时器（renderVideo 在 video 元素上挂载 _cleanupTranscode）
        const video = body.querySelector('video');
        if (video && video._cleanupTranscode) {
            video._cleanupTranscode();
        }
        modal.hidden = true;
        body.innerHTML = '';
        qualitySel.hidden = true;
        qualitySel.onchange = null;
    }

    /** 渲染图片预览：3 档分辨率切换 + 点击缩放 */
    function renderImage(meta) {
        const body = document.getElementById('preview-body');
        const qualitySel = document.getElementById('preview-quality');

        // 重置选项为图片规格（防止视频预览污染选项）
        qualitySel.innerHTML = `
            <option value="small">小</option>
            <option value="medium" selected>中</option>
            <option value="large">大</option>
        `;
        qualitySel.hidden = false;
        qualitySel.value = 'medium';

        const update = () => {
            const size = qualitySel.value;
            const url = meta.urls[`thumb_${size}`] || meta.urls.original;
            body.innerHTML = `<img src="${url}" alt="${escapeHtml(meta.filename)}">`;
            const img = body.querySelector('img');
            if (img) {
                img.addEventListener('click', () => img.classList.toggle('zoomed'));
                img.addEventListener('error', () => {
                    body.innerHTML = `<div class="preview-empty" style="color:var(--err)">图片加载失败</div>`;
                });
            }
        };

        qualitySel.onchange = update;
        update();
    }

    /** 渲染 PDF 预览：iframe 依赖浏览器内置 PDF 查看器 */
    function renderPdf(meta) {
        const body = document.getElementById('preview-body');
        body.innerHTML = `<iframe src="${meta.urls.original}"></iframe>`;
    }

    /** 渲染文本/代码预览：按扩展名分发（Markdown/JSON/CSV 友好渲染，其他 prism 高亮） */
    function renderText(meta) {
        const ext = meta.filename.split('.').pop().toLowerCase();

        // 友好渲染：Markdown/JSON/CSV
        if (ext === 'md') return renderMarkdown(meta);
        if (ext === 'json') return renderJson(meta);
        if (ext === 'csv') return renderCsv(meta);

        // 默认：prism 语法高亮
        renderCode(meta);
    }

    /** 渲染 Markdown：marked.js 转 HTML，加载失败降级为 prism 高亮 */
    function renderMarkdown(meta) {
        const body = document.getElementById('preview-body');
        body.innerHTML = '<div class="preview-loading">加载内容…</div>';

        fetch(meta.urls.original)
            .then(res => {
                if (!res.ok) throw new Error('HTTP ' + res.status);
                return res.text();
            })
            .then(text => {
                const MAX = 2 * 1024 * 1024; // Markdown 允许 2MB
                let truncated = false;
                if (text.length > MAX) {
                    text = text.slice(0, MAX);
                    truncated = true;
                }

                if (window.marked) {
                    const html = marked.parse(text);
                    body.innerHTML = `<div class="markdown-body">${html}</div>`;
                } else {
                    // marked.js 加载失败，降级为 prism 高亮
                    loadPrismLang('markdown', () => {
                        body.innerHTML = `<pre><code class="language-markdown">${escapeHtml(text)}</code></pre>`;
                        if (window.Prism) Prism.highlightAllUnder(body);
                    });
                }

                if (truncated) {
                    body.innerHTML += '<div class="preview-empty">（文件过大，仅显示前 2MB 内容）</div>';
                }
            })
            .catch(e => {
                body.innerHTML = `<div class="preview-empty" style="color:var(--err)">加载失败: ${escapeHtml(e.message)}</div>`;
            });
    }

    /** 渲染 JSON：JSON.parse 格式化缩进 + prism 高亮，解析失败降级为普通文本 */
    function renderJson(meta) {
        const body = document.getElementById('preview-body');
        body.innerHTML = '<div class="preview-loading">加载内容…</div>';

        fetch(meta.urls.original)
            .then(res => {
                if (!res.ok) throw new Error('HTTP ' + res.status);
                return res.text();
            })
            .then(text => {
                const MAX = 1024 * 1024; // 1MB
                let truncated = false;
                if (text.length > MAX) {
                    text = text.slice(0, MAX);
                    truncated = true;
                }

                try {
                    const obj = JSON.parse(text);
                    const formatted = JSON.stringify(obj, null, 2);
                    loadPrismLang('json', () => {
                        body.innerHTML = `<pre><code class="language-json">${escapeHtml(formatted)}</code></pre>`;
                        if (window.Prism) Prism.highlightAllUnder(body);
                    });
                } catch (e) {
                    // JSON 解析失败，降级为普通文本
                    body.innerHTML = `<pre><code class="language-none">${escapeHtml(text)}</code></pre>`;
                }

                if (truncated) {
                    body.innerHTML += '<div class="preview-empty">（文件过大，仅显示前 1MB 内容）</div>';
                }
            })
            .catch(e => {
                body.innerHTML = `<div class="preview-empty" style="color:var(--err)">加载失败: ${escapeHtml(e.message)}</div>`;
            });
    }

    /** 渲染 CSV：解析为 HTML 表格（首行作为表头 th，最多渲染 1000 行） */
    function renderCsv(meta) {
        const body = document.getElementById('preview-body');
        body.innerHTML = '<div class="preview-loading">加载内容…</div>';

        fetch(meta.urls.original)
            .then(res => {
                if (!res.ok) throw new Error('HTTP ' + res.status);
                return res.text();
            })
            .then(text => {
                const MAX = 1024 * 1024; // 1MB
                let truncated = false;
                if (text.length > MAX) {
                    text = text.slice(0, MAX);
                    truncated = true;
                }

                const rows = parseCsv(text);
                const MAX_ROWS = 1000; // 最多渲染 1000 行
                const displayRows = rows.slice(0, MAX_ROWS);

                let html = '<div class="csv-table-wrap"><table class="csv-table"><tbody>';
                displayRows.forEach((row, idx) => {
                    html += '<tr>';
                    row.forEach(cell => {
                        const tag = idx === 0 ? 'th' : 'td';
                        html += `<${tag}>${escapeHtml(cell)}</${tag}>`;
                    });
                    html += '</tr>';
                });
                html += '</tbody></table></div>';

                if (truncated || rows.length > MAX_ROWS) {
                    html += `<div class="preview-empty">（文件过大，仅显示前 ${displayRows.length} 行）</div>`;
                }

                body.innerHTML = html;
            })
            .catch(e => {
                body.innerHTML = `<div class="preview-empty" style="color:var(--err)">加载失败: ${escapeHtml(e.message)}</div>`;
            });
    }

    /** 简单 CSV 解析（支持逗号分隔、双引号包裹、换行） */
    function parseCsv(text) {
        const rows = [];
        let row = [];
        let cell = '';
        let inQuotes = false;

        for (let i = 0; i < text.length; i++) {
            const ch = text[i];
            const next = text[i + 1];

            if (inQuotes) {
                if (ch === '"' && next === '"') {
                    cell += '"';
                    i++; // 跳过下一个 "
                } else if (ch === '"') {
                    inQuotes = false;
                } else {
                    cell += ch;
                }
            } else {
                if (ch === '"') {
                    inQuotes = true;
                } else if (ch === ',') {
                    row.push(cell);
                    cell = '';
                } else if (ch === '\n') {
                    row.push(cell);
                    rows.push(row);
                    row = [];
                    cell = '';
                } else if (ch === '\r') {
                    // 跳过 \r（Windows 换行符 \r\n）
                } else {
                    cell += ch;
                }
            }
        }
        // 最后一行（文件末尾无换行）
        if (cell !== '' || row.length > 0) {
            row.push(cell);
            rows.push(row);
        }
        return rows;
    }

    /** 渲染代码：prism 语法高亮（原 renderText 逻辑） */
    function renderCode(meta) {
        const body = document.getElementById('preview-body');
        body.innerHTML = '<div class="preview-loading">加载内容…</div>';

        fetch(meta.urls.original)
            .then(res => {
                if (!res.ok) throw new Error('HTTP ' + res.status);
                return res.text();
            })
            .then(text => {
                const MAX = 1024 * 1024; // 1MB
                let truncated = false;
                if (text.length > MAX) {
                    text = text.slice(0, MAX);
                    truncated = true;
                }

                const ext = meta.filename.split('.').pop().toLowerCase();
                const langMap = {
                    js: 'javascript', ts: 'typescript', go: 'go', py: 'python',
                    java: 'java', c: 'c', cpp: 'cpp', rs: 'rust', rb: 'ruby',
                    php: 'php', sh: 'bash', sql: 'sql', html: 'markup', htm: 'markup',
                    css: 'css', xml: 'markup', yml: 'yaml', yaml: 'yaml',
                    log: 'none', txt: 'none'
                };
                const lang = langMap[ext] || 'none';

                loadPrismLang(lang, () => {
                    body.innerHTML = `<pre><code class="language-${lang}">${escapeHtml(text)}</code></pre>`;
                    if (truncated) {
                        body.innerHTML += '<div class="preview-empty">（文件过大，仅显示前 1MB 内容）</div>';
                    }
                    if (window.Prism) Prism.highlightAllUnder(body);
                });
            })
            .catch(e => {
                body.innerHTML = `<div class="preview-empty" style="color:var(--err)">加载失败: ${escapeHtml(e.message)}</div>`;
            });
    }

    /** 动态加载 prism.js 语言包（核心已含 markup/css/clike/javascript） */
    function loadPrismLang(lang, callback) {
        if (lang === 'none' || lang === 'markup' || lang === 'css' || lang === 'javascript' || lang === 'clike') {
            if (callback) callback();
            return;
        }
        // 已加载则直接回调
        if (window.Prism && Prism.languages[lang]) {
            if (callback) callback();
            return;
        }
        const compMap = {
            typescript: 'typescript', go: 'go', python: 'python', java: 'java',
            c: 'c', cpp: 'cpp', rust: 'rust', ruby: 'ruby', php: 'php',
            bash: 'bash', sql: 'sql', json: 'json', yaml: 'yaml', markdown: 'markdown'
        };
        const comp = compMap[lang];
        if (!comp) {
            if (callback) callback();
            return;
        }
        const script = document.createElement('script');
        script.src = `/web/lib/prism/components_prism-${comp}.min.js`;
        script.onload = () => { if (callback) callback(); };
        script.onerror = () => { if (callback) callback(); }; // 加载失败也回调（不高亮）
        document.head.appendChild(script);
    }

    /** 渲染音频预览：原生 HTML5 audio 控件 */
    function renderAudio(meta) {
        const body = document.getElementById('preview-body');
        body.innerHTML = `<audio controls src="${meta.urls.original}"></audio>`;
    }

    /**
     * 渲染视频预览：HTML5 video 控件 + 海报 + 三档画质切换（默认高画质）。
     * high=原画质（1080p，默认，直接 fallback 到原文件流式） / medium=720p / low=480p
     * 切换画质时保留 currentTime/playbackRate/paused 状态，避免重新播放。
     * 中/低画质首次切换时后端异步转码，前端显示进度条并轮询状态，完成后自动加载视频流。
     * @param {Object} meta - /api/preview/{id} 返回的元数据
     * @param {string} fileID - 文件 ID（用于构造 transcode-status URL）
     */
    function renderVideo(meta, fileID) {
        const body = document.getElementById('preview-body');
        const qualitySel = document.getElementById('preview-quality');

        // 复用顶部画质选择器，替换为视频三档选项
        // 默认高画质：直接 fallback 到原文件流式播放，无需等待 ffmpeg 转码
        qualitySel.innerHTML = `
            <option value="high" selected>高画质</option>
            <option value="medium">中画质</option>
            <option value="low">低画质</option>
        `;
        qualitySel.hidden = false;
        qualitySel.value = 'high';

        const poster = meta.urls.poster ? ` poster="${meta.urls.poster}"` : '';
        // video 容器使用 position:relative，进度条绝对定位居中覆盖
        body.innerHTML = `
            <div class="video-preview-wrap">
                <video controls${poster} preload="metadata" style="max-width:100%;max-height:85vh;background:#000"></video>
                <div class="transcode-progress" hidden>
                    <div class="transcode-spinner"></div>
                    <div class="transcode-text">转码中…</div>
                    <div class="transcode-hint">首次切换中/低画质需要转码，请稍候</div>
                </div>
            </div>
        `;
        const video = body.querySelector('video');
        const progressEl = body.querySelector('.transcode-progress');

        // 清理函数：清除轮询定时器（供 closePreview 调用）
        const cleanup = () => {
            if (video._transcodeTimer) {
                clearTimeout(video._transcodeTimer);
                video._transcodeTimer = null;
            }
        };
        video._cleanupTranscode = cleanup;

        /**
         * 设置 video.src 并恢复播放状态（currentTime/playbackRate/paused）。
         * @param {string} url - 视频流 URL
         */
        const loadVideoStream = (url) => {
            const t = video.currentTime || 0;
            const rate = video.playbackRate || 1;
            const wasPaused = video.paused;
            video.src = url;
            video.playbackRate = rate;
            const onLoaded = () => {
                if (t > 0 && isFinite(t)) {
                    try { video.currentTime = t; } catch (e) { /* ignore */ }
                }
                if (!wasPaused) {
                    video.play().catch(() => { /* autoplay 可能被浏览器拦截 */ });
                }
                video.removeEventListener('loadedmetadata', onLoaded);
            };
            video.addEventListener('loadedmetadata', onLoaded);
        };

        /**
         * 异步加载指定画质：
         * - high：直接 fallback 到原文件流式，无需转码
         * - medium/low：先查 transcode-status，done 直接加载；否则显示进度条 + 触发转码 + 轮询 2s
         * @param {string} q - high|medium|low
         */
        const loadQuality = async (q) => {
            // 清理上一轮轮询（切 medium→low→medium 时避免叠加定时器）
            cleanup();

            if (q === 'high') {
                progressEl.hidden = true;
                loadVideoStream(meta.urls.original);
                return;
            }

            // 中/低画质：先查转码状态
            const statusUrl = `/api/preview/${fileID}/transcode-status?quality=${q}`;
            try {
                const res = await fetch(statusUrl);
                if (!res.ok) throw new Error('HTTP ' + res.status);
                const data = await res.json();

                if (data.status === 'done') {
                    // 缓存命中，直接加载视频流
                    progressEl.hidden = true;
                    loadVideoStream(data.url || meta.urls[`video_${q}`]);
                    return;
                }

                // 需要转码，显示进度条
                progressEl.hidden = false;

                // 若任务未启动，先触发 transcode 端点启动后台任务
                if (data.status === 'not_started') {
                    fetch(meta.urls[`video_${q}`]).catch(() => { /* 触发启动，忽略响应 */ });
                }

                // 启动轮询（2s 间隔）
                const poll = async () => {
                    try {
                        const r = await fetch(statusUrl);
                        if (!r.ok) throw new Error('HTTP ' + r.status);
                        const d = await r.json();
                        if (d.status === 'done') {
                            progressEl.hidden = true;
                            loadVideoStream(d.url || meta.urls[`video_${q}`]);
                            return;
                        }
                        if (d.status === 'failed') {
                            progressEl.hidden = true;
                            toast('转码失败: ' + (d.error || '未知错误'), 'err');
                            return;
                        }
                        // 继续轮询
                        video._transcodeTimer = setTimeout(poll, 2000);
                    } catch (e) {
                        // 网络错误：继续重试
                        video._transcodeTimer = setTimeout(poll, 2000);
                    }
                };
                video._transcodeTimer = setTimeout(poll, 2000);
            } catch (e) {
                toast('加载画质失败: ' + e.message, 'err');
            }
        };

        qualitySel.onchange = () => loadQuality(qualitySel.value);
        // 首次加载默认高画质（原文件流式，无需转码等待）
        loadQuality('high');
    }

    /** 渲染压缩包预览：列出包内文件树，单击文件触发提取预览或下载 */
    async function renderArchive(meta) {
        const body = document.getElementById('preview-body');
        body.innerHTML = '<div class="preview-loading">读取压缩包内容…</div>';
        try {
            const res = await fetch(meta.urls.archive_list);
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const data = await res.json();
            if (!data.entries || data.entries.length === 0) {
                body.innerHTML = '<div class="preview-empty">压缩包为空</div>';
                return;
            }

            // 排序：目录在前，按 path 字母序
            const entries = data.entries.slice().sort((a, b) => {
                if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
                return a.path.localeCompare(b.path);
            });

            body.innerHTML = '';
            const list = document.createElement('div');
            list.className = 'archive-list';
            body.appendChild(list);

            // 构建并渲染树
            const tree = buildArchiveTree(entries);
            const stats = { files: 0, dirs: 0 };
            renderArchiveNode(list, tree, 0, meta, stats);

            // 底部统计
            const footer = document.createElement('div');
            footer.className = 'archive-footer';
            let footText = `共 ${stats.files} 个文件，${stats.dirs} 个目录`;
            if (data.truncated) footText += '（已截断，仅显示前 10000 项）';
            footer.textContent = footText;
            body.appendChild(footer);
        } catch (e) {
            body.innerHTML = `<div class="preview-empty" style="color:var(--err)">读取失败: ${escapeHtml(e.message)}</div>`;
        }
    }

    /** 构建压缩包条目树（扁平 entries → 嵌套结构） */
    function buildArchiveTree(entries) {
        const root = { name: '', path: '', isDir: true, entry: null, children: [] };
        const dirMap = new Map();
        dirMap.set('', root);

        for (const e of entries) {
            const parts = e.path.split('/');
            let curPath = '';
            let parent = root;
            for (let i = 0; i < parts.length; i++) {
                const part = parts[i];
                const isLast = i === parts.length - 1;
                curPath = curPath ? curPath + '/' + part : part;
                if (isLast && !e.is_dir) {
                    // 文件节点：直接挂到 parent
                    parent.children.push({ name: part, path: curPath, isDir: false, entry: e, children: [] });
                } else {
                    // 目录节点（e.is_dir && isLast → 末段目录；否则中间路径段）
                    if (!dirMap.has(curPath)) {
                        const node = { name: part, path: curPath, isDir: true, entry: (e.is_dir && isLast) ? e : null, children: [] };
                        dirMap.set(curPath, node);
                        parent.children.push(node);
                    }
                    parent = dirMap.get(curPath);
                }
            }
        }
        return root;
    }

    /** 递归渲染压缩包节点（目录懒加载子节点） */
    function renderArchiveNode(container, node, depth, meta, stats) {
        for (const child of node.children) {
            const row = document.createElement('div');
            row.className = 'archive-row' + (child.isDir ? ' archive-dir-row' : '');
            row.style.paddingLeft = (depth * 16) + 'px';

            if (child.isDir) {
                stats.dirs++;
                const arrow = document.createElement('span');
                arrow.className = 'archive-arrow';
                arrow.textContent = '▶';
                row.appendChild(arrow);

                const icon = document.createElement('span');
                icon.className = 'archive-icon';
                icon.textContent = '📁';
                row.appendChild(icon);

                const name = document.createElement('span');
                name.className = 'archive-name';
                name.textContent = child.name + '/';
                row.appendChild(name);

                container.appendChild(row);

                // 子节点容器（懒加载，默认折叠）
                const subContainer = document.createElement('div');
                subContainer.className = 'archive-sub';
                subContainer.hidden = true;
                container.appendChild(subContainer);

                let expanded = false;
                row.addEventListener('click', () => {
                    expanded = !expanded;
                    arrow.textContent = expanded ? '▼' : '▶';
                    subContainer.hidden = !expanded;
                    if (expanded && subContainer.children.length === 0) {
                        renderArchiveNode(subContainer, child, depth + 1, meta, stats);
                    }
                });
            } else {
                stats.files++;
                const arrow = document.createElement('span');
                arrow.className = 'archive-arrow archive-arrow-placeholder';
                row.appendChild(arrow);

                const icon = document.createElement('span');
                icon.className = 'archive-icon';
                icon.textContent = fileIconFor(child.name);
                row.appendChild(icon);

                const name = document.createElement('span');
                name.className = 'archive-name';
                name.textContent = child.name;
                row.appendChild(name);

                const size = document.createElement('span');
                size.className = 'archive-size';
                size.textContent = fmtSize(child.entry.size);
                row.appendChild(size);

                const ops = document.createElement('span');
                ops.className = 'archive-ops';
                if (isArchiveFilePreviewable(child.name)) {
                    const previewBtn = document.createElement('button');
                    previewBtn.className = 'ghost-btn archive-op-btn';
                    previewBtn.textContent = '预览';
                    previewBtn.addEventListener('click', (ev) => {
                        ev.stopPropagation();
                        extractArchiveFile(meta, child.path, 'preview');
                    });
                    ops.appendChild(previewBtn);
                }
                const downloadBtn = document.createElement('button');
                downloadBtn.className = 'ghost-btn archive-op-btn';
                downloadBtn.textContent = '下载';
                downloadBtn.addEventListener('click', (ev) => {
                    ev.stopPropagation();
                    extractArchiveFile(meta, child.path, 'download');
                });
                ops.appendChild(downloadBtn);
                row.appendChild(ops);

                container.appendChild(row);
            }
        }
    }

    /** 判断压缩包内文件是否可前端预览（与后端 getFileType 对齐） */
    function isArchiveFilePreviewable(filename) {
        const ext = (filename.split('.').pop() || '').toLowerCase();
        const previewable = ['jpg','jpeg','png','gif','webp','bmp','pdf','txt','md','log','csv','json','xml','yml','yaml','js','ts','go','py','java','c','cpp','rs','rb','php','sh','sql','html','htm','css','mp3','wav','ogg','flac','aac','m4a','mp4','webm','mkv','avi','mov'];
        return previewable.includes(ext);
    }

    /** 返回文件名对应的 emoji 图标 */
    function fileIconFor(filename) {
        const ext = (filename.split('.').pop() || '').toLowerCase();
        if (['jpg','jpeg','png','gif','webp','bmp'].includes(ext)) return '🖼️';
        if (ext === 'pdf') return '📄';
        if (['mp3','wav','ogg','flac'].includes(ext)) return '🎵';
        if (['mp4','webm','mkv','avi','mov'].includes(ext)) return '🎬';
        if (['zip','tar','gz','bz2','tgz','tbz2'].includes(ext)) return '🗜️';
        return '📄';
    }

    /** 提取压缩包内单个文件：mode='preview' 在 modal 内 iframe 预览；mode='download' 触发附件下载 */
    function extractArchiveFile(meta, path, mode) {
        const url = meta.urls.archive_extract + encodeURIComponent(path);
        if (mode === 'download') {
            const a = document.createElement('a');
            a.href = url + '&download=1';
            a.download = '';
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
        } else {
            const body = document.getElementById('preview-body');
            body.innerHTML = `
                <div class="archive-preview-back">
                    <button class="ghost-btn" id="archive-back-btn">← 返回文件列表</button>
                    <span class="archive-preview-name">${escapeHtml(path)}</span>
                </div>
                <iframe src="${url}" class="archive-preview-iframe"></iframe>
            `;
            document.getElementById('archive-back-btn').addEventListener('click', () => {
                renderArchive(meta);
            });
        }
    }

    /** 下载选中的文件（单选直接下载，多选逐个触发批量下载） */
    function downloadSelected() {
        const items = Array.from(selectedItems.values());
        const files = items.filter(it => it.type === 'file');
        if (files.length === 0) { toast('请选择至少一个文件', 'err'); return; }
        if (files.length === 1) {
            window.location.href = `${API.download}/${files[0].id}`;
            return;
        }
        // 多文件：逐个触发下载，间隔 400ms 避免浏览器拦截
        toast(`开始下载 ${files.length} 个文件...`, 'ok');
        files.forEach((f, i) => {
            setTimeout(() => {
                const a = document.createElement('a');
                a.href = `${API.download}/${f.id}`;
                a.download = '';
                document.body.appendChild(a);
                a.click();
                document.body.removeChild(a);
            }, i * 400);
        });
    }

    /** ZIP 下载选中的单个目录 */
    function zipDownloadSelected() {
        const items = Array.from(selectedItems.values());
        const dir = items.find(it => it.type === 'dir');
        if (dir) {
            window.location.href = `${API.downloadDir}?prefix=${encodeURIComponent(dir.prefix)}`;
        }
    }

    /** 分享选中的单项（文件或目录） */
    function shareSelected() {
        const items = Array.from(selectedItems.values());
        if (items.length !== 1) return;
        const it = items[0];
        if (it.type === 'file') {
            openShareDialog(it.id, null, it.name || '文件', 'file');
        } else {
            openShareDialog(null, it.prefix, it.name || '目录', 'dir');
        }
    }

    /** 重命名选中的单个文件 */
    function renameSelected() {
        const items = Array.from(selectedItems.values());
        const file = items.find(it => it.type === 'file');
        if (file) {
            openRenameDialog(file.id, file.filename);
        }
    }

    /** 移动选中项（单目录用原逻辑，多项批量移动到目标目录） */
    function moveSelected() {
        const items = Array.from(selectedItems.values());
        if (items.length === 0) return;
        // 单选目录：用原逻辑（输入完整新路径）
        if (items.length === 1 && items[0].type === 'dir') {
            openMoveDirDialog(items[0].prefix, items[0].name);
            return;
        }
        // 多选或含文件：批量移动到目标目录
        openBatchMoveDialog(items);
    }

    /** 批量移动多个选中项到目标目录（目录选择器方式）
     * @param {Array} items - 选中项数组，每项 {type, id?, prefix?, name, filename?} */
    function openBatchMoveDialog(items) {
        const modal = document.getElementById('move-dir-modal');
        const nameEl = document.getElementById('move-dir-name');
        const input = document.getElementById('move-dir-input');
        const inputWrap = modal.querySelector('.modal-input-wrap');
        const confirmBtn = document.getElementById('move-dir-confirm');
        const cancelBtn = document.getElementById('move-dir-cancel');
        const head = modal.querySelector('.modal-head h3');
        const desc = modal.querySelector('.modal-desc');
        const origHead = head.textContent;
        const origDesc = desc.textContent;
        head.textContent = '批量移动';
        nameEl.textContent = `已选 ${items.length} 项`;
        desc.textContent = '选择目标目录，所有选中项将移动到该目录下：';
        inputWrap.style.display = 'none';

        // 创建目录选择器（如不存在）
        let selector = modal.querySelector('.batch-move-selector');
        if (!selector) {
            selector = document.createElement('div');
            selector.className = 'batch-move-selector';
            inputWrap.parentNode.insertBefore(selector, inputWrap.nextSibling);
        }

        let targetPath = '/'; // 当前浏览路径（也是移动目标）

        /** 加载指定路径的子目录列表到选择器 */
        async function loadDirs(path) {
            targetPath = path;
            selector.innerHTML = '<div class="batch-move-loading">加载中...</div>';
            try {
                // 根目录用空字符串作为 prefix（与 loadFiles 一致）
                const apiPrefix = path === '/' ? '' : path;
                const url = apiPrefix
                    ? `${API.files}?prefix=${encodeURIComponent(apiPrefix)}`
                    : API.files;
                const res = await apiFetch(url);
                if (!res.ok) throw new Error('HTTP ' + res.status);
                const files = await res.json();
                let dirEntries = [];
                if (files && files.length > 0) {
                    const { dirs } = buildChildren(files, apiPrefix);
                    dirEntries = Array.from(dirs.entries());
                }
                let html = `<div class="batch-move-path">当前目录：<strong>${path === '/' ? '根目录' : escapeHtml(path.replace(/\/$/, ''))}</strong></div>`;
                if (path !== '/' && path !== '') {
                    const parts = path.replace(/\/$/, '').split('/');
                    parts.pop();
                    const parentPath = parts.length === 0 ? '/' : parts.join('/') + '/';
                    html += `<button type="button" class="batch-move-item" data-path="${escapeHtml(parentPath)}">📁 ../（返回上级）</button>`;
                }
                if (dirEntries.length === 0) {
                    html += '<div class="batch-move-empty">此目录下无子目录</div>';
                } else {
                    for (const [name, count] of dirEntries) {
                        // 根目录时不加前导 /（与 loadFiles 路径规范一致）
                        const childPath = (path === '/' ? '' : path) + name + '/';
                        html += `<button type="button" class="batch-move-item" data-path="${escapeHtml(childPath)}">📁 ${escapeHtml(name)}（${count} 个文件）</button>`;
                    }
                }
                selector.innerHTML = html;
                selector.querySelectorAll('.batch-move-item').forEach(btn => {
                    btn.addEventListener('click', () => loadDirs(btn.dataset.path));
                });
            } catch (e) {
                selector.innerHTML = `<div class="batch-move-empty" style="color:var(--err);">加载失败：${escapeHtml(e.message)}</div>`;
            }
        }

        loadDirs('/');
        modal.hidden = false;

        const submit = async () => {
            const targetDir = targetPath;
            // 根目录前缀为空字符串（validateFilePath 禁止以 / 开头）
            const prefix = targetDir === '/' ? '' : targetDir;
            let success = 0, fail = 0;
            for (const it of items) {
                try {
                    if (it.type === 'file') {
                        const newFilename = prefix + it.name;
                        const res = await apiFetch(API.filesRename, {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ id: it.id, new_filename: newFilename }),
                        });
                        if (res.ok) success++; else fail++;
                    } else if (it.type === 'dir') {
                        const newPrefix = prefix + it.name + '/';
                        const res = await apiFetch(API.filesMoveDir, {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ old_prefix: it.prefix, new_prefix: newPrefix }),
                        });
                        if (res.ok) success++; else fail++;
                    }
                } catch (e) {
                    fail++;
                }
            }
            toast(`批量移动完成（成功 ${success}，失败 ${fail}）`, fail > 0 ? 'err' : 'ok');
            cleanup();
            refreshAfterModify();
        };
        const cleanup = () => {
            modal.hidden = true;
            confirmBtn.onclick = null;
            cancelBtn.onclick = null;
            head.textContent = origHead;
            desc.textContent = origDesc;
            inputWrap.style.display = '';
            selector.innerHTML = '';
        };
        cancelBtn.onclick = cleanup;
        confirmBtn.onclick = submit;
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
        const passwordInput = document.getElementById('share-password');
        const createBtn = document.getElementById('share-create-btn');
        const resultEl = document.getElementById('share-result');
        const linkInput = document.getElementById('share-link');
        const copyBtn = document.getElementById('share-copy-btn');

        nameEl.textContent = displayName;
        expirySelect.value = '0'; // 默认永久
        if (passwordInput) passwordInput.value = ''; // 清空密码输入
        resultEl.hidden = true;
        linkInput.value = '';
        modal.hidden = false;

        // 创建分享按钮
        const onCreate = async () => {
            const expiresIn = parseInt(expirySelect.value, 10);
            const body = { share_type: shareType, expires_in: expiresIn };
            if (shareType === 'file') body.file_id = fileId;
            else body.dir_prefix = dirPrefix;
            // 附加访问密码（可选，trim 后非空才发送）
            const pwd = passwordInput ? passwordInput.value.trim() : '';
            if (pwd) body.password = pwd;

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
                const lock = s.has_password ? '<span class="share-status locked" title="已设置访问密码">密码</span>' : '';
                const fullURL = window.location.origin + s.url;
                return `
                    <div class="share-item" data-id="${escapeHtml(s.id)}">
                        <div class="share-item-head">
                            <span class="share-item-name">${escapeHtml(s.name)}</span>
                            <span class="share-item-type">${s.share_type === 'file' ? '文件' : '目录'}</span>
                            ${status}
                            ${lock}
                        </div>
                        <div class="share-item-meta">
                            <span>创建: ${created}</span>
                            <span>过期: ${expiry}</span>
                            <span>下载: ${s.download_count} 次</span>
                        </div>
                        <div class="share-item-link">
                            <input type="text" value="${escapeHtml(fullURL)}" readonly class="share-link-input">
                            <button class="op-btn copy-share-link" data-url="${escapeHtml(fullURL)}">复制</button>
                            <button class="op-btn share-pwd-btn" data-id="${escapeHtml(s.id)}" data-has-pwd="${s.has_password ? '1' : '0'}">${s.has_password ? '修改密码' : '设置密码'}</button>
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
            listEl.querySelectorAll('.share-pwd-btn').forEach(btn => {
                btn.addEventListener('click', () => {
                    const id = btn.dataset.id;
                    const hasPwd = btn.dataset.hasPwd === '1';
                    openSharePasswordDialog(id, hasPwd);
                });
            });
        } catch (e) {
            listEl.innerHTML = `<div class="error">加载失败: ${escapeHtml(e.message)}</div>`;
        }
    }

    /**
     * 打开修改/设置分享密码对话框
     * @param {string} id - 分享 ID
     * @param {boolean} hasPassword - 当前是否已设置密码（true=修改，false=设置）
     */
    function openSharePasswordDialog(id, hasPassword) {
        const modal = document.getElementById('share-password-modal');
        const title = document.getElementById('share-password-title');
        const input = document.getElementById('share-password-input');
        const confirmBtn = document.getElementById('share-password-confirm-btn');
        const cancelBtn = document.getElementById('share-password-cancel-btn');

        title.textContent = hasPassword ? '修改访问密码' : '设置访问密码';
        input.value = '';
        modal.hidden = false;

        // 用克隆替换清除旧事件监听，避免重复绑定
        const newConfirm = confirmBtn.cloneNode(true);
        confirmBtn.parentNode.replaceChild(newConfirm, confirmBtn);
        const newCancel = cancelBtn.cloneNode(true);
        cancelBtn.parentNode.replaceChild(newCancel, cancelBtn);

        newConfirm.addEventListener('click', async () => {
            const pwd = input.value.trim();
            try {
                const res = await apiFetch(`${API.share}/${id}/password`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ password: pwd })
                });
                if (!res.ok) throw new Error(`HTTP ${res.status}`);
                const data = await res.json();
                toast(data.action === 'cleared' ? '密码已清除' : '密码已更新', 'ok');
                modal.hidden = true;
                openShareManageDialog();
            } catch (e) {
                toast('操作失败: ' + e.message, 'err');
            }
        });

        newCancel.addEventListener('click', () => {
            modal.hidden = true;
        });
    }

    // === 回收站操作 ===

    /** 打开回收站对话框并加载回收站文件列表 */
    async function openTrashDialog() {
        const modal = document.getElementById('trash-modal');
        const listEl = document.getElementById('trash-list');
        const countTag = document.getElementById('trash-count-tag');
        modal.hidden = false;
        listEl.innerHTML = '<div class="loading">加载中...</div>';
        countTag.textContent = '—';

        try {
            const res = await apiFetch(API.trash);
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            const data = await res.json();
            const items = data.items || [];
            const retention = data.retention || 30;
            countTag.textContent = `${items.length} 项`;
            document.getElementById('trash-desc').textContent =
                `文件删除后移入回收站，保留 ${retention} 天可恢复。过期后自动永久删除。`;

            if (items.length === 0) {
                listEl.innerHTML = '<div class="empty">回收站为空</div>';
                return;
            }

            listEl.innerHTML = items.map(it => {
                const deleted = it.deleted_at ? new Date(it.deleted_at).toLocaleString('zh-CN') : '—';
                const expires = it.expires_at ? new Date(it.expires_at).toLocaleString('zh-CN') : '—';
                const expiredBadge = it.is_expired
                    ? '<span class="share-status expired">已过期</span>'
                    : `<span class="share-status active">${expires} 后清理</span>`;
                return `
                    <div class="share-item" data-id="${escapeHtml(it.id)}">
                        <div class="share-item-head">
                            <span class="share-item-name">${escapeHtml(it.filename)}</span>
                            <span class="share-item-type">${fmtSize(it.size)}</span>
                            ${expiredBadge}
                        </div>
                        <div class="share-item-meta">
                            <span>删除时间: ${deleted}</span>
                            <span>归属: ${escapeHtml(it.owner || '—')}</span>
                        </div>
                        <div class="share-item-link">
                            <button class="op-btn restore-trash" data-id="${escapeHtml(it.id)}">恢复</button>
                            <button class="op-btn danger permanent-delete-trash" data-id="${escapeHtml(it.id)}">永久删除</button>
                        </div>
                    </div>
                `;
            }).join('');

            // 绑定恢复按钮
            listEl.querySelectorAll('.restore-trash').forEach(btn => {
                btn.addEventListener('click', () => restoreTrashItem(btn.dataset.id));
            });
            // 绑定永久删除按钮
            listEl.querySelectorAll('.permanent-delete-trash').forEach(btn => {
                btn.addEventListener('click', () => permanentDeleteTrashItem(btn.dataset.id));
            });
        } catch (e) {
            listEl.innerHTML = `<div class="error">加载失败: ${escapeHtml(e.message)}</div>`;
        }
    }

    /** 恢复回收站文件（POST /api/trash/{id}/restore） */
    async function restoreTrashItem(id) {
        try {
            const res = await apiFetch(`${API.trashRestore}/${id}/restore`, { method: 'POST' });
            if (res.status === 409) {
                const data = await res.json().catch(() => ({}));
                toast(`恢复失败：${data.message || '同名文件已存在'}`, 'err');
                return;
            }
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            toast('文件已恢复', 'ok');
            // 刷新回收站列表 + 文件列表
            openTrashDialog();
            refreshAfterModify();
        } catch (e) {
            toast(`恢复失败: ${e.message}`, 'err');
        }
    }

    /** 永久删除回收站文件（DELETE /api/trash/{id}，不可恢复） */
    function permanentDeleteTrashItem(id) {
        openConfirmDialog('确认永久删除此文件？此操作不可恢复，文件将被彻底删除。', async () => {
            try {
                const res = await apiFetch(`${API.trashDelete}/${id}`, { method: 'DELETE' });
                if (!res.ok) throw new Error(`HTTP ${res.status}`);
                toast('文件已永久删除', 'ok');
                openTrashDialog();
            } catch (e) {
                toast(`永久删除失败: ${e.message}`, 'err');
            }
        });
    }

    /** 清空回收站（DELETE /api/trash，永久删除所有回收站文件） */
    function emptyTrash() {
        openConfirmDialog('确认清空回收站？所有回收站文件将被永久删除，不可恢复。', async () => {
            try {
                const res = await apiFetch(API.trash, { method: 'DELETE' });
                if (!res.ok) throw new Error(`HTTP ${res.status}`);
                const data = await res.json().catch(() => ({}));
                toast(`回收站已清空（${data.count || 0} 个文件）`, 'ok');
                openTrashDialog();
            } catch (e) {
                toast(`清空回收站失败: ${e.message}`, 'err');
            }
        });
    }

    // === 文件/目录操作 ===

    /** 删除文件确认（软删除：移入回收站，30 天内可恢复） */
    function confirmDeleteFile(fileId, filename) {
        openConfirmDialog(`确认删除文件「${filename}」？文件将移入回收站，30 天内可恢复。`, async () => {
            try {
                const res = await apiFetch(`${API.files}/${fileId}`, { method: 'DELETE' });
                if (!res.ok) throw new Error('HTTP ' + res.status);
                toast(`「${filename}」已移入回收站`, 'ok');
                refreshAfterModify();
            } catch (e) {
                toast(`删除失败: ${e.message}`, 'err');
            }
        });
    }

    /** 删除目录确认（软删除：递归移入回收站，30 天内可恢复） */
    function confirmDeleteDir(prefix, dirName) {
        openConfirmDialog(`确认删除目录「${dirName}」及其所有内容？文件将移入回收站，30 天内可恢复。`, async () => {
            try {
                const res = await apiFetch(`${API.files}?prefix=${encodeURIComponent(prefix)}`, { method: 'DELETE' });
                if (!res.ok) throw new Error('HTTP ' + res.status);
                const data = await res.json().catch(() => ({}));
                toast(`目录「${dirName}」已移入回收站（${data.files_deleted || 0} 个文件）`, 'ok');
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
                refreshAfterModify();
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
                refreshAfterModify();
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
        const head = modal.querySelector('.modal-head h3');
        const origHead = head.textContent;
        // 单选目录场景：标题改"重命名目录"（MoveDir 批量改前缀即重命名）
        head.textContent = '重命名目录';

        nameEl.textContent = dirName + '/';
        // 默认填入源目录去掉末尾 / 的路径，方便用户在原基础上修改
        input.value = prefix.replace(/\/$/, '');
        modal.hidden = false;
        input.focus();
        // 默认选中末级目录名（如 prefix="docs/sub/" → 选中 "sub"），降低误改父级风险
        const trimmed = prefix.replace(/\/$/, '');
        const lastSlash = trimmed.lastIndexOf('/');
        if (lastSlash >= 0) {
            input.setSelectionRange(lastSlash + 1, trimmed.length);
        } else {
            input.select();
        }

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
                refreshAfterModify();
            } catch (e) {
                toast(`移动目录失败: ${e.message}`, 'err');
            }
        };
        const cleanup = () => {
            modal.hidden = true;
            confirmBtn.onclick = null;
            cancelBtn.onclick = null;
            input.onkeydown = null;
            // 恢复 modal 原始标题（避免影响 openBatchMoveDialog 等其他场景）
            head.textContent = origHead;
        };
        cancelBtn.onclick = cleanup;
        confirmBtn.onclick = submit;
        input.onkeydown = e => {
            if (e.key === 'Enter') { e.preventDefault(); submit(); }
            else if (e.key === 'Escape') { e.preventDefault(); cleanup(); }
        };
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

        // 选择文件夹：webkitdirectory 返回的 File 对象含 webkitRelativePath
        // getUploadFilename 会优先用 webkitRelativePath 作为 filename，保留目录结构
        const folderPickBtn = document.getElementById('folder-pick-btn');
        const folderInput = document.getElementById('folder-input');
        if (folderPickBtn && folderInput) {
            folderPickBtn.addEventListener('click', () => {
                folderInput.click();
            });
            folderInput.addEventListener('change', e => {
                const files = Array.from(e.target.files);
                if (files.length) handleFiles(files);
                folderInput.value = ''; // 允许重复选择同一文件夹
            });
        }

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

        // === 选择操作栏按钮（selection-ops）===
        // 取消选择：清空选中状态
        const selCancelBtn = document.getElementById('sel-cancel');
        if (selCancelBtn) {
            selCancelBtn.addEventListener('click', () => clearSelection());
        }
        // 下载选中文件
        const selDownloadBtn = document.getElementById('sel-download');
        if (selDownloadBtn) {
            selDownloadBtn.addEventListener('click', downloadSelected);
        }
        // ZIP 下载选中目录
        const selZipBtn = document.getElementById('sel-zip');
        if (selZipBtn) {
            selZipBtn.addEventListener('click', zipDownloadSelected);
        }
        // 分享选中项
        const selShareBtn = document.getElementById('sel-share');
        if (selShareBtn) {
            selShareBtn.addEventListener('click', shareSelected);
        }
        // 重命名选中文件
        const selRenameBtn = document.getElementById('sel-rename');
        if (selRenameBtn) {
            selRenameBtn.addEventListener('click', renameSelected);
        }
        // 移动选中目录
        const selMoveBtn = document.getElementById('sel-move');
        if (selMoveBtn) {
            selMoveBtn.addEventListener('click', moveSelected);
        }
        // 批量删除选中项
        const selDeleteBtn = document.getElementById('sel-delete');
        if (selDeleteBtn) {
            selDeleteBtn.addEventListener('click', batchDeleteSelected);
        }

        // === 上下文菜单：点击空白处或 ESC 关闭 ===
        document.addEventListener('click', e => {
            const menu = document.getElementById('ctx-menu');
            if (menu && !menu.hidden && !menu.contains(e.target)) {
                hideContextMenu();
            }
        });
        document.addEventListener('keydown', e => {
            if (e.key === 'Escape') {
                hideContextMenu();
                // 预览 modal 打开时优先关闭预览
                const previewModal = document.getElementById('preview-modal');
                if (previewModal && !previewModal.hidden) {
                    closePreview();
                    return;
                }
                // ESC 也清空选择（便于快速退出多选模式）
                if (selectedItems.size > 0) clearSelection();
            }
        });
        // 窗口尺寸变化时隐藏菜单（避免定位错乱）
        window.addEventListener('resize', hideContextMenu);

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

        // 回收站
        const trashBtn = document.getElementById('trash-btn');
        if (trashBtn) {
            trashBtn.addEventListener('click', openTrashDialog);
        }
        const trashCloseBtn = document.getElementById('trash-close-btn');
        if (trashCloseBtn) {
            trashCloseBtn.addEventListener('click', () => {
                document.getElementById('trash-modal').hidden = true;
            });
        }
        const trashEmptyBtn = document.getElementById('trash-empty-btn');
        if (trashEmptyBtn) {
            trashEmptyBtn.addEventListener('click', emptyTrash);
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

        // === 文件预览 Modal 事件绑定 ===
        const previewModal = document.getElementById('preview-modal');
        const previewCloseBtn = document.getElementById('preview-close');
        const previewDownloadBtn = document.getElementById('preview-download');
        const previewBackdrop = previewModal ? previewModal.querySelector('.modal-backdrop') : null;
        if (previewCloseBtn) {
            previewCloseBtn.addEventListener('click', closePreview);
        }
        if (previewBackdrop) {
            previewBackdrop.addEventListener('click', closePreview);
        }
        if (previewDownloadBtn) {
            previewDownloadBtn.addEventListener('click', () => {
                const id = previewDownloadBtn.dataset.fileId;
                if (id) {
                    window.open(`${API.download}/${id}`, '_blank');
                }
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
        // 每次新批次上传重置全局冲突策略，避免上次"应用到后续所有冲突"残留影响本次决策
        globalStrategy = null;

        const chunkSize = parseInt(document.getElementById('chunk-size').value, 10);
        const concurrency = parseInt(document.getElementById('concurrency').value, 10);
        const targetDir = currentPath;

        // 先创建所有任务的 DOM（让用户看到排队状态），再控制并发启动
        const tasks = [];
        for (const file of files) {
            // 空文件不支持上传：跳过并在队列中显示提示，避免发起无效请求触发 400
            // DOM 结构与正常 queue-item 一致（li.queue-item.done），便于"清除已完成"按钮统一清理
            if (file.size <= 0) {
                const skipped = document.createElement('li');
                skipped.className = 'queue-item done';
                skipped.innerHTML = `
                    <div class="qi-head">
                        <span class="qi-name">${escapeHtml(file.name)}</span>
                        <span class="qi-status done">已跳过</span>
                    </div>
                    <div class="qi-meta">
                        <span class="qi-progress-text" style="color:var(--warn)">文件为空，无法上传</span>
                    </div>
                `;
                list.appendChild(skipped);
                continue;
            }
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
                if (task.status === 'done') refreshAfterModify();
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
