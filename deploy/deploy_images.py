"""部署网站图片资源到生产服务器
- 创建 /opt/filesync/web/img 目录
- 上传 favicon.svg / logo.svg / banner.svg
- 上传修改的 8 个 HTML 文件（添加 favicon + logo 引用）
- 上传修改的 style.css（brand-banner 样式）
- 验证 SVG 文件可访问 + HTML favicon 引用
"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run, upload

LOCAL_BASE = r'd:\STUDY\GO\StudyGolang\firstGolang\filesync'
REMOTE_BASE = '/opt/filesync'

FILES = [
    # 新建 SVG 图片资源
    (f'{LOCAL_BASE}\\web\\img\\favicon.svg', f'{REMOTE_BASE}/web/img/favicon.svg'),
    (f'{LOCAL_BASE}\\web\\img\\logo.svg', f'{REMOTE_BASE}/web/img/logo.svg'),
    (f'{LOCAL_BASE}\\web\\img\\banner.svg', f'{REMOTE_BASE}/web/img/banner.svg'),
    # 修改的 8 个 HTML 文件
    (f'{LOCAL_BASE}\\web\\login.html', f'{REMOTE_BASE}/web/login.html'),
    (f'{LOCAL_BASE}\\web\\register.html', f'{REMOTE_BASE}/web/register.html'),
    (f'{LOCAL_BASE}\\web\\forgot-password.html', f'{REMOTE_BASE}/web/forgot-password.html'),
    (f'{LOCAL_BASE}\\web\\reset-password.html', f'{REMOTE_BASE}/web/reset-password.html'),
    (f'{LOCAL_BASE}\\web\\activate.html', f'{REMOTE_BASE}/web/activate.html'),
    (f'{LOCAL_BASE}\\web\\share.html', f'{REMOTE_BASE}/web/share.html'),
    (f'{LOCAL_BASE}\\web\\admin.html', f'{REMOTE_BASE}/web/admin.html'),
    (f'{LOCAL_BASE}\\web\\index.html', f'{REMOTE_BASE}/web/index.html'),
    # 修改的 style.css
    (f'{LOCAL_BASE}\\web\\style.css', f'{REMOTE_BASE}/web/style.css'),
]

print('=== 步骤 1: 创建 web/img 目录 ===')
out = run(f'mkdir -p {REMOTE_BASE}/web/img && ls -la {REMOTE_BASE}/web/img/')
print(out)

print('=== 步骤 2: 上传所有文件 ===')
for local, remote in FILES:
    print(f'  上传: {local} -> {remote}')
    upload(local, remote)

print('=== 步骤 3: 验证 SVG 文件可访问 ===')
for svg in ['favicon.svg', 'logo.svg', 'banner.svg']:
    out = run(f'curl -sI https://aistudy.icu/web/img/{svg} | head -1')
    print(f'  {svg}: {out.strip()}')

print('=== 步骤 4: 验证 HTML favicon 引用 ===')
out = run('curl -s https://aistudy.icu/web/login.html | grep -c favicon.svg')
print(f'  login.html favicon 引用数: {out.strip()}')
out = run('curl -s https://aistudy.icu/web/index.html | grep -c favicon.svg')
print(f'  index.html favicon 引用数: {out.strip()}')

print('=== 步骤 5: 验证 HTML logo 引用 ===')
out = run('curl -s https://aistudy.icu/web/login.html | grep -c logo.svg')
print(f'  login.html logo 引用数: {out.strip()}')
out = run('curl -s https://aistudy.icu/web/index.html | grep -c logo.svg')
print(f'  index.html logo 引用数: {out.strip()}')

print('=== 步骤 6: 验证 index.html banner 引用 ===')
out = run('curl -s https://aistudy.icu/web/index.html | grep -c banner.svg')
print(f'  index.html banner 引用数: {out.strip()}')

print('=== 部署完成 ===')
