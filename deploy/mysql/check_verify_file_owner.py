"""检查 13:01 上传的 verify-test 文件的所有者，以及数据库中所有 13:00 之后的文件记录."""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

DB_CMD = 'mysql -h 127.0.0.1 -P 13306 -u filesync -p***REMOVED_DB_PASSWORD*** filesync'

# 1. 查询 ID 为 93b782158ffae6ada4a6156e8a9129b7 的文件
print('=== 1. 查询 13:01 上传的 verify-test 文件 ===')
sql = "SELECT id, filename, size, owner, created_at FROM files WHERE id='93b782158ffae6ada4a6156e8a9129b7'"
print(run(f'{DB_CMD} -e "{sql}" 2>&1'))

# 2. 查询 13:00 之后创建的所有文件（不限用户）
print('\n=== 2. 数据库中所有 13:00 之后创建的文件 ===')
sql2 = "SELECT id, filename, size, owner, created_at FROM files WHERE created_at >= '2026-07-07 13:00:00' ORDER BY created_at DESC"
print(run(f'{DB_CMD} -e "{sql2}" 2>&1'))

# 3. 通过文件名 verify-test 查询
print('\n=== 3. 通过文件名 verify-test 查询 ===')
sql3 = "SELECT id, filename, size, owner, created_at FROM files WHERE filename LIKE 'verify-test%'"
print(run(f'{DB_CMD} -e "{sql3}" 2>&1'))

# 4. 看用户 3301767269f775 的最新活跃文件（按时间倒序，前 5 个）
print('\n=== 4. 用户 3301767269f775 的最新 5 个活跃文件 ===')
sql4 = "SELECT id, filename, size, created_at FROM files WHERE owner='3301767269f775' AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 5"
print(run(f'{DB_CMD} -e "{sql4}" 2>&1'))

# 5. 检查 admin 的文件列表
print('\n=== 5. admin 用户的所有文件统计 ===')
sql5 = "SELECT COUNT(*) AS total, MAX(created_at) AS latest FROM files WHERE owner='admin' AND deleted_at IS NULL"
print(run(f'{DB_CMD} -e "{sql5}" 2>&1'))

# 6. 列出 11:14:38 左右的会话对应的文件
print('\n=== 6. 11:14:38 上传会话对应的文件（admin 视角下的 3/ 截图文件）===')
sql6 = "SELECT id, filename, size, owner, created_at FROM files WHERE created_at >= '2026-07-07 11:14:00' AND created_at <= '2026-07-07 11:15:00' LIMIT 20"
print(run(f'{DB_CMD} -e "{sql6}" 2>&1'))

# 7. 验证：admin 视角下看到了 verify-test-1783400468.txt，但 owner 是谁？
print('\n=== 7. admin 视角下 13:01 上传的 verify-test 文件的 owner ===')
sql7 = "SELECT id, filename, size, owner, created_at FROM files WHERE filename LIKE '%verify-test%' OR filename LIKE '%1783400468%'"
print(run(f'{DB_CMD} -e "{sql7}" 2>&1'))
