// register.js — 注册页面逻辑（从 register.html 内联脚本提取，满足 CSP script-src 'self'）
(function () {
    'use strict';

    const form = document.getElementById('register-form');
    if (!form) return;

    const errorBox = document.getElementById('login-error');
    const successBox = document.getElementById('login-success');
    const submitBtn = document.getElementById('login-btn');

    form.addEventListener('submit', async (e) => {
        e.preventDefault();
        errorBox.classList.remove('show');
        successBox.classList.remove('show');
        submitBtn.disabled = true;
        submitBtn.textContent = '注册中...';

        const email = document.getElementById('email').value.trim();
        const password = document.getElementById('password').value;
        const confirmPassword = document.getElementById('confirm-password').value;

        // 前端校验
        if (!email || !password || !confirmPassword) {
            showError('请填写所有字段');
            submitBtn.disabled = false;
            submitBtn.textContent = '注册';
            return;
        }
        if (password !== confirmPassword) {
            showError('两次输入的密码不一致');
            submitBtn.disabled = false;
            submitBtn.textContent = '注册';
            return;
        }
        if (password.length < 8) {
            showError('密码至少 8 位');
            submitBtn.disabled = false;
            submitBtn.textContent = '注册';
            return;
        }

        try {
            const encryptedPassword = await encryptPassword(password);
            const encryptedConfirm = await encryptPassword(confirmPassword);
            const resp = await fetch('/api/register', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ email, password: encryptedPassword, confirm_password: encryptedConfirm }),
                credentials: 'same-origin'
            });
            const data = await resp.json().catch(() => ({}));

            if (resp.ok && data.success) {
                showSuccess(data.message || '激活邮件已发送，请查收邮箱', email);
            } else {
                showError(data.message || '注册失败');
            }
        } catch (err) {
            showError('网络错误：' + err.message);
        } finally {
            submitBtn.disabled = false;
            submitBtn.textContent = '注册';
        }
    });

    function showError(msg) {
        errorBox.textContent = msg;
        errorBox.classList.add('show');
    }

    function showSuccess(msg, email) {
        successBox.innerHTML = msg + '<br><br>' +
            '没有收到邮件？<a href="#" id="resend-link">重新发送</a>　·　' +
            '<a href="/web/login.html">返回登录</a>';
        successBox.classList.add('show');
        // 隐藏表单
        form.style.display = 'none';
        // 绑定重新发送链接
        const resendLink = document.getElementById('resend-link');
        if (resendLink) {
            resendLink.addEventListener('click', async (e) => {
                e.preventDefault();
                resendLink.textContent = '发送中...';
                try {
                    const resp = await fetch('/api/resend-activation', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ email }),
                        credentials: 'same-origin'
                    });
                    const data = await resp.json().catch(() => ({}));
                    if (resp.ok && data.success) {
                        errorBox.classList.remove('show');
                        alert(data.message || '激活邮件已重新发送');
                    } else {
                        alert(data.message || '发送失败');
                    }
                } catch (err) {
                    alert('网络错误：' + err.message);
                } finally {
                    resendLink.textContent = '重新发送';
                }
            });
        }
    }
})();
