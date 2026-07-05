#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
部署 FileSync HTTPS 版本到 aistudy.icu 服务器
步骤：
1. 停止 filesync 服务
2. 上传新二进制 server_linux
3. 上传 web/ 静态文件（login.html, index.html, app.js, style.css）
4. 创建证书目录
5. 更新 systemd 配置（添加 DOMAIN, JWT_SECRET, 初始管理员, CERT_DIR, WEB_DIR）
6. daemon-reload + 启动服务
7. 查看状态和日志
"""
import sys
import time

sys.path.insert(0, r"C:\Users\Administrator\.config\ssh-mcp")
from ssh_tool import run, upload

# 部署配置
DOMAIN = "aistudy.icu"
JWT_SECRET = "***REMOVED_JWT_SECRET***"
INIT_USERNAME = "admin"
INIT_PASSWORD = "***REMOVED_PASSWORD***"

SYSTEMD_CONFIG = f"""[Unit]
Description=FileSync Server
After=network.target redis.service docker.service
Requires=docker.service

[Service]
Type=simple
User=root
WorkingDirectory=/opt/filesync
ExecStart=/opt/filesync/server
Environment=DOMAIN={DOMAIN}
Environment=DATA_DIR=/opt/filesync/data
Environment=STORAGE_TYPE=local
Environment=WEB_DIR=/opt/filesync/web
Environment=CERT_DIR=/opt/filesync/certs
Environment=MYSQL_DSN=filesync:***REMOVED_DB_PASSWORD***@tcp(127.0.0.1:13306)/filesync?parseTime=true&loc=Local&charset=utf8mb4
Environment=REDIS_SENTINEL_ADDRS=127.0.0.1:36379,127.0.0.1:36380,127.0.0.1:36381
Environment=REDIS_SENTINEL_MASTER=mymaster
Environment=REDIS_PASSWORD=redis123
Environment=REDIS_DB=0
Environment=JWT_SECRET={JWT_SECRET}
Environment=FILESYNC_INITIAL_USERNAME={INIT_USERNAME}
Environment=FILESYNC_INITIAL_PASSWORD={INIT_PASSWORD}
Restart=on-failure
RestartSec=5
StandardOutput=append:/opt/filesync/server.log
StandardError=append:/opt/filesync/server.log
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
"""


def main():
    # 1. 停止服务
    print("=== 1. 停止 filesync 服务 ===")
    out = run("systemctl stop filesync 2>&1; echo EXIT_$?")
    print(out)

    # 2. 上传新二进制
    print("\n=== 2. 上传 server_linux ===")
    local_bin = r"d:\STUDY\GO\StudyGolang\firstGolang\filesync\server_linux"
    out = upload(local_bin, "/opt/filesync/server.new")
    print(out)
    if "[ERROR]" in out:
        print("[FATAL] 上传二进制失败")
        sys.exit(1)

    # 3. 替换二进制并赋权
    print("\n=== 3. 替换二进制 ===")
    out = run(
        "cp /opt/filesync/server /opt/filesync/server.bak.$(date +%s) && "
        "mv /opt/filesync/server.new /opt/filesync/server && "
        "chmod +x /opt/filesync/server && "
        "ls -la /opt/filesync/server"
    )
    print(out)

    # 4. 上传 web/ 静态文件
    print("\n=== 4. 上传 web/ 静态文件 ===")
    web_files = [
        ("login.html", r"d:\STUDY\GO\StudyGolang\firstGolang\filesync\web\login.html"),
        ("index.html", r"d:\STUDY\GO\StudyGolang\firstGolang\filesync\web\index.html"),
        ("app.js", r"d:\STUDY\GO\StudyGolang\firstGolang\filesync\web\app.js"),
        ("style.css", r"d:\STUDY\GO\StudyGolang\firstGolang\filesync\web\style.css"),
    ]
    for name, local_path in web_files:
        remote = f"/opt/filesync/web/{name}"
        out = upload(local_path, remote)
        print(f"  {name}: {out.strip()}")
        if "[ERROR]" in out:
            print(f"[FATAL] 上传 {name} 失败")
            sys.exit(1)

    # 5. 创建证书目录
    print("\n=== 5. 创建证书目录 ===")
    out = run("mkdir -p /opt/filesync/certs && chmod 700 /opt/filesync/certs && ls -ld /opt/filesync/certs")
    print(out)

    # 6. 写入新 systemd 配置
    print("\n=== 6. 更新 systemd 配置 ===")
    # 先写到临时文件
    config_remote = "/tmp/filesync.service.new"
    # 用 base64 避免特殊字符问题
    import base64
    config_b64 = base64.b64encode(SYSTEMD_CONFIG.encode()).decode()
    out = run(f"echo '{config_b64}' | base64 -d > {config_remote} && cat {config_remote} | head -5")
    print(out)

    # 校验配置内容
    out = run(f"grep -c 'DOMAIN=' {config_remote} && grep -c 'JWT_SECRET=' {config_remote} && grep -c 'FILESYNC_INITIAL' {config_remote}")
    print(f"配置项检查: {out.strip()}")

    # 替换 systemd 配置
    out = run(
        f"cp /etc/systemd/system/filesync.service /etc/systemd/system/filesync.service.bak.$(date +%s) && "
        f"mv {config_remote} /etc/systemd/system/filesync.service && "
        f"chmod 644 /etc/systemd/system/filesync.service"
    )
    print(out)

    # 7. daemon-reload + 启动
    print("\n=== 7. daemon-reload + 启动服务 ===")
    out = run("systemctl daemon-reload && systemctl start filesync 2>&1; echo EXIT_$?")
    print(out)

    # 8. 等待并查看状态
    print("\n=== 8. 等待 5s 后查看状态 ===")
    time.sleep(5)
    out = run("systemctl is-active filesync; echo '---'; systemctl --no-pager status filesync 2>&1 | head -15")
    print(out)

    # 9. 查看日志
    print("\n=== 9. 最近 40 行日志 ===")
    out = run("journalctl -u filesync -n 40 --no-pager 2>&1")
    print(out)

    # 10. 检查端口
    print("\n=== 10. 端口检查 ===")
    out = run("ss -tlnp | grep -E ':(80|443) ' 2>&1 || echo 'ports not ready yet'")
    print(out)

    print("\n=== 部署完成 ===")
    print(f"域名: https://{DOMAIN}")
    print(f"初始管理员: {INIT_USERNAME}")
    print(f"初始密码: {INIT_PASSWORD}")
    print(f"JWT_SECRET: {JWT_SECRET[:16]}...（已写入 systemd 配置）")


if __name__ == "__main__":
    main()
