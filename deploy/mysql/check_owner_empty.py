"""查询 owner 为空记录的详细情况，用于决定修复策略."""
import sys
sys.path.insert(0, r"C:\Users\Administrator\.config\ssh-mcp")
from ssh_tool import run

MYSQL = "mysql -h 127.0.0.1 -P 13306 -u filesync -p***REMOVED_DB_PASSWORD*** filesync -e"

print("=== 1. users 表所有用户 ===")
print(run(f'{MYSQL} "SELECT id, username, role, status, created_at FROM users ORDER BY created_at" 2>&1'))

print("\n=== 2. owner 为空的文件总数 + 时间范围 ===")
print(run(f'{MYSQL} "SELECT COUNT(*) AS cnt, MIN(created_at) AS min_t, MAX(created_at) AS max_t FROM files WHERE owner=\'\'" 2>&1'))

print("\n=== 3. owner 为空的文件按 filename 路径前缀分组（前 30 个）===")
print(run(f'{MYSQL} "SELECT SUBSTRING_INDEX(filename, \'/\', 1) AS prefix, COUNT(*) AS cnt FROM files WHERE owner=\'\' GROUP BY prefix ORDER BY cnt DESC LIMIT 30" 2>&1'))

print("\n=== 4. owner 为空文件示例 10 条 ===")
print(run(f'{MYSQL} "SELECT id, filename, size, created_at FROM files WHERE owner=\'\' ORDER BY created_at DESC LIMIT 10" 2>&1'))

print("\n=== 5. owner 不为空文件示例 5 条 ===")
print(run(f'{MYSQL} "SELECT id, filename, owner, created_at FROM files WHERE owner<>\'\' ORDER BY created_at DESC LIMIT 5" 2>&1'))

print("\n=== 6. upload_sessions 表结构 ===")
print(run(f'{MYSQL} "DESCRIBE upload_sessions" 2>&1'))

print("\n=== 7. upload_sessions 最近 10 条 ===")
print(run(f'{MYSQL} "SELECT id, filename, status, created_at, updated_at FROM upload_sessions ORDER BY created_at DESC LIMIT 10" 2>&1'))

print("\n=== 8. 检查 owner 为空文件的会话状态匹配 ===")
print(run(f'{MYSQL} "SELECT f.id, f.filename, f.created_at, s.id AS session_id, s.status AS sess_status FROM files f LEFT JOIN upload_sessions s ON f.id LIKE CONCAT(\'%\', s.id, \'%\') OR s.filename=f.filename WHERE f.owner=\'\' ORDER BY f.created_at DESC LIMIT 5" 2>&1'))
