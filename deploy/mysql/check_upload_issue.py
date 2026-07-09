"""检查生产服务器日志，排查上传后刷新不显示的问题"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print("=== 1. server.log 最近 50 行 ===")
print(run('tail -50 /opt/filesync/server.log'))

print("\n=== 2. 检查最近的错误日志 ===")
print(run('grep -iE "error|panic|fatal|fail|exception" /opt/filesync/server.log | tail -20'))

print("\n=== 3. 检查最近的上传/文件操作日志 ===")
print(run('grep -iE "upload|file|list" /opt/filesync/server.log | tail -20'))

print("\n=== 4. 检查服务状态 ===")
print(run('systemctl status filesync --no-pager | head -15'))

print("\n=== 5. 检查磁盘空间 ===")
print(run('df -h /opt/filesync/'))

print("\n=== 6. 检查 data 目录权限 ===")
print(run('ls -la /opt/filesync/data/ | head -20'))

print("\n=== 7. 检查数据库文件 ===")
print(run('ls -la /opt/filesync/data/*.db 2>/dev/null'))
