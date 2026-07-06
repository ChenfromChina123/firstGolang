"""
部署脚本：上传新的 server 二进制和 web 文件到 aistudy.icu 服务器。
重要：systemd ExecStart=/opt/filesync/server，必须上传到 server（不是 server_linux）。
- 停止服务（释放文件锁，避免上传失败）
- 备份当前 server
- 上传新的 server（覆盖旧文件）
- 上传 web 文件（8 个 HTML/CSS/JS：含登录/注册/忘记密码/重置密码/激活页）
- 启动 filesync 服务
- 检查服务状态和健康端点
"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run, upload
import time

REMOTE_DIR = '/opt/filesync'
WEB_DIR = f'{REMOTE_DIR}/web'
LOCAL_SERVER = r'd:\STUDY\GO\StudyGolang\firstGolang\filesync\server_linux'

# === 1. 停止服务（释放文件锁，避免上传失败）===
print('=== 1. 停止 filesync 服务 ===')
print(run('systemctl stop filesync'))
time.sleep(2)

# === 2. 备份当前 server ===
print('\n=== 2. 备份当前 server ===')
print(run(f'cp {REMOTE_DIR}/server {REMOTE_DIR}/server.bak.$(date +%Y%m%d%H%M%S) 2>/dev/null; ls -la {REMOTE_DIR}/server*'))

# === 3. 上传新的 server（覆盖旧文件）===
print('\n=== 3. 上传新的 server ===')
print(run(f'rm -f {REMOTE_DIR}/server'))
print(upload(LOCAL_SERVER, f'{REMOTE_DIR}/server'))
print(run(f'chmod +x {REMOTE_DIR}/server && ls -la {REMOTE_DIR}/server'))

# === 4. 上传 web 文件 ===
print('\n=== 4. 上传 web 文件 ===')
web_files = [
    ('style.css', 'style.css'),
    ('index.html', 'index.html'),
    ('app.js', 'app.js'),
    ('login.html', 'login.html'),
    ('register.html', 'register.html'),
    ('forgot-password.html', 'forgot-password.html'),
    ('reset-password.html', 'reset-password.html'),
    ('activate.html', 'activate.html'),
    ('share.html', 'share.html'),
    ('share.js', 'share.js'),
]
for local_name, remote_name in web_files:
    local_path = rf'd:\STUDY\GO\StudyGolang\firstGolang\filesync\web\{local_name}'
    print(f'  上传 {local_name}...')
    print(upload(local_path, f'{WEB_DIR}/{remote_name}'))

print(run(f'ls -la {WEB_DIR}/'))

# === 5. 启动 filesync 服务 ===
print('\n=== 5. 启动 filesync 服务 ===')
print(run('systemctl start filesync'))
time.sleep(3)
print(run('systemctl status filesync --no-pager | head -10'))

# === 6. 健康检查 ===
print('\n=== 6. 健康检查 ===')
print(run('curl -s -k https://aistudy.icu/api/health'))

print('\n=== 部署完成 ===')
