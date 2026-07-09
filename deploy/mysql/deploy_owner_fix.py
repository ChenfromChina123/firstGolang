"""部署 owner 修复版的 server_linux 到生产服务器.

流程：
1. 备份旧二进制 /opt/filesync/server_linux -> /opt/filesync/server_linux.bak
2. 停止 filesync.service
3. 上传新二进制
4. 启动 filesync.service
5. 健康检查
"""

import sys
import os
import time

sys.path.insert(0, r"C:\Users\Administrator\.config\ssh-mcp")
from ssh_tool import run, upload

LOCAL_BIN = r"d:\STUDY\GO\StudyGolang\firstGolang\filesync\server_linux.exe"
REMOTE_BIN = "/opt/filesync/server_linux"
REMOTE_BAK = "/opt/filesync/server_linux.bak"


def main():
    # 0. 校验本地二进制存在
    if not os.path.exists(LOCAL_BIN):
        print(f"[FATAL] local binary not found: {LOCAL_BIN}")
        sys.exit(1)
    size = os.path.getsize(LOCAL_BIN)
    print(f"[INFO] local binary size={size} bytes")

    # 1. 备份旧二进制
    print("\n=== Step 1: backup old binary ===")
    out = run(f"cp -f {REMOTE_BIN} {REMOTE_BAK} && ls -lh {REMOTE_BAK}")
    print(out)

    # 2. 停止服务
    print("\n=== Step 2: stop filesync.service ===")
    out = run("systemctl stop filesync.service && systemctl is-active filesync.service || echo STOPPED")
    print(out)

    # 3. 上传新二进制
    print("\n=== Step 3: upload new binary ===")
    out = upload(LOCAL_BIN, REMOTE_BIN)
    print(out)

    # 4. 设置权限 + 启动
    print("\n=== Step 4: chmod + start ===")
    out = run(f"chmod +x {REMOTE_BIN} && ls -lh {REMOTE_BIN}")
    print(out)
    out = run("systemctl start filesync.service")
    print(out)

    # 5. 等待启动
    time.sleep(2)
    print("\n=== Step 5: health check ===")
    out = run("systemctl is-active filesync.service")
    print(f"service active: {out.strip()}")
    out = run("systemctl status filesync.service --no-pager | head -n 15")
    print(out)
    out = run("curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/api/health 2>/dev/null || echo NO_HEALTH_ENDPOINT")
    print(f"health endpoint: {out.strip()}")

    # 6. 检查日志最新输出
    print("\n=== Step 6: tail server.log ===")
    out = run("tail -n 20 /opt/filesync/server.log")
    print(out)


if __name__ == "__main__":
    main()
