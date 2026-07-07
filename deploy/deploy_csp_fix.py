"""部署 CSP 修复 + Cache-Control + 密码泄露修复到生产服务器
- 上传 filesync_server_linux 到 /opt/filesync/server
- 上传修改的 HTML 文件（login/register/forgot-password/reset-password/activate/share/admin）
- 上传新建的 JS 文件（web/js/*.js）和修改的 admin.js
- 备份原二进制，重启 systemd 服务
- 验证 CSP 头和 HTML 内容
"""
import sys
import time
from datetime import datetime
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run, upload

LOCAL_BASE = r'd:\STUDY\GO\StudyGolang\firstGolang\filesync'
REMOTE_BASE = '/opt/filesync'

# 需要上传的文件列表：(本地路径, 远程路径)
FILES = [
    # 二进制
    (f'{LOCAL_BASE}\\filesync_server_linux', f'{REMOTE_BASE}/server'),
    # 修改的 HTML 文件
    (f'{LOCAL_BASE}\\web\\login.html', f'{REMOTE_BASE}/web/login.html'),
    (f'{LOCAL_BASE}\\web\\register.html', f'{REMOTE_BASE}/web/register.html'),
    (f'{LOCAL_BASE}\\web\\forgot-password.html', f'{REMOTE_BASE}/web/forgot-password.html'),
    (f'{LOCAL_BASE}\\web\\reset-password.html', f'{REMOTE_BASE}/web/reset-password.html'),
    (f'{LOCAL_BASE}\\web\\activate.html', f'{REMOTE_BASE}/web/activate.html'),
    (f'{LOCAL_BASE}\\web\\share.html', f'{REMOTE_BASE}/web/share.html'),
    (f'{LOCAL_BASE}\\web\\admin.html', f'{REMOTE_BASE}/web/admin.html'),
    # 修改的 admin.js
    (f'{LOCAL_BASE}\\web\\admin.js', f'{REMOTE_BASE}/web/admin.js'),
    # 新建的 JS 文件
    (f'{LOCAL_BASE}\\web\\js\\login.js', f'{REMOTE_BASE}/web/js/login.js'),
    (f'{LOCAL_BASE}\\web\\js\\register.js', f'{REMOTE_BASE}/web/js/register.js'),
    (f'{LOCAL_BASE}\\web\\js\\forgot-password.js', f'{REMOTE_BASE}/web/js/forgot-password.js'),
    (f'{LOCAL_BASE}\\web\\js\\reset-password.js', f'{REMOTE_BASE}/web/js/reset-password.js'),
    (f'{LOCAL_BASE}\\web\\js\\activate.js', f'{REMOTE_BASE}/web/js/activate.js'),
]

TIMESTAMP = datetime.now().strftime('%Y%m%d%H%M%S')
BACKUP_BIN = f'{REMOTE_BASE}/server.bak.{TIMESTAMP}'

print('=== 步骤 1: 创建 web/js 目录（如果不存在）===')
out = run(f'mkdir -p {REMOTE_BASE}/web/js && ls -la {REMOTE_BASE}/web/js/')
print(out)

print('=== 步骤 2: 备份原二进制 ===')
out = run(f'cp {REMOTE_BASE}/server {BACKUP_BIN} && ls -la {BACKUP_BIN}')
print(out)

print('=== 步骤 3: 停止服务 ===')
out = run('systemctl stop filesync.service')
print('stopped')

print('=== 步骤 4: 上传所有文件 ===')
for local, remote in FILES:
    print(f'  上传: {local} -> {remote}')
    upload(local, remote)

print('=== 步骤 5: 添加执行权限 ===')
out = run(f'chmod +x {REMOTE_BASE}/server')
print('done')

print('=== 步骤 6: 启动服务 ===')
out = run('systemctl start filesync.service')
print('started')

print('=== 步骤 7: 检查服务状态 ===')
out = run('systemctl status filesync.service --no-pager | head -15')
print(out)

print('=== 步骤 8: 等待 3 秒后检查启动日志 ===')
time.sleep(3)
out = run('tail -20 /opt/filesync/server.log')
print(out)

print('=== 步骤 9: 验证 CSP 头 ===')
out = run('curl -sI https://aistudy.icu/api/health | grep -i content-security-policy')
print(out)

print('=== 步骤 10: 验证 Cache-Control 头 ===')
out = run('curl -sI https://aistudy.icu/api/health | grep -i cache-control')
print(out)

print('=== 步骤 11: 验证 login.html 无内联脚本 ===')
out = run('curl -s https://aistudy.icu/web/login.html | grep -c "<script>"')
print(f'内联 script 标签数: {out.strip()}')

print('=== 步骤 12: 验证 login.html 表单 method=POST ===')
out = run('curl -s https://aistudy.icu/web/login.html | grep -o \'method="[^"]*\' | head -1')
print(f'表单 method: {out.strip()}')

print('=== 部署完成 ===')
