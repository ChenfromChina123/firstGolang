"""上传 prism 库到服务器"""
import sys
import os
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run, upload

LOCAL_PRISM = r'd:\STUDY\GO\StudyGolang\firstGolang\filesync\web\lib\prism'
REMOTE_PRISM = '/opt/filesync/web/lib/prism'

# 创建远程目录
print('=== 创建远程 prism 目录 ===')
print(run(f'mkdir -p {REMOTE_PRISM}'))

# 上传所有文件
print('\n=== 上传 prism 文件 ===')
files = os.listdir(LOCAL_PRISM)
for f in files:
    local_path = os.path.join(LOCAL_PRISM, f)
    if os.path.isfile(local_path):
        remote_path = f'{REMOTE_PRISM}/{f}'
        print(f'  uploading {f}...')
        print(upload(local_path, remote_path))

# 验证
print('\n=== 验证上传 ===')
print(run(f'ls -la {REMOTE_PRISM}/ | head -20'))

# 验证 prism.min.js 可访问
print('\n=== 验证 prism.min.js HTTP 可访问 ===')
print(run('curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}" https://aistudy.icu/web/lib/prism/prism.min.js'))
