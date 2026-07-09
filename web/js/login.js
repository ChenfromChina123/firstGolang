// login.js — 登录页面逻辑（从 login.html 内联脚本提取，满足 CSP script-src 'self'）
(function () {
    'use strict';

    const form = document.getElementById('login-form');
    if (!form) return;

    const errorBox = document.getElementById('login-error');
    const loginBtn = document.getElementById('login-btn');

    // 如果 URL 中有 redirect 参数，登录成功后跳转到该地址
    const params = new URLSearchParams(window.location.search);
    const redirect = params.get('redirect') || '/web/index.html';

    // URL 参数提示（从注册/激活/重置页面跳转回来时显示）
    const noticeBox = document.getElementById('login-notice');
    if (params.get('activated') === '1') {
        showNotice('账号已激活，请登录', 'success');
    } else if (params.get('registered') === '1') {
        showNotice('注册成功，请查收激活邮件', 'success');
    } else if (params.get('reset') === '1') {
        showNotice('密码已重置，请登录', 'success');
    } else if (params.get('error') === 'not_activated') {
        showNotice('账号未激活，请查收激活邮件', 'error');
    }

    function showNotice(msg, type) {
        noticeBox.textContent = msg;
        noticeBox.className = 'login-notice show ' + type;
    }

    form.addEventListener('submit', async (e) => {
        e.preventDefault();
        errorBox.classList.remove('show');
        loginBtn.disabled = true;
        loginBtn.textContent = '登录中...';

        const username = document.getElementById('username').value.trim();
        const password = document.getElementById('password').value;

        try {
            const encryptedPassword = await encryptPassword(password);
            const resp = await fetch('/api/login', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ username, password: encryptedPassword }),
                credentials: 'same-origin'
            });

            if (resp.ok) {
                window.location.href = redirect;
            } else {
                const data = await resp.json().catch(() => ({}));
                showError(data.message || '登录失败');
            }
        } catch (err) {
            showError('网络错误：' + err.message);
        } finally {
            loginBtn.disabled = false;
            loginBtn.textContent = '登录';
        }
    });

    function showError(msg) {
        errorBox.textContent = msg;
        errorBox.classList.add('show');
    }
})();
