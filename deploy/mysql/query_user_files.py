"""查询 MySQL 数据库，检查用户 3301767269f775 的所有文件记录."""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

DB_CMD = 'mysql -h 127.0.0.1 -P 13306 -u filesync -p***REMOVED_DB_PASSWORD*** filesync'

# 1. 列出该用户的所有文件（按创建时间倒序，前 20 条）
print('=== 1. 用户 3301767269f775 的所有文件（按创建时间倒序，前 20 条）===')
sql = "SELECT id, filename, file_size, owner, created_at, deleted_at FROM files WHERE owner='3301767269f775' ORDER BY created_at DESC LIMIT 20"
print(run(f'{DB_CMD} -e "{sql}" 2>&1'))

# 2. 列出该用户在 13:00 之后创建的文件
print('\n=== 2. 用户在 2026-07-07 13:00 之后创建的文件 ===')
sql2 = "SELECT id, filename, file_size, owner, created_at, deleted_at FROM files WHERE owner='3301767269f775' AND created_at >= '2026-07-07 13:00:00' ORDER BY created_at DESC"
print(run(f'{DB_CMD} -e "{sql2}" 2>&1'))

# 3. 查询数据库中所有 13:00 之后创建的文件（不限用户）
print('\n=== 3. 数据库中所有 2026-07-07 13:00 之后创建的文件 ===')
sql3 = "SELECT id, filename, file_size, owner, created_at, deleted_at FROM files WHERE created_at >= '2026-07-07 13:00:00' ORDER BY created_at DESC"
print(run(f'{DB_CMD} -e "{sql3}" 2>&1'))

# 4. 查询 upload_sessions 表，看是否有未完成的会话
print('\n=== 4. upload_sessions 表中所有 13:00 之后的会话 ===')
sql4 = "SELECT id, filename, file_size, owner_id, status, created_at, completed_at FROM upload_sessions WHERE created_at >= '2026-07-07 13:00:00' ORDER BY created_at DESC LIMIT 20"
print(run(f'{DB_CMD} -e "{sql4}" 2>&1'))

# 5. 列出该用户的总文件数
print('\n=== 5. 用户 3301767269f775 的文件统计 ===')
sql5 = "SELECT COUNT(*) AS total_files, SUM(CASE WHEN deleted_at IS NULL THEN 1 ELSE 0 END) AS active_files, SUM(CASE WHEN deleted_at IS NOT NULL THEN 1 ELSE 0 END) AS trashed_files, MAX(created_at) AS latest_upload FROM files WHERE owner='3301767269f775'"
print(run(f'{DB_CMD} -e "{sql5}" 2>&1'))

# 6. 检查 upload_sessions 表结构
print('\n=== 6. upload_sessions 表结构 ===')
sql6 = "DESC upload_sessions"
print(run(f'{DB_CMD} -e "{sql6}" 2>&1'))
