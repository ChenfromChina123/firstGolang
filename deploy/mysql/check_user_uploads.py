"""检查用户 3301767269f775 的上传活动，重点关注 13:01 之后的请求."""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

# 1. server.log 最新 200 行（13:00 之后的所有日志）
print('=== 1. server.log 最新 200 行 ===')
print(run('tail -n 200 /opt/filesync/server.log'))

# 2. 检查 _chunks 目录
print('\n=== 2. _chunks 目录内容 ===')
print(run('ls -la /opt/filesync/data/_chunks/ 2>/dev/null | head -30'))
print(run('find /opt/filesync/data/_chunks -type d -newer /opt/filesync/server.log 2>/dev/null | head -20'))

# 3. 检查 13:00 之后的所有日志
print('\n=== 3. 13:00 之后所有 3301767269f775 相关日志 ===')
print(run('grep "3301767269f775" /opt/filesync/server.log | tail -50'))

# 4. 检查 13:00 之后的 upload / complete / files 请求
print('\n=== 4. 13:00 之后的 upload/complete/files 请求 ===')
print(run('awk \'/2026\\/07\\/07 13:/,/2026\\/07\\/07 23:/\' /opt/filesync/server.log | grep -E "upload|complete|/api/files|chunk" | head -50'))

# 5. 检查最近 5 分钟的日志
print('\n=== 5. 最近 5 分钟的所有日志 ===')
print(run('tail -n 50 /opt/filesync/server.log'))
