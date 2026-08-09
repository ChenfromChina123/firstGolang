// activate.js — 账号激活页面逻辑（从 activate.html 内联脚本提取，满足 CSP script-src 'self'）
(function () {
    'use strict';

    const params = new URLSearchParams(window.location.search);
    const status = params.get('status') || 'loading';
    const card = document.getElementById('card');
    if (!card) return;

    const icons = {
        success: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6L9 17l-5-5"/></svg>',
        error: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg>',
        expired: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>',
        loading: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/></svg>'
    };

    if (status === 'loading' || !status) {
        // 直接访问 activate.html 没有 status 参数，可能是用户手动打开
        renderError('无效的激活链接', '请通过邮件中的激活链接点击进入，或重新发送激活邮件。');
    } else if (status === 'success') {
        renderSuccess();
    } else if (status === 'expired') {
        renderExpired();
    } else if (status === 'invalid') {
        renderError('激活链接无效', '链接可能已被使用或不存在，请重新发送激活邮件。');
    } else if (status === 'error') {
        renderError('激活失败', '服务器处理时发生错误，请稍后重试或联系管理员。');
    } else {
        renderError('未知状态', '激活链接状态异常，请重新发送激活邮件。');
    }

    function renderSuccess() {
        card.innerHTML = `
            <div class="status-icon success">${icons.success}</div>
            <div class="status-title">账号已激活</div>
            <div class="status-message">恭喜！你的 FileSync 账号已成功激活，<br>现在可以使用邮箱和密码登录了。</div>
            <a href="/web/login.html?activated=1" class="action-btn">立即登录</a>
        `;
    }

    function renderExpired() {
        card.innerHTML = `
            <div class="status-icon error">${icons.expired}</div>
            <div class="status-title">链接已过期</div>
            <div class="status-message">激活链接有效期 24 小时，已过期。<br>请重新发送激活邮件。</div>
            <div class="resend-form">
                <input type="email" id="resend-email" placeholder="你的注册邮箱" autocomplete="email" inputmode="email" spellcheck="false">
                <button class="resend-btn" id="resend-btn">重新发送激活邮件</button>
            </div>
            <a href="/web/login.html" class="action-link">返回登录</a>
        `;
        bindResend();
    }

    function renderError(title, message) {
        card.innerHTML = `
            <div class="status-icon error">${icons.error}</div>
            <div class="status-title">${title}</div>
            <div class="status-message">${message}</div>
            <div class="resend-form">
                <input type="email" id="resend-email" placeholder="你的注册邮箱" autocomplete="email" inputmode="email" spellcheck="false">
                <button class="resend-btn" id="resend-btn">重新发送激活邮件</button>
            </div>
            <a href="/web/login.html" class="action-link">返回登录</a>
        `;
        bindResend();
    }

    function bindResend() {
        const btn = document.getElementById('resend-btn');
        const emailInput = document.getElementById('resend-email');
        if (!btn || !emailInput) return;
        btn.addEventListener('click', async () => {
            const email = emailInput.value.trim();
            if (!email) {
                alert('请输入邮箱');
                return;
            }
            btn.disabled = true;
            btn.textContent = '发送中...';
            try {
                const resp = await fetch('/api/resend-activation', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ email }),
                    credentials: 'same-origin'
                });
                const data = await resp.json().catch(() => ({}));
                if (resp.ok && data.success) {
                    alert(data.message || '激活邮件已发送，请查收邮箱');
                    window.location.href = window.__FS_LOGIN_URL || '/web/login.html';
                } else {
                    alert(data.message || '发送失败');
                }
            } catch (err) {
                alert('网络错误：' + err.message);
            } finally {
                btn.disabled = false;
                btn.textContent = '重新发送激活邮件';
            }
        });
    }
})();
