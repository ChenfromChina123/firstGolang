"""
排查服务崩溃问题：检查服务状态、最近日志、端口监听、内存等。
"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== 1. 服务状态 ===')
print(run('systemctl status filesync --no-pager | head -20'))

print('\n=== 2. 最近 50 行日志 ===')
print(run('journalctl -u filesync -n 50 --no-pager'))

print('\n=== 3. 端口监听 ===')
print(run('ss -tlnp | grep -E ":(80|443) "'))

print('\n=== 4. 进程 ===')
print(run('ps aux | grep -E "filesync|server" | grep -v grep'))

print('\n=== 5. 内存/磁盘 ===')
print(run('free -m; df -h /opt'))

print('\n=== 6. 健康端点 ===')
print(run('curl -s -k -o /dev/null -w "HTTP_CODE:%{http_code} TIME:%{time_total}s\\n" https://aistudy.icu/api/health'))

print('\n=== 7. 主页 ===')
print(run('curl -s -k -o /dev/null -w "HTTP_CODE:%{http_code} TIME:%{time_total}s SIZE:%{size_download}\\n" https://aistudy.icu/'))

print('\n=== 8. 主页内容前 500 字节 ===')
print(run('curl -s -k https://aistudy.icu/ | head -c 500'))

print('\n=== 9. 最近修改时间 ===')
print(run('ls -la /opt/filesync/server /opt/filesync/web/app.js /opt/filesync/web/index.html'))
