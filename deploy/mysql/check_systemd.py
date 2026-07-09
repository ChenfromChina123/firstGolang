"""
检查 systemd 配置中的 DOMAIN 和 EXTRA_DOMAINS 环境变量。
"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== 1. systemd 配置文件 ===')
print(run('cat /etc/systemd/system/filesync.service'))

print('\n=== 2. 当前进程的环境变量 ===')
print(run('cat /proc/$(pgrep -f "/opt/filesync/server")/environ 2>/dev/null | tr "\\0" "\\n" | grep -E "DOMAIN|EXTRA"'))

print('\n=== 3. 证书缓存目录 ===')
print(run('ls -la /opt/filesync/certs/ 2>/dev/null'))
