"""通过 API 验证修复效果.

1. admin 登录，列出文件，确认总数合理
2. 上传一个测试文件（admin）
3. 再次列出，确认新文件 owner 正确（admin）
4. 同时查询数据库直接验证 owner 字段写入正确
"""
import sys
import json
import requests

sys.path.insert(0, r"C:\Users\Administrator\.config\ssh-mcp")
from ssh_tool import run

BASE = "https://aistudy.icu"
SESSION = requests.Session()
SESSION.verify = False

import urllib3
urllib3.disable_warnings()


def admin_login():
    r = SESSION.post(f"{BASE}/api/login", json={
        "username": "admin",
        "password": "***REMOVED_PASSWORD***"
    }, timeout=10)
    print(f"[admin login] status={r.status_code} body={r.text[:200]}")
    return r.status_code == 200


def list_files(prefix=""):
    url = f"{BASE}/api/files"
    if prefix:
        url += f"?prefix={prefix}"
    r = SESSION.get(url, timeout=10)
    if r.status_code != 200:
        print(f"[list] status={r.status_code} body={r.text[:300]}")
        return None
    data = r.json()
    files = data.get("files", data) if isinstance(data, dict) else data
    return files


def upload_test():
    """通过 admin 上传一个测试文件."""
    fname = f"verify-owner-fix-{__import__('time').time_ns()}.txt"
    content = b"verify owner fix test\n"

    # init
    r = SESSION.post(f"{BASE}/api/upload/init", json={
        "filename": fname,
        "file_size": len(content),
        "file_hash": "",
        "chunk_size": 512 * 1024,
        "storage": "local"
    }, timeout=10)
    print(f"[upload init] status={r.status_code} body={r.text[:200]}")
    if r.status_code != 200:
        return None, None
    session_id = r.json().get("session_id")
    print(f"  session_id={session_id}")

    # chunk
    import io
    files = {"chunk_data": (fname, io.BytesIO(content), "application/octet-stream")}
    data = {"session_id": session_id, "chunk_index": "0"}
    r = SESSION.post(f"{BASE}/api/upload/chunk", files=files, data=data, timeout=10)
    print(f"[upload chunk] status={r.status_code} body={r.text[:200]}")

    # complete
    r = SESSION.post(f"{BASE}/api/upload/complete", json={"session_id": session_id}, timeout=15)
    print(f"[upload complete] status={r.status_code} body={r.text[:300]}")
    if r.status_code != 200:
        return None, None
    file_id = r.json().get("file_id")
    return fname, file_id


def check_db_owner(file_id):
    """通过 SSH 直接查询数据库验证新上传文件的 owner 字段."""
    MYSQL = "mysql -h 127.0.0.1 -P 13306 -u filesync -p***REMOVED_DB_PASSWORD*** filesync -N -e"
    out = run(f'{MYSQL} "SELECT id, filename, owner, created_at FROM files WHERE id=\'{file_id}\'" 2>&1')
    return out


def main():
    if not admin_login():
        print("[FATAL] admin login failed")
        return

    print("\n=== 1. 修复后 admin 列出文件 ===")
    files = list_files()
    if files is None:
        return
    print(f"  total files visible to admin: {len(files)}")
    if files:
        sample = files[0] if isinstance(files, list) else list(files.values())[0]
        print(f"  sample: {json.dumps(sample, ensure_ascii=False)[:300]}")

    print("\n=== 2. admin 上传测试文件（验证 owner 写入）===")
    fname, file_id = upload_test()
    if not file_id:
        print("[FATAL] upload failed")
        return
    print(f"  uploaded: filename={fname} id={file_id}")

    print("\n=== 3. 查询数据库，验证新上传文件的 owner 字段 ===")
    out = check_db_owner(file_id)
    print(out)
    if "admin" in out:
        print("  [OK] owner correctly written as 'admin'")
    else:
        print("  [FAIL] owner NOT written correctly")

    print("\n=== 4. 再次列出文件，确认总数 +1 ===")
    files2 = list_files()
    if files2 is not None:
        print(f"  total files after upload: {len(files2)}")

    # 测试用 3301767269f775 账号无法登录（不知密码），但可以通过 admin 验证逻辑
    print("\n=== 5. 查询数据库，按 owner 统计 ===")
    MYSQL = "mysql -h 127.0.0.1 -P 13306 -u filesync -p***REMOVED_DB_PASSWORD*** filesync -e"
    out = run(f'{MYSQL} "SELECT owner, COUNT(*) AS cnt FROM files GROUP BY owner ORDER BY cnt DESC" 2>&1')
    print(out)


if __name__ == "__main__":
    main()
