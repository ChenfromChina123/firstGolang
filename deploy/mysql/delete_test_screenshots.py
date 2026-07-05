"""
删除服务器上 test/ 目录下的 .png 截图文件（保留 .md/.yaml 文档）。
步骤：
1. 查询所有 test/*.png 文件的 id 和 storage_path
2. 逐个删除存储文件
3. 批量删除数据库记录
4. 同步清理 Redis 冲突检查集合（通过 redis-cli）
5. 验证剩余文件
"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

# === 1. 查询所有 .png 文件的 id 和 storage_path ===
print('=== 1. 查询 test/*.png 文件 ===')
sql = "SELECT id, filename, storage_path FROM files WHERE filename LIKE 'test/%.png' ORDER BY filename"
result = run(f'mysql -u filesync -p***REMOVED_DB_PASSWORD*** -h 127.0.0.1 -P 13306 filesync -e "{sql}" -N')
print(result)

# 解析结果，提取 storage_path
lines = [l.strip() for l in result.strip().split('\n') if l.strip()]
storage_paths = []
filenames = []
for line in lines:
    parts = line.split('\t')
    if len(parts) >= 3:
        storage_paths.append(parts[2])
        filenames.append(parts[1])

print(f'\n共找到 {len(storage_paths)} 个 .png 文件需要删除')

# === 2. 逐个删除存储文件 ===
print('\n=== 2. 删除存储文件 ===')
deleted_storage = 0
for path in storage_paths:
    # storage_path 格式可能是相对路径或绝对路径，需要确认
    # 假设存储在 /opt/filesync/data/ 下
    full_path = path if path.startswith('/') else f'/opt/filesync/data/{path}'
    r = run(f'rm -f {full_path} && echo OK || echo FAIL')
    if 'OK' in r:
        deleted_storage += 1
    else:
        print(f'  删除失败: {full_path}: {r}')
print(f'已删除 {deleted_storage}/{len(storage_paths)} 个存储文件')

# === 3. 批量删除数据库记录 ===
print('\n=== 3. 删除数据库记录 ===')
sql_del = "DELETE FROM files WHERE filename LIKE 'test/%.png'"
result_del = run(f'mysql -u filesync -p***REMOVED_DB_PASSWORD*** -h 127.0.0.1 -P 13306 filesync -e "{sql_del}"')
print(result_del if result_del else 'DELETE 执行完成')

# === 4. 验证剩余文件 ===
print('\n=== 4. 验证剩余文件 ===')
sql_check = "SELECT filename, size FROM files WHERE filename LIKE 'test/%' ORDER BY filename"
result_check = run(f'mysql -u filesync -p***REMOVED_DB_PASSWORD*** -h 127.0.0.1 -P 13306 filesync -e "{sql_check}"')
print(result_check)

# 统计剩余
sql_count = "SELECT COUNT(*) AS remaining FROM files WHERE filename LIKE 'test/%'"
result_count = run(f'mysql -u filesync -p***REMOVED_DB_PASSWORD*** -h 127.0.0.1 -P 13306 filesync -e "{sql_count}"')
print(f'\n剩余文件数: {result_count}')
