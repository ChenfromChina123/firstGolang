"""检查用户文件的详细数据，看是否有异常记录导致 JSON 序列化失败."""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

DB_CMD = 'mysql -h 127.0.0.1 -P 13306 -u filesync -p***REMOVED_DB_PASSWORD*** filesync'

# 1. 查看 files 表结构
print('=== 1. files 表结构 ===')
print(run(f'{DB_CMD} -e "DESC files" 2>&1'))

# 2. 列出该用户最近的 10 个文件（含所有字段）
print('\n=== 2. 用户 3301767269f775 最近的 10 个文件 ===')
sql = "SELECT * FROM files WHERE owner='3301767269f775' ORDER BY created_at DESC LIMIT 10"
print(run(f'{DB_CMD} -e "{sql}" 2>&1'))

# 3. 检查是否有 NULL 或空 filename
print('\n=== 3. 检查是否有 NULL/空 filename 的文件 ===')
sql3 = "SELECT id, filename, owner, created_at FROM files WHERE owner='3301767269f775' AND (filename IS NULL OR filename='' OR filename LIKE '%\"%' OR filename LIKE '%\\\\%')"
print(run(f'{DB_CMD} -e "{sql3}" 2>&1'))

# 4. 检查是否有 NULL created_at
print('\n=== 4. 检查是否有 NULL created_at 的文件 ===')
sql4 = "SELECT id, filename, owner, created_at FROM files WHERE owner='3301767269f775' AND (created_at IS NULL OR created_at='')"
print(run(f'{DB_CMD} -e "{sql4}" 2>&1'))

# 5. 检查 upload_sessions 中所有最近的会话（不限用户）
print('\n=== 5. upload_sessions 表中所有最近的 20 个会话 ===')
sql5 = "SELECT id, filename, file_size, file_hash, status, created_at, updated_at FROM upload_sessions ORDER BY created_at DESC LIMIT 20"
print(run(f'{DB_CMD} -e "{sql5}" 2>&1'))

# 6. 检查该用户在 11:00 之后的所有文件记录
print('\n=== 6. 用户 11:00 之后创建的所有文件 ===')
sql6 = "SELECT id, filename, size, owner, created_at FROM files WHERE owner='3301767269f775' AND created_at >= '2026-07-07 11:00:00' ORDER BY created_at DESC"
print(run(f'{DB_CMD} -e "{sql6}" 2>&1'))
