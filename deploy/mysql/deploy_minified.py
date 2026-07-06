"""
部署压缩版前端到 aistudy.icu 服务器。

流程：
1. 运行 node tools/minify/minify.js 生成 web/dist/（压缩后的前端文件）
2. 停止 filesync 服务
3. 备份当前 server
4. 上传新的 server 二进制
5. 上传 web/dist/ 中的压缩版前端文件
6. 启动 filesync 服务
7. 健康检查

前置条件：
- server_linux 已编译（GOOS=linux GOARCH=amd64 go build -o server_linux ./cmd/server）
- Node.js 已安装，tools/minify/ 下已 npm install

用法：python deploy/mysql/deploy_minified.py
"""
import sys
import os
import subprocess
import time

sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run, upload

# 路径配置
PROJECT_ROOT = r'd:\STUDY\GO\StudyGolang\firstGolang\filesync'
MINIFY_SCRIPT = os.path.join(PROJECT_ROOT, 'tools', 'minify', 'minify.js')
REMOTE_DIR = '/opt/filesync'
WEB_DIR = f'{REMOTE_DIR}/web'
LOCAL_SERVER = os.path.join(PROJECT_ROOT, 'server_linux')
LOCAL_WEB_DIST = os.path.join(PROJECT_ROOT, 'web', 'dist')

# 需要上传的 web 文件清单
WEB_FILES = [
    'style.css', 'index.html', 'app.js',
    'login.html', 'register.html', 'forgot-password.html',
    'reset-password.html', 'activate.html',
    'share.html', 'share.js',
]


def run_minify():
    """运行前端压缩脚本，生成 web/dist/"""
    print('=== 0. 运行前端压缩 ===')
    if not os.path.exists(MINIFY_SCRIPT):
        print(f'  错误：minify 脚本不存在: {MINIFY_SCRIPT}')
        sys.exit(1)
    result = subprocess.run(
        ['node', 'minify.js'],
        cwd=os.path.dirname(MINIFY_SCRIPT),
        capture_output=True, text=True
    )
    print(result.stdout)
    if result.returncode != 0:
        print(f'  压缩失败: {result.stderr}')
        sys.exit(1)
    if not os.path.exists(LOCAL_WEB_DIST):
        print(f'  错误：dist 目录未生成: {LOCAL_WEB_DIST}')
        sys.exit(1)
    print('  压缩文件已生成\n')


def deploy():
    """部署到服务器"""
    # 1. 停止服务
    print('=== 1. 停止 filesync 服务 ===')
    print(run('systemctl stop filesync'))
    time.sleep(2)

    # 2. 备份当前 server
    print('\n=== 2. 备份当前 server ===')
    print(run(f'cp {REMOTE_DIR}/server {REMOTE_DIR}/server.bak.$(date +%Y%m%d%H%M%S) 2>/dev/null; ls -la {REMOTE_DIR}/server'))

    # 3. 上传新的 server
    print('\n=== 3. 上传新的 server ===')
    print(run(f'rm -f {REMOTE_DIR}/server'))
    print(upload(LOCAL_SERVER, f'{REMOTE_DIR}/server'))
    print(run(f'chmod +x {REMOTE_DIR}/server && ls -la {REMOTE_DIR}/server'))

    # 4. 上传压缩版 web 文件
    print('\n=== 4. 上传压缩版 web 文件（从 web/dist/）===')
    for fname in WEB_FILES:
        local_path = os.path.join(LOCAL_WEB_DIST, fname)
        if not os.path.exists(local_path):
            print(f'  警告：{fname} 不存在于 dist/，跳过')
            continue
        print(f'  上传 {fname}...')
        print(upload(local_path, f'{WEB_DIR}/{fname}'))
    print(run(f'ls -la {WEB_DIR}/'))

    # 5. 启动服务
    print('\n=== 5. 启动 filesync 服务 ===')
    print(run('systemctl start filesync'))
    time.sleep(3)
    print(run('systemctl status filesync --no-pager | head -10'))

    # 6. 健康检查
    print('\n=== 6. 健康检查 ===')
    print(run('curl -s -k https://aistudy.icu/api/health'))

    print('\n=== 部署完成 ===')


if __name__ == '__main__':
    run_minify()
    deploy()
