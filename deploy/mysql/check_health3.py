import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== health (aistudy.icu) ===')
print(run('curl -s -w "\nHTTP_CODE:%{http_code}" https://aistudy.icu/api/health'))

print('=== storage base path ===')
print(run('cat /etc/systemd/system/filesync.service | grep -E "WorkingDirectory|Environment|ExecStart"'))

print('=== find data dir ===')
print(run('ls -la /opt/filesync/ | head -20'))

print('=== find storage path in db ===')
print(run('find /opt/filesync -name "*.db" 2>/dev/null | head -5'))
