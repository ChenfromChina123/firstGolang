"""查询生产服务器 systemd 服务配置，确认 server 二进制路径."""
import paramiko

HOST = '8.138.174.80'
PORT = 22
USER = 'root'
PASSWORD = '***REMOVED_SSH_PASSWORD***'

ssh = paramiko.SSHClient()
ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
ssh.connect(HOST, port=PORT, username=USER, password=PASSWORD, timeout=15)

# 查询 systemd 服务文件
cmds = [
    'cat /etc/systemd/system/filesync.service',
    'systemctl status filesync --no-pager | head -5',
    'ls -la /opt/filesync/server* 2>/dev/null || echo "no server in /opt/filesync/"',
    'ls -la /opt/filesync/ | head -20',
]
for cmd in cmds:
    print(f'=== {cmd} ===')
    _, stdout, stderr = ssh.exec_command(cmd)
    print(stdout.read().decode())
    err = stderr.read().decode()
    if err:
        print(f'[stderr] {err}')

ssh.close()
