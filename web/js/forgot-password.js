// forgot-password.js — 忘记密码页面逻辑（从 forgot-password.html 内联脚本提取，满足 CSP script-src 'self'）
(function () {
    'use strict';

    const form = document.getElementById('forgot-form');
    if (!form) return;

    const errorBox = document.getElementById('login-error');
    const successBox = document.getElementById('login-success');
    const submitBtn = document.getElementById('login-btn');

    form.addEventListener('submit', async (e) => {
        e.preventDefault();
        errorBox.classList.remove('show');
        successBox.classList.remove('show');
        submitBtn.disabled = true;
        submitBtn.textContent = '发送中...';

        const email = document.getElementById('email').value.trim();
        if (!email) {
            showError('请输入邮箱');
            submitBtn.disabled = false;
            submitBtn.textContent = '发送验证码';
            return;
        }

        try {
            const resp = await fetch('/api/forgot-password', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ email }),
                credentials: 'same-origin'
            });
            const data = await resp.json().catch(() => ({}));

            if (resp.ok && data.success) {
                const encoded = encodeURIComponent(email);
                successBox.innerHTML = (data.message || '验证码已发送') + '<br><br>' +
                    '<a href="/web/reset-password.html?email=' + encoded + '">前往重置密码 →</a>';
                successBox.classList.add('show');
                form.style.display = 'none';
            } else {
                showError(data.message || '发送失败');
            }
        } catch (err) {
            showError('网络错误：' + err.message);
        } finally {
            submitBtn.disabled = false;
            submitBtn.textContent = '发送验证码';
        }
    });

    function showError(msg) {
        errorBox.textContent = msg;
        errorBox.classList.add('show');
    }
})();
