"""修复 309 条 owner 为空的历史文件记录，全部归到 3301767269f775.

执行前先备份，执行后验证。
"""
import sys
sys.path.insert(0, r"C:\Users\Administrator\.config\ssh-mcp")
from ssh_tool import run

MYSQL = "mysql -h 127.0.0.1 -P 13306 -u filesync -p***REMOVED_DB_PASSWORD*** filesync -e"

print("=== 修复前：owner 为空记录数 ===")
print(run(f'{MYSQL} "SELECT COUNT(*) AS empty_owner_count FROM files WHERE owner=\'\'" 2>&1'))

print("\n=== 备份 files 表到 files_backup_owner_fix_20260707 ===")
print(run(f'{MYSQL} "CREATE TABLE IF NOT EXISTS files_backup_owner_fix_20260707 AS SELECT * FROM files WHERE owner=\'\'" 2>&1'))
print(run(f'{MYSQL} "SELECT COUNT(*) AS backup_count FROM files_backup_owner_fix_20260707" 2>&1'))

print("\n=== 执行 UPDATE：把所有 owner 为空的记录归到 3301767269f775 ===")
print(run(f'{MYSQL} "UPDATE files SET owner=\'3301767269f775\' WHERE owner=\'\'" 2>&1'))

print("\n=== 修复后：owner 为空记录数（应该为 0）===")
print(run(f'{MYSQL} "SELECT COUNT(*) AS empty_owner_count FROM files WHERE owner=\'\'" 2>&1'))

print("\n=== 修复后：按 owner 统计文件数 ===")
print(run(f'{MYSQL} "SELECT owner, COUNT(*) AS cnt FROM files GROUP BY owner ORDER BY cnt DESC" 2>&1'))

print("\n=== 修复后：3301767269f775 用户的最近 5 条文件 ===")
print(run(f'{MYSQL} "SELECT id, filename, owner, created_at FROM files WHERE owner=\'3301767269f775\' ORDER BY created_at DESC LIMIT 5" 2>&1'))
