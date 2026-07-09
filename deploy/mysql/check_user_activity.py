"""检查用户 3301767269f775 的所有活动"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print("=== 1. 用户 3301767269f775 的所有日志 ===")
print(run('grep "3301767269f775" /opt/filesync/server.log'))

print("\n=== 2. 13:01:00 到 13:10:00 的所有日志（按时间范围） ===")
print(run('sed -n "/2026\\/07\\/07 13:0[1-9]/,/^$/p" /opt/filesync/server.log | head -100'))

print("\n=== 3. 检查 IP 111.59.118.225 的所有请求 ===")
print(run('grep "111.59.118.225" /opt/filesync/server.log'))

print("\n=== 4. 检查最近的 upload 请求 ===")
print(run('grep -i "upload" /opt/filesync/server.log | tail -20'))

print("\n=== 5. 检查 /api/files 请求 ===")
print(run('grep "/api/files" /opt/filesync/server.log | tail -20'))

print("\n=== 6. 数据库中该用户的文件 ===")
print(run('sqlite3 /opt/filesync/data/filesync.db "SELECT id, username, role FROM users WHERE id LIKE \"3301%\";"'))
