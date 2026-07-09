"""从外部和本地双角度验证健康端点."""
import sys
sys.path.insert(0, r"C:\Users\Administrator\.config\ssh-mcp")
from ssh_tool import run

print("=== Server-side curl (http -> https redirect) ===")
print(run("curl -sL -o /dev/null -w 'HTTP %{http_code}\\n' http://127.0.0.1/api/health --insecure"))

print("\n=== Server-side curl https direct ===")
print(run("curl -s -w '\\nHTTP %{http_code}\\n' https://127.0.0.1/api/health --insecure"))

print("\n=== Process listening ports ===")
print(run("ss -tlnp 2>/dev/null | grep -E ':(80|443|8080) ' | head -10"))

print("\n=== Top 30 lines of recent log ===")
print(run("tail -n 30 /opt/filesync/server.log"))
