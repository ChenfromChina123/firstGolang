"""
部署脚本：上传修复 .part.mp4 竞态条件的 server 二进制到生产服务器。
- 停止服务（释放文件锁）
- 备份当前 server
- 上传新的 server
- 启动 filesync 服务
- 检查服务状态
"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run, upload
import time

REMOTE_DIR = '/opt/filesync'
LOCAL_SERVER = r'd:\STUDY\GO\StudyGolang\firstGolang\filesync\server_linux'

# === 1. 停止服务（释放文件锁，避免上传失败）===
print('=== 1. 停止 filesync 服务 ===')
print(run('systemctl stop filesync'))
time.sleep(2)

# === 2. 备份当前 server ===
print('\n=== 2. 备份当前 server ===')
print(run(f'cp {REMOTE_DIR}/server {REMOTE_DIR}/server.bak.$(date +%Y%m%d%H%M%S) 2>/dev/null; ls -la {REMOTE_DIR}/server*'))

# === 3. 上传新的 server ===
print('\n=== 3. 上传新的 server（修复 .part.mp4 竞态条件）===')
print(run(f'rm -f {REMOTE_DIR}/server'))
print(upload(LOCAL_SERVER, f'{REMOTE_DIR}/server'))
print(run(f'chmod +x {REMOTE_DIR}/server && ls -la {REMOTE_DIR}/server'))

# === 4. 上传 web 文件（app.js 含异步转码 + 进度条 UI，style.css 含进度条样式）===
print('\n=== 4. 上传 web 文件 ===')
WEB_DIR = f'{REMOTE_DIR}/web'
web_files = [
    ('style.css', 'style.css'),
    ('index.html', 'index.html'),
    ('app.js', 'app.js'),
]
for local_name, remote_name in web_files:
    local_path = rf'd:\STUDY\GO\StudyGolang\firstGolang\filesync\web\{local_name}'
    print(f'  uploading {local_name}...')
    print(upload(local_path, f'{WEB_DIR}/{remote_name}'))
# 版本号已在本地 index.html 中更新为 v=20260709b，上传后自动生效

# === 5. 启动 filesync 服务 ===
print('\n=== 5. 启动 filesync 服务 ===')
print(run('systemctl start filesync'))
time.sleep(3)

# === 6. 检查服务状态 ===
print('\n=== 6. 检查服务状态 ===')
print(run('systemctl status filesync --no-pager | head -15'))

# === 7. 健康检查 ===
print('\n=== 7. 健康检查 ===')
print(run('curl -s http://localhost:8080/api/health'))

# === 8. 检查 ffmpeg 可用性 ===
print('\n=== 8. 检查 ffmpeg 可用性 ===')
print(run('which ffmpeg && ffmpeg -version 2>&1 | head -3'))

print('\n=== 部署完成 ===')
