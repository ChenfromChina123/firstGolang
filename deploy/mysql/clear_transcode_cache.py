"""删除 42ae473e 的 medium.mp4 缓存，用于验证异步转码流程"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run
import time

print("=== 删除前的缓存目录 ===")
print(run('ls -la /opt/filesync/data/_transcoded/42ae473e67127b11686dc3c4676c72ff/'))

print("\n=== 删除 medium.mp4 ===")
print(run('rm -f /opt/filesync/data/_transcoded/42ae473e67127b11686dc3c4676c72ff/medium.mp4'))

print("\n=== 删除后的缓存目录 ===")
print(run('ls -la /opt/filesync/data/_transcoded/42ae473e67127b11686dc3c4676c72ff/'))
