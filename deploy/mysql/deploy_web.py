"""
部署脚本（仅前端）：上传 web/dist/ 压缩文件到 aistudy.icu 服务器。
后端不变时使用，无需停服/启服（前端是静态文件，直接覆盖即可）。

用法：
    python deploy_web.py            # 上传 web/dist/ 压缩版
    python deploy_web.py --source   # 上传 web/ 未压缩版（调试用）
"""
import sys
import os
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run, upload

REMOTE_DIR = '/opt/filesync'
WEB_DIR = f'{REMOTE_DIR}/web'

# 选择源目录：默认 dist（压缩版），--source 时用 web（未压缩版）
use_source = '--source' in sys.argv
if use_source:
    LOCAL_WEB = r'd:\STUDY\GO\StudyGolang\firstGolang\filesync\web'
    print('=== 部署未压缩版（web/）===')
else:
    LOCAL_WEB = r'd:\STUDY\GO\StudyGolang\firstGolang\filesync\web\dist'
    print('=== 部署压缩版（web/dist/）===')

# 需要上传的前端文件
web_files = [
    'style.css',
    'index.html',
    'app.js',
    'login.html',
    'register.html',
    'forgot-password.html',
    'reset-password.html',
    'activate.html',
    'share.html',
    'share.js',
]

# === 1. 上传前端文件 ===
print(f'\n=== 1. 上传前端文件到 {WEB_DIR}/ ===')
for name in web_files:
    local_path = os.path.join(LOCAL_WEB, name)
    if not os.path.exists(local_path):
        print(f'  跳过 {name}（本地不存在）')
        continue
    print(f'  上传 {name}...')
    print(upload(local_path, f'{WEB_DIR}/{name}'))

print(run(f'ls -la {WEB_DIR}/'))

# === 2. 健康检查 ===
print('\n=== 2. 健康检查 ===')
print(run('curl -s -k https://aistudy.icu/api/health'))

print('\n=== 前端部署完成 ===')
