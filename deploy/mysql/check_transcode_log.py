"""检查生产服务器转码日志"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print("=== 1. server.log 最近的 transcode/preview 条目 ===")
print(run('grep -E "transcode|preview" /opt/filesync/server.log | tail -20'))

print("\n=== 2. _transcoded 目录内容 ===")
print(run('ls -la /opt/filesync/data/_transcoded/*/ 2>/dev/null | head -30'))

print("\n=== 3. 检查 42ae473e 文件的转码缓存 ===")
print(run('ls -la /opt/filesync/data/_transcoded/42ae473e67127b11686dc3c4676c72ff/ 2>/dev/null'))
