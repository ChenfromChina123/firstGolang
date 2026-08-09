// FileSync 访问令牌(PAT)页面逻辑
// 从 tokens.html 提取为外部 JS，符合 CSP script-src 'self' 要求（禁止内联脚本）
(function () {
    'use strict';
    let lastToken = '';

    // apiFetch 统一封装 fetch：401 自动跳转登录页
    function apiFetch(input, init) {
        return fetch(input, init).then(res => {
            if (res.status === 401) window.location.href = '/web/login.html?reason=session_expired';
            return res;
        });
    }

    // toast 显示顶部提示条（ok=true 绿色，false 红色）
    function toast(msg, ok) {
        const el = document.getElementById('toast');
        el.textContent = msg;
        el.className = 'toast ' + (ok ? 'ok' : 'error');
        el.style.display = 'block';
        setTimeout(() => { el.style.display = 'none'; }, 4000);
    }

    // 格式化日期：空值显示"永久"
    function fmtDate(s) { return s ? new Date(s).toLocaleString() : '永久'; }
    // 格式化字节数：<=0 显示"不限"
    function fmtBytes(n) {
        n = Number(n) || 0;
        if (n <= 0) return '不限';
        if (n < 1024) return n + ' B';
        if (n < 1048576) return (n / 1024).toFixed(1) + ' KB';
        if (n < 1073741824) return (n / 1048576).toFixed(2) + ' MB';
        return (n / 1073741824).toFixed(2) + ' GB';
    }
    // scopeTags 把空格分隔的 scope 字符串渲染成彩色标签
    function scopeTags(scopes) {
        return (scopes || '').split(/\s+/).filter(Boolean).map(s => {
            const cls = s === 'filesync:read' ? 'scope-read' : s === 'filesync:write' ? 'scope-write' : 'scope-share';
            return '<span class="scope-tag ' + cls + '">' + s.replace('filesync:', '') + '</span>';
        }).join('');
    }

    // loadSpaces 加载当前用户的空间列表，填充到"限定空间"下拉框
    // 显示空间名称，提交空间 ID（后端按 ID 校验空间存在性）
    function loadSpaces() {
        const sel = document.getElementById('tok-space');
        apiFetch('/api/spaces', { credentials: 'same-origin' })
            .then(r => r.json().then(d => ({ ok: r.ok, d })))
            .then(({ ok, d }) => {
                if (!ok) { toast('加载空间列表失败: ' + (d.error || ''), false); return; }
                const spaces = d.spaces || [];
                // 保留"不限"选项，追加每个空间（显示名称，提交 ID）
                sel.innerHTML = '<option value="">不限</option>' +
                    spaces.map(s => '<option value="' + s.id + '">' + s.name + '</option>').join('');
            })
            .catch(() => toast('加载空间列表失败', false));
    }

    // loadTokens 加载已生成的令牌列表并渲染表格
    // 改进错误处理：显示 HTTP 状态码和具体错误信息，便于诊断
    function loadTokens() {
        apiFetch('/api/tokens', { credentials: 'same-origin' })
            .then(r => r.text().then(t => ({ ok: r.ok, status: r.status, text: t })))
            .then(({ ok, status, text }) => {
                let d = {};
                try { d = JSON.parse(text); } catch (e) {
                    document.getElementById('tok-list').innerHTML =
                        '<tr><td colspan="6" class="empty">加载失败 (HTTP ' + status + '): ' + text.slice(0, 120) + '</td></tr>';
                    return;
                }
                if (!ok) {
                    document.getElementById('tok-list').innerHTML =
                        '<tr><td colspan="6" class="empty">加载失败 (HTTP ' + status + '): ' + (d.message || d.error || '未知错误') + '</td></tr>';
                    return;
                }
                const tb = document.getElementById('tok-list');
                if (!d.tokens || !d.tokens.length) {
                    tb.innerHTML = '<tr><td colspan="6" class="empty">还没有令牌，先在上方生成一个</td></tr>';
                    return;
                }
                tb.innerHTML = d.tokens.map(t => {
                    const sandbox = [t.space_id ? '空间:' + t.space_id : '', t.path_prefix ? '前缀:' + t.path_prefix : ''].filter(Boolean).join(' · ') || '不限';
                    return '<tr>' +
                        '<td>' + t.name + '</td>' +
                        '<td>' + scopeTags(t.scopes) + '</td>' +
                        '<td class="mono" style="font-size:11px">' + sandbox + '</td>' +
                        '<td class="mono">' + fmtBytes(t.quota_used) + ' / ' + fmtBytes(t.quota_bytes) + '</td>' +
                        '<td class="mono" style="font-size:11px">' + fmtDate(t.created_at) + '</td>' +
                        '<td><button class="btn btn-danger" onclick="window.__revoke(\'' + t.id + '\')">吊销</button></td>' +
                        '</tr>';
                }).join('');
            })
            .catch(() => toast('加载令牌列表失败（网络错误）', false));
    }

    // __revoke 吊销令牌（暴露到 window 供 onclick 调用）
    window.__revoke = function (id) {
        if (!confirm('确定吊销该令牌？使用它的 Agent 将立即失去访问权限。')) return;
        apiFetch('/api/tokens/' + id, { method: 'DELETE', credentials: 'same-origin' })
            .then(r => r.json().then(d => ({ ok: r.ok, d })))
            .then(({ ok, d }) => {
                if (ok) { toast('已吊销', true); loadTokens(); }
                else toast(d.message || '吊销失败', false);
            })
            .catch(() => toast('吊销失败', false));
    };

    // 生成令牌按钮点击事件
    document.getElementById('btn-create').addEventListener('click', function () {
        const name = document.getElementById('tok-name').value.trim();
        if (!name) { toast('请输入令牌名称', false); return; }
        const scopes = Array.from(document.querySelectorAll('.scope:checked')).map(c => c.value);
        if (!scopes.length) { toast('至少选择一个权限范围', false); return; }
        const quotaMB = parseInt(document.getElementById('tok-quota').value || '0', 10);
        const expiresIn = parseInt(document.getElementById('tok-expires').value || '0', 10);
        const body = {
            name,
            scopes,
            space_id: document.getElementById('tok-space').value,  // select 的 value 即空间 ID
            path_prefix: document.getElementById('tok-prefix').value.trim(),
            quota_bytes: quotaMB > 0 ? quotaMB * 1024 * 1024 : 0,
            expires_in: expiresIn
        };
        apiFetch('/api/tokens', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body)
        })
            .then(r => r.json().then(d => ({ ok: r.ok, d })))
            .then(({ ok, d }) => {
                if (ok) {
                    lastToken = d.token || '';
                    document.getElementById('token-plain').textContent = lastToken;
                    document.getElementById('modal-result').classList.add('show');
                    loadTokens();
                    document.getElementById('tok-name').value = '';
                } else {
                    toast(d.message || d.error || '生成失败', false);
                }
            })
            .catch(() => toast('生成失败', false));
    });

    // copyToken 复制明文令牌到剪贴板（暴露到 window 供 onclick 调用）
    // 注意：token-plain 是 span 元素，不支持 select()，需用 Clipboard API + 临时 textarea 降级
    window.copyToken = function () {
        const text = (document.getElementById('token-plain').textContent || '').trim();
        if (!text) { toast('令牌为空', false); return; }
        // 优先使用现代 Clipboard API（HTTPS / localhost 下可用）
        if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(text)
                .then(() => toast('已复制', true))
                .catch(() => fallbackCopy(text));
        } else {
            fallbackCopy(text);
        }
    };
    // fallbackCopy 降级复制方案：临时 textarea + execCommand（兼容非 HTTPS 环境）
    function fallbackCopy(text) {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.top = '-9999px';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.focus();
        ta.select();
        let ok = false;
        try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
        document.body.removeChild(ta);
        toast(ok ? '已复制' : '请手动选中复制', ok);
    }
    // closeResult 关闭令牌明文弹窗（暴露到 window 供 onclick 调用）
    window.closeResult = function () { document.getElementById('modal-result').classList.remove('show'); };

    // 页面初始化：先加载空间下拉框，再加载令牌列表
    loadSpaces();
    loadTokens();
})();
