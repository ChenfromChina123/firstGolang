"""验证部署后的健康状态."""
import sys
sys.path.insert(0, r"C:\Users\Administrator\.config\ssh-mcp")
from ssh_tool import run

print("=== Health endpoint ===")
print(run("curl -s -o /dev/null -w 'HTTP %{http_code}\\n' https://aistudy.icu/api/health --insecure"))

print("\n=== MySQL log ===")
print(run("grep -i mysql /opt/filesync/server.log | tail -5"))

print("\n=== Last 5 lines log ===")
print(run("tail -n 5 /opt/filesync/server.log"))
