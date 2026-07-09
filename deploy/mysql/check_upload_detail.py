"""检查用户登录后的所有请求"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print("=== 1. 13:00 之后的所有日志 ===")
print(run('awk \'/2026\\/07\\/07 13:/{print}\' /opt/filesync/server.log'))

print("\n=== 2. 13:00 之后的所有请求（包含 method 和 path） ===")
print(run('grep -E "13:[0-9]{2}:" /opt/filesync/server.log | head -50'))

print("\n=== 3. 检查 Referer 拦截 ===")
print(run('grep "Referer.*blocked" /opt/filesync/server.log | tail -10'))

print("\n=== 4. 检查 Auth 失败 ===")
print(run('grep -E "Auth.*fail|token.*invalid|unauthorized" /opt/filesync/server.log | tail -10'))

print("\n=== 5. 检查 403/401 响应 ===")
print(run('grep -E "403|401" /opt/filesync/server.log | tail -10'))
