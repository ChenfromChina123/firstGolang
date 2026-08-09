// reset-password.js — 重置密码页面逻辑（从 reset-password.html 内联脚本提取，满足 CSP script-src 'self'）
(function () {
    'use strict';

    // 自动从 URL 参数填充 email
    const params = new URLSearchParams(window.location.search);
    const presetEmail = params.get('email');
    if (presetEmail) {
        document.getElementById('email').value = presetEmail;
        document.getElementById('code').focus();
    }

    const form = document.getElementById('reset-form');
    if (!form) return;

    const errorBox = document.getElementById('login-error');
    const successBox = document.getElementById('login-success');
    const submitBtn = document.getElementById('login-btn');

    form.addEventListener('submit', async (e) => {
        e.preventDefault();
        errorBox.classList.remove('show');
        successBox.classList.remove('show');
        submitBtn.disabled = true;
        submitBtn.textContent = '重置中...';

        const email = document.getElementById('email').value.trim();
        const code = document.getElementById('code').value.trim();
        const password = document.getElementById('password').value;
        const confirmPassword = document.getElementById('confirm-password').value;

        if (!email || !code || !password || !confirmPassword) {
            showError('请填写所有字段');
            submitBtn.disabled = false;
            submitBtn.textContent = '重置密码';
            return;
        }
        if (!/^\d{6}$/.test(code)) {
            showError('验证码必须为 6 位数字');
            submitBtn.disabled = false;
            submitBtn.textContent = '重置密码';
            return;
        }
        if (password !== confirmPassword) {
            showError('两次输入的密码不一致');
            submitBtn.disabled = false;
            submitBtn.textContent = '重置密码';
            return;
        }
        if (password.length < 8) {
            showError('密码至少 8 位');
            submitBtn.disabled = false;
            submitBtn.textContent = '重置密码';
            return;
        }

        try {
            const encryptedNew = await encryptPassword(password);
            const encryptedConfirm = await encryptPassword(confirmPassword);
            const resp = await fetch('/api/reset-password', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    email, code,
                    new_password: encryptedNew,
                    confirm_password: encryptedConfirm
                }),
                credentials: 'same-origin'
            });
            const data = await resp.json().catch(() => ({}));

            if (resp.ok && data.success) {
                successBox.innerHTML = (data.message || '密码已重置') + '<br><br>' +
                    '<a href="' + (window.__FS_LOGIN_URL || '/web/login.html') + '?reset=1">前往登录 →</a>';
                successBox.classList.add('show');
                form.style.display = 'none';
            } else {
                showError(data.message || '重置失败');
            }
        } catch (err) {
            showError('网络错误：' + err.message);
        } finally {
            submitBtn.disabled = false;
            submitBtn.textContent = '重置密码';
        }
    });

    function showError(msg) {
        errorBox.textContent = msg;
        errorBox.classList.add('show');
    }
})();
