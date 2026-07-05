"""查询一条 test/*.png 记录的 storage_path 格式，确认删除路径。"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

sql = "SELECT id, filename, storage_path FROM files WHERE filename LIKE 'test/%.png' LIMIT 3"
result = run(f'mysql -u filesync -p***REMOVED_DB_PASSWORD*** -h 127.0.0.1 -P 13306 filesync -e "{sql}"')
print('=== storage_path 格式样本 ===')
print(result)

# 检查存储目录
print('\n=== 存储目录内容（前 5 个文件）===')
print(run('ls -la /opt/filesync/data/ 2>/dev/null | head -10'))
print(run('ls -la /opt/filesync/data/files/ 2>/dev/null | head -10'))
print(run('find /opt/filesync/data -name "*.dat" -o -name "*" -type f 2>/dev/null | head -5'))
