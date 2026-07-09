import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== service status ===')
print(run('systemctl status filesync --no-pager -l'))

print('=== service full log ===')
print(run('journalctl -u filesync --no-pager -n 50 --no-pager'))

print('=== server.log tail ===')
print(run('tail -30 /opt/filesync/server.log 2>/dev/null'))

print('=== try run binary directly ===')
print(run('cd /opt/filesync && timeout 3 ./server 2>&1 | head -20'))
