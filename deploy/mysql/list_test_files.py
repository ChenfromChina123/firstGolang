"""查询服务器上 test/ 目录下的所有文件，分类统计 .png 截图和 .md/.yaml 文档。"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

# 查询 test/ 目录下所有文件
sql = "SELECT id, filename, size, created_at FROM files WHERE filename LIKE 'test/%' ORDER BY filename"
result = run(f'mysql -u filesync -p***REMOVED_DB_PASSWORD*** -h 127.0.0.1 -P 13306 filesync -e "{sql}"')
print('=== test/ 目录所有文件 ===')
print(result)

# 按扩展名分类统计
sql2 = "SELECT SUBSTRING_INDEX(filename, '.', -1) AS ext, COUNT(*) AS cnt, SUM(size) AS total_size FROM files WHERE filename LIKE 'test/%' GROUP BY ext ORDER BY cnt DESC"
result2 = run(f'mysql -u filesync -p***REMOVED_DB_PASSWORD*** -h 127.0.0.1 -P 13306 filesync -e "{sql2}"')
print('\n=== 按扩展名分类统计 ===')
print(result2)
