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
        download: '/api/download',
        downloadDir: '/api/download/dir',
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
            this.targetDir = opts.targetDir || ''; // 目标目录前缀（如 "docs/"）
            this.sessionId = null;
            this.totalChunks = 0;
            this.received = new Set(); // 已上传分片索引
            this.uploaded = 0;
            this.status = 'pending'; // pending | uploading | done | error | paused
            this.error = null;
            this.dom = null;
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
            const res = await fetch(url, {
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
            // 过滤 .keep 占位文件（mkdir 创建的虚拟目录标记）
            if (f.filename.endsWith('/.keep') || f.filename === '.keep') continue;
            const rel = prefix ? f.filename.slice(prefix.length) : f.filename;
            if (!rel) continue;
            const slashIdx = rel.indexOf('/');
            if (slashIdx === -1) {
                fileList.push(f);
            } else {
                const dirName = rel.slice(0, slashIdx);
                dirs.set(dirName, (dirs.get(dirName) || 0) + 1);
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
        tree.innerHTML = '<div class="tree-empty">加载中…</div>';
        try {
            const url = currentPath
                ? `${API.files}?prefix=${encodeURIComponent(currentPath)}`
                : API.files;
            const res = await fetch(url);
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
                        <button class="op-btn danger" data-action="rmdir" data-prefix="${escapeHtml(dirPrefix)}" data-name="${escapeHtml(name)}" title="删除目录">删除</button>
                    </span>
                </div>
            `);
        }
        for (const f of files) {
            items.push(`
                <div class="tree-row file" data-id="${escapeHtml(f.id)}" data-filename="${escapeHtml(f.filename)}">
                    <span class="tree-icon" aria-hidden="true">▪</span>
                    <span class="tree-name">${escapeHtml(f.filename.split('/').pop())}</span>
                    <span class="tree-meta num">${fmtSize(f.size)}</span>
                    <span class="tree-meta">${fmtDate(f.created_at)}</span>
                    <span class="tree-meta"><span class="fstore">${escapeHtml(f.storage_type || 'local')}</span></span>
                    <span class="tree-ops">
                        <a class="op-btn" href="${API.download}/${f.id}" download="${escapeHtml(f.filename)}" title="下载文件">下载</a>
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
                } else if (action === 'rmdir') {
                    confirmDeleteDir(btn.dataset.prefix, btn.dataset.name);
                } else if (action === 'rmfile') {
                    confirmDeleteFile(btn.dataset.id, btn.dataset.name);
                } else if (action === 'rename') {
                    openRenameDialog(btn.dataset.id, btn.dataset.filename);
                }
            });
        });
    }

    // === 文件/目录操作 ===

    /** 删除文件确认 */
    function confirmDeleteFile(fileId, filename) {
        openConfirmDialog(`确认删除文件「${filename}」？此操作不可恢复。`, async () => {
            try {
                const res = await fetch(`${API.files}/${fileId}`, { method: 'DELETE' });
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
                const res = await fetch(`${API.files}?prefix=${encodeURIComponent(prefix)}`, { method: 'DELETE' });
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
        const cleanup = () => {
            modal.hidden = true;
            confirmBtn.onclick = null;
            cancelBtn.onclick = null;
        };
        cancelBtn.onclick = cleanup;
        confirmBtn.onclick = async () => {
            const newFilename = input.value.trim();
            if (!newFilename) { toast('文件名不能为空', 'err'); return; }
            if (newFilename === currentFilename) { cleanup(); return; }
            try {
                const res = await fetch(API.filesRename, {
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

        const confirmBtn = document.getElementById('mkdir-confirm');
        const cancelBtn = document.getElementById('mkdir-cancel');
        const cleanup = () => {
            modal.hidden = true;
            confirmBtn.onclick = null;
            cancelBtn.onclick = null;
        };
        cancelBtn.onclick = cleanup;
        confirmBtn.onclick = async () => {
            const rawPath = input.value.trim();
            if (!rawPath) { toast('目录名不能为空', 'err'); return; }
            try {
                const res = await fetch(API.filesMkdir, {
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

        // 新建目录
        document.getElementById('mkdir-btn').addEventListener('click', mkdir);

        // 清除已完成
        document.getElementById('clear-done').addEventListener('click', () => {
            document.querySelectorAll('.queue-item.done').forEach(el => el.remove());
            const list = document.getElementById('queue-list');
            if (!list.children.length) document.getElementById('queue').hidden = true;
        });
    }

    /**
     * 规范化目标目录路径：去除开头 /、合并连续 //、非空时末尾补 /。
     * 例："docs" → "docs/"，"/docs/" → "docs/"，"" → ""，"a//b" → "a/b/"
     */
    function normalizeTargetDir(raw) {
        let s = (raw || '').trim().replace(/\\/g, '/').replace(/^\/+/, '').replace(/\/{2,}/g, '/');
        if (s && !s.endsWith('/')) s += '/';
        return s;
    }

    /** 处理选择的文件列表，加入上传队列 */
    async function handleFiles(files) {
        const queue = document.getElementById('queue');
        const list = document.getElementById('queue-list');
        queue.hidden = false;

        const chunkSize = parseInt(document.getElementById('chunk-size').value, 10);
        const concurrency = parseInt(document.getElementById('concurrency').value, 10);
        const targetDir = normalizeTargetDir(document.getElementById('target-dir').value);

        // 创建任务并依次启动（避免同时上传多个大文件导致内存压力）
        for (const file of files) {
            const task = new UploadTask(file, { chunkSize, concurrency, targetDir });
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
