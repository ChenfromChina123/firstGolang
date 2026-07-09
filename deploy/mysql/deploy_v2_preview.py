"""
部署脚本：上传第二阶段预览功能的 server 二进制和 web 文件到 aistudy.icu 服务器。
- 停止服务（释放文件锁）
- 备份当前 server
- 上传新的 server（覆盖旧文件）
- 上传 web 文件（app.js, index.html, style.css, marked.min.js）
- 启动 filesync 服务
- 检查服务状态和 ffmpeg 可用性
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
]
for local_name, remote_name in web_files:
    local_path = rf'd:\STUDY\GO\StudyGolang\firstGolang\filesync\web\{local_name}'
    print(f'  uploading {local_name}...')
    print(upload(local_path, f'{WEB_DIR}/{remote_name}'))

# === 5. 创建 marked 目录并上传 marked.min.js ===
print('\n=== 5. 上传 marked.min.js ===')
print(run(f'mkdir -p {WEB_DIR}/lib/marked'))
marked_local = r'd:\STUDY\GO\StudyGolang\firstGolang\filesync\web\lib\marked\marked.min.js'
print(upload(marked_local, f'{WEB_DIR}/lib/marked/marked.min.js'))
print(run(f'ls -la {WEB_DIR}/lib/marked/'))

# === 6. 启动 filesync 服务 ===
print('\n=== 6. 启动 filesync 服务 ===')
print(run('systemctl start filesync'))
time.sleep(3)

# === 7. 检查服务状态 ===
print('\n=== 7. 检查服务状态 ===')
print(run('systemctl status filesync --no-pager | head -15'))

# === 8. 检查 ffmpeg 可用性 ===
print('\n=== 8. 检查 ffmpeg 可用性 ===')
print(run('which ffmpeg && ffmpeg -version 2>&1 | head -3'))

# === 9. 健康检查 ===
print('\n=== 9. 健康检查 ===')
print(run('curl -s http://localhost:8080/api/health'))

print('\n=== 部署完成 ===')
