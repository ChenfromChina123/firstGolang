"""检查部署后的服务状态、日志、端口和健康端点。"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== 服务状态 ===')
print(run('systemctl status filesync --no-pager -l | head -15'))

print('=== 最近日志 ===')
print(run('journalctl -u filesync --no-pager -n 30'))

print('=== 端口监听 ===')
print(run('ss -tlnp | grep -E ":80 |:443 |:8080 "'))

print('=== HTTP 健康检查 (localhost:8080) ===')
print(run('curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/api/health'))
print()

print('=== HTTPS 健康检查 (localhost) ===')
print(run('curl -s -k https://localhost/api/health'))

print('=== HTTPS 健康检查 (aistudy.icu) ===')
print(run('curl -s -k https://aistudy.icu/api/health'))
