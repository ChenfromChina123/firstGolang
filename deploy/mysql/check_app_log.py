"""
检查应用自身日志、最近的请求错误。
"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== 1. 查找应用日志文件 ===')
print(run('find /opt/filesync -name "*.log" -mmin -60 2>/dev/null; ls -la /var/log/filesync* 2>/dev/null'))

print('\n=== 2. journalctl 最近的 stdout/stderr ===')
print(run('journalctl -u filesync -n 100 --no-pager -o cat'))

print('\n=== 3. 检查 nginx/apache 访问日志（如果有反代） ===')
print(run('ls -la /www/wwwlogs/ 2>/dev/null | head -20'))

print('\n=== 4. 检查 /var/log 下最近修改的日志 ===')
print(run('find /var/log -name "*.log" -mmin -30 2>/dev/null | head -10'))

print('\n=== 5. 测试登录页 ===')
print(run('curl -s -k -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}\\n" https://aistudy.icu/web/login.html'))

print('\n=== 6. 测试 share.html ===')
print(run('curl -s -k -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}\\n" https://aistudy.icu/web/share.html'))

print('\n=== 7. 服务器本地 curl 主页（绕过网络） ===')
print(run('curl -s -k -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}\\n" https://localhost/web/index.html'))

print('\n=== 8. 检查 share.js 是否可访问（前面列表没看到 share.js，但 deploy_v2 上传了） ===')
print(run('ls -la /opt/filesync/web/share.js 2>&1; curl -s -k -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}\\n" https://aistudy.icu/web/share.js'))

print('\n=== 9. 检查 share.html 引用的资源 ===')
print(run('grep -E "src=|href=" /opt/filesync/web/share.html | head -20'))

print('\n=== 10. 检查 index.html 引用的资源 ===')
print(run('grep -E "src=|href=" /opt/filesync/web/index.html | head -20'))
