"""
部署脚本：上传新的 server_linux 二进制和 web 文件到 aistudy.icu 服务器。
- 备份当前 server_linux
- 上传新的 server_linux
- 上传 web 文件（style.css, index.html, app.js）
- 重启 filesync 服务
- 检查服务状态
"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run, upload

REMOTE_DIR = '/opt/filesync'
WEB_DIR = f'{REMOTE_DIR}/web'

# === 1. 备份当前 server_linux ===
print('=== 1. 备份当前 server_linux ===')
print(run(f'cp {REMOTE_DIR}/server_linux {REMOTE_DIR}/server_linux.bak.$(date +%Y%m%d%H%M%S) && ls -la {REMOTE_DIR}/server_linux*'))

# === 2. 上传新的 server_linux ===
print('\n=== 2. 上传新的 server_linux ===')
print(upload(r'd:\STUDY\GO\StudyGolang\firstGolang\filesync\server_linux', f'{REMOTE_DIR}/server_linux'))
print(run(f'chmod +x {REMOTE_DIR}/server_linux && ls -la {REMOTE_DIR}/server_linux'))

# === 3. 上传 web 文件 ===
print('\n=== 3. 上传 web 文件 ===')
web_files = [
    ('style.css', 'style.css'),
    ('index.html', 'index.html'),
    ('app.js', 'app.js'),
]
for local_name, remote_name in web_files:
    local_path = rf'd:\STUDY\GO\StudyGolang\firstGolang\filesync\web\{local_name}'
    print(f'  上传 {local_name}...')
    print(upload(local_path, f'{WEB_DIR}/{remote_name}'))

# 确认 web 文件
print(run(f'ls -la {WEB_DIR}/'))

# === 4. 重启 filesync 服务 ===
print('\n=== 4. 重启 filesync 服务 ===')
print(run('systemctl restart filesync'))
import time
time.sleep(2)
print(run('systemctl status filesync --no-pager -l | head -20'))

# === 5. 健康检查 ===
print('\n=== 5. 健康检查 ===')
print(run('curl -s http://localhost:8080/api/health'))

print('\n=== 部署完成 ===')
