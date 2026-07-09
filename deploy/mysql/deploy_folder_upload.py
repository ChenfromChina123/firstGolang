"""部署 web/dist 静态资源到生产服务器.

仅替换前端静态资源，不重启 systemd 服务（dist 由 Go http.FileServer 直接读取）。

流程：
1. 备份原 app.js 和 index.html 到 /opt/filesync/web/dist.bak.20260707/
2. SFTP 上传 web/dist/ 下全部 10 个文件
3. 校验文件大小一致
4. 健康检查（服务仍正常响应）
"""

import sys
import os
import time

sys.path.insert(0, r"C:\Users\Administrator\.config\ssh-mcp")
from ssh_tool import run, upload

LOCAL_DIST = r"d:\STUDY\GO\StudyGolang\firstGolang\filesync\web\dist"
REMOTE_DIST = "/opt/filesync/web/dist"
REMOTE_BAK = "/opt/filesync/web/dist.bak.20260707"

DIST_FILES = [
    "activate.html",
    "app.js",
    "forgot-password.html",
    "index.html",
    "login.html",
    "register.html",
    "reset-password.html",
    "share.html",
    "share.js",
    "style.css",
]

# 只备份本次会替换的两个核心文件（其他文件未改动）
BACKUP_FILES = ["app.js", "index.html"]


def main():
    # 0. 校验本地 dist 目录
    if not os.path.isdir(LOCAL_DIST):
        print(f"[FATAL] local dist dir not found: {LOCAL_DIST}")
        sys.exit(1)

    print("=== Step 0: verify local dist files ===")
    for f in DIST_FILES:
        p = os.path.join(LOCAL_DIST, f)
        if not os.path.exists(p):
            print(f"[FATAL] missing local file: {p}")
            sys.exit(1)
        size = os.path.getsize(p)
        print(f"  {f}: {size} bytes")

    # 1. 备份原 app.js 和 index.html
    print("\n=== Step 1: backup core files ===")
    out = run(f"mkdir -p {REMOTE_BAK}")
    print(out)
    for f in BACKUP_FILES:
        remote_path = f"{REMOTE_DIST}/{f}"
        bak_path = f"{REMOTE_BAK}/{f}"
        out = run(f"cp -f {remote_path} {bak_path} 2>/dev/null && ls -lh {bak_path} || echo 'no existing file to backup'")
        print(out)

    # 2. 上传全部 dist 文件
    print("\n=== Step 2: upload dist files ===")
    for f in DIST_FILES:
        local_path = os.path.join(LOCAL_DIST, f)
        remote_path = f"{REMOTE_DIST}/{f}"
        out = upload(local_path, remote_path)
        print(f"  uploaded {f}: {out.strip()}")

    # 3. 校验文件大小
    print("\n=== Step 3: verify file sizes ===")
    for f in DIST_FILES:
        local_size = os.path.getsize(os.path.join(LOCAL_DIST, f))
        remote_path = f"{REMOTE_DIST}/{f}"
        out = run(f"stat -c %s {remote_path}")
        remote_size = int(out.strip()) if out.strip().isdigit() else -1
        match = "OK" if local_size == remote_size else "MISMATCH"
        print(f"  {f}: local={local_size} remote={remote_size} [{match}]")
        if match == "MISMATCH":
            print(f"[WARN] size mismatch for {f}")

    # 4. 健康检查（服务应仍正常响应）
    print("\n=== Step 4: health check ===")
    out = run("systemctl is-active filesync.service")
    print(f"service active: {out.strip()}")
    out = run("curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/api/health 2>/dev/null || echo NO_HEALTH_ENDPOINT")
    print(f"health endpoint: {out.strip()}")

    # 5. 检查 dist 目录最新修改时间
    print("\n=== Step 5: dist directory listing ===")
    out = run(f"ls -lh --time-style=long-iso {REMOTE_DIST}/")
    print(out)

    print("\n[DONE] deployment complete")


if __name__ == "__main__":
    main()
