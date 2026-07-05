#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
检查服务器当前状态：systemd 配置、端口占用、web 目录
"""
import sys

sys.path.insert(0, r"C:\Users\Administrator\.config\ssh-mcp")
from ssh_tool import run, upload


def main():
    print("=== 1. 当前 systemd 配置 ===")
    out = run("cat /etc/systemd/system/filesync.service 2>&1")
    print(out)

    print("\n=== 2. filesync 服务状态 ===")
    out = run("systemctl is-active filesync; echo '---'; systemctl --no-pager status filesync 2>&1 | head -15")
    print(out)

    print("\n=== 3. 80/443/8080 端口占用 ===")
    out = run("ss -tlnp | grep -E ':(80|443|8080) ' 2>&1 || echo 'no match'")
    print(out)

    print("\n=== 4. /opt/filesync/ 目录 ===")
    out = run("ls -la /opt/filesync/ 2>&1")
    print(out)

    print("\n=== 5. /opt/filesync/web/ 目录 ===")
    out = run("ls -la /opt/filesync/web/ 2>&1")
    print(out)

    print("\n=== 6. 当前环境变量（从 systemd） ===")
    out = run("systemctl show filesync -p Environment 2>&1")
    print(out)

    print("\n=== 7. nginx 状态 ===")
    out = run("systemctl is-active nginx 2>&1; echo '---'; ss -tlnp | grep ':80 ' 2>&1 || echo 'port 80 free'")
    print(out)


if __name__ == "__main__":
    main()
