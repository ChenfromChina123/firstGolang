"""检查生产服务器最新日志，定位用户 3301767269f775 的活动."""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

# 1. 查找日志文件位置
print('=== 1. 查找日志文件 ===')
print(run('find /opt/filesync -name "*.log" 2>/dev/null'))
print(run('ls -la /opt/filesync/data/ 2>/dev/null'))

# 2. systemd 服务状态
print('\n=== 2. systemd 服务状态 ===')
print(run('systemctl status filesync --no-pager | head -25'))

# 3. journalctl 最新 200 行
print('\n=== 3. journalctl 最新 200 行 ===')
print(run('journalctl -u filesync -n 200 --no-pager'))
