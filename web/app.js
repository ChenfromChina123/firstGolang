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
        download: '/api/download',
        health: '/api/health',
    };

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

    /** 简单 SHA-256 计算（用于断点续传标识，失败时降级为 name+size） */
    async function calcFileHash(file) {
        try {
            const buf = await file.slice(0, Math.min(file.size, 8 * 1024 * 1024)).arrayBuffer();
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
            this.sessionId = null;
            this.totalChunks = 0;
            this.received = new Set(); // 已上传分片索引
            this.uploaded = 0;
            this.status = 'pending'; // pending | uploading | done | error | paused
            this.error = null;
            this.dom = null;
        }

        /** 初始化上传 session，处理冲突 */
        async init() {
            const fileHash = await calcFileHash(this.file);
            // force/rename 通过 query 参数传递（后端 fastQueryParam 解析）
            let url = API.init;
            if (this.strategy === 'overwrite') url += '?force=true';
            if (this.strategy === 'rename') url += '?rename=true';

            // 文件元信息通过 JSON body 传递（后端 InitUploadRequest 解析）
            const res = await fetch(url, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    filename: this.file.name,
                    file_size: this.file.size,
                    chunk_size: this.chunkSize,
                    storage: 'local',
                    file_hash: fileHash,
                }),
            });
            if (res.status === 409) {
                // 冲突：需要用户决策
                const data = await res.json().catch(() => ({}));
                const strategy = await askConflict(this.file.name, data);
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
                const res = await fetch(`${API.status}?session_id=${this.sessionId}`);
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

            const res = await fetch(API.chunk, { method: 'POST', body: form });
            if (!res.ok) throw new Error(`chunk ${idx} 失败: HTTP ${res.status}`);
            this.received.add(idx);
            this.uploaded = Math.min((idx + 1) * this.chunkSize, this.file.size);
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
            const res = await fetch(API.complete, {
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
                toast(`「${this.file.name}」上传完成`, 'ok');
            } catch (e) {
                this.status = 'error';
                this.error = e.message;
                this.updateDom();
                toast(`「${this.file.name}」上传失败: ${e.message}`, 'err');
            }
        }

        /** 创建队列 DOM 节点 */
        createDom() {
            const li = document.createElement('li');
            li.className = 'queue-item uploading';
            li.innerHTML = `
                <div class="qi-head">
                    <span class="qi-name">${escapeHtml(this.file.name)}</span>
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

        /** 更新队列 DOM 进度 */
        updateDom() {
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

    // === 文件列表 ===

    /** 加载并渲染文件列表 */
    async function loadFiles() {
        const body = document.getElementById('files-body');
        body.innerHTML = '<tr class="empty-row"><td colspan="6">加载中…</td></tr>';
        try {
            const res = await fetch(API.files);
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const files = await res.json();
            if (!files || files.length === 0) {
                body.innerHTML = '<tr class="empty-row"><td colspan="6">暂无文件，请上传</td></tr>';
                return;
            }
            body.innerHTML = files.map(f => `
                <tr>
                    <td class="fname">${escapeHtml(f.filename)}</td>
                    <td class="num fsize">${fmtSize(f.size)}</td>
                    <td class="num fsize">${Math.ceil(f.size / 524288)}</td>
                    <td class="fdate">${fmtDate(f.created_at)}</td>
                    <td><span class="fstore">${escapeHtml(f.storage_type || 'local')}</span></td>
                    <td class="ops"><a class="dl-btn" href="${API.download}/${f.id}" download="${escapeHtml(f.filename)}">下载</a></td>
                </tr>
            `).join('');
        } catch (e) {
            body.innerHTML = `<tr class="empty-row"><td colspan="6" style="color:var(--err)">加载失败: ${escapeHtml(e.message)}</td></tr>`;
        }
    }

    // === 事件绑定 ===

    /** 绑定拖拽、选择、上传控制等事件 */
    function bindEvents() {
        const dropzone = document.getElementById('dropzone');
        const fileInput = document.getElementById('file-input');
        const pickBtn = document.getElementById('pick-btn');

        // 点击拖拽区触发选择
        dropzone.addEventListener('click', e => {
            if (e.target !== pickBtn) fileInput.click();
        });
        dropzone.addEventListener('keydown', e => {
            if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fileInput.click(); }
        });
        pickBtn.addEventListener('click', e => { e.stopPropagation(); fileInput.click(); });

        // 拖拽
        ['dragenter', 'dragover'].forEach(ev => {
            dropzone.addEventListener(ev, e => { e.preventDefault(); dropzone.classList.add('dragover'); });
        });
        ['dragleave', 'drop'].forEach(ev => {
            dropzone.addEventListener(ev, e => { e.preventDefault(); dropzone.classList.remove('dragover'); });
        });
        dropzone.addEventListener('drop', e => {
            const files = Array.from(e.dataTransfer.files);
            if (files.length) handleFiles(files);
        });

        // 选择文件
        fileInput.addEventListener('change', e => {
            const files = Array.from(e.target.files);
            if (files.length) handleFiles(files);
            fileInput.value = ''; // 允许重复选择同一文件
        });

        // 冲突对话框按钮
        document.querySelectorAll('.opt-btn').forEach(btn => {
            btn.addEventListener('click', () => {
                if (conflictResolver) conflictResolver(btn.dataset.strategy);
            });
        });

        // 刷新文件列表
        document.getElementById('refresh-files').addEventListener('click', loadFiles);

        // 清除已完成
        document.getElementById('clear-done').addEventListener('click', () => {
            document.querySelectorAll('.queue-item.done').forEach(el => el.remove());
            const list = document.getElementById('queue-list');
            if (!list.children.length) document.getElementById('queue').hidden = true;
        });
    }

    /** 处理选择的文件列表，加入上传队列 */
    async function handleFiles(files) {
        const queue = document.getElementById('queue');
        const list = document.getElementById('queue-list');
        queue.hidden = false;

        const chunkSize = parseInt(document.getElementById('chunk-size').value, 10);
        const concurrency = parseInt(document.getElementById('concurrency').value, 10);

        // 创建任务并依次启动（避免同时上传多个大文件导致内存压力）
        for (const file of files) {
            const task = new UploadTask(file, { chunkSize, concurrency });
            const dom = task.createDom();
            list.appendChild(dom);
            task.run().then(() => {
                if (task.status === 'done') loadFiles();
            });
            await sleep(150); // 错开启动，让 init 请求不扎堆
        }
    }

    // === 初始化 ===

    /** 入口：绑定事件、启动健康检查、加载文件列表 */
    function init() {
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
