import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== health (localhost:80) ===')
print(run('curl -s -w "\nHTTP_CODE:%{http_code}" http://localhost:80/api/health'))

print('=== health (https) ===')
print(run('curl -sk -w "\nHTTP_CODE:%{http_code}" https://localhost:443/api/health'))

print('=== check port 80 and 443 ===')
print(run('ss -tlnp | grep -E ":80|:443"'))

print('=== check _posters dir ===')
print(run('ls -la /opt/filesync/data/_posters/ 2>&1; ls -la /opt/filesync/_posters/ 2>&1'))

print('=== check _thumbnails dir ===')
print(run('ls -la /opt/filesync/data/_thumbnails/ 2>&1 | head -5'))
