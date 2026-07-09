"""
检查域名访问、SSL 证书、防火墙状态。
"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== 1. SSL 证书有效期 ===')
print(run('echo | openssl s_client -connect localhost:443 -servername aistudy.icu 2>/dev/null | openssl x509 -noout -dates -subject -issuer'))

print('\n=== 2. 防火墙状态 ===')
print(run('systemctl is-active firewalld 2>/dev/null; iptables -L INPUT -n 2>/dev/null | head -20'))

print('\n=== 3. 80/443 端口监听（再确认） ===')
print(run('ss -tlnp | grep -E ":(80|443) "'))

print('\n=== 4. 服务器自己访问 aistudy.icu ===')
print(run('curl -sv -k --max-time 10 https://aistudy.icu/api/health 2>&1 | tail -30'))

print('\n=== 5. 服务器自己访问主页 ===')
print(run('curl -sv -k --max-time 10 https://aistudy.icu/ 2>&1 | tail -20'))

print('\n=== 6. 检查 aistudy.icu 解析 ===')
print(run('dig +short aistudy.icu; nslookup aistudy.icu 2>&1 | head -10'))

print('\n=== 7. 检查 nginx/反代（如果有的话） ===')
print(run('systemctl is-active nginx 2>/dev/null; ps aux | grep -E "nginx|caddy" | grep -v grep'))

print('\n=== 8. 检查阿里云安全组（无法直接查，但看本地 iptables） ===')
print(run('iptables -L -n 2>/dev/null | head -30'))

print('\n=== 9. 检查 server.log 最近的错误 ===')
print(run('tail -100 /opt/filesync/server.log 2>/dev/null'))

print('\n=== 10. 检查 server 进程的最近 stderr ===')
print(run('journalctl -u filesync --since "10 minutes ago" --no-pager'))
