#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""查看应用日志确认 HTTPS 证书申请状态"""
import sys
sys.path.insert(0, r"C:\Users\Administrator\.config\ssh-mcp")
from ssh_tool import run

print("=== 应用日志（最后 50 行）===")
out = run("tail -50 /opt/filesync/server.log 2>&1")
print(out)

print("\n=== 证书目录 ===")
out = run("ls -la /opt/filesync/certs/ 2>&1")
print(out)

print("\n=== 健康检查 ===")
out = run("curl -sk https://localhost/api/health 2>&1 || curl -s http://localhost/api/health 2>&1")
print(out)

print("\n=== HTTPS 本地访问测试 ===")
out = run("curl -sk -o /dev/null -w 'HTTP %{http_code}, redirect=%{redirect_url}\\n' https://localhost/web/index.html 2>&1")
print(out)

print("\n=== HTTP 本地访问测试（应重定向到 HTTPS）===")
out = run("curl -s -o /dev/null -w 'HTTP %{http_code}, redirect=%{redirect_url}\\n' http://localhost/web/index.html 2>&1")
print(out)
