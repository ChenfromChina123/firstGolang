import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== health ===')
print(run('curl -s -w "\nHTTP_CODE:%{http_code}" http://localhost:8080/api/health'))

print('=== service log (last 20 lines) ===')
print(run('journalctl -u filesync --no-pager -n 20'))

print('=== check port 8080 ===')
print(run('ss -tlnp | grep 8080'))
