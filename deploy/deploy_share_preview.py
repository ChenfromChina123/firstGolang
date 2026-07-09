"""部署分享预览功能到生产服务器.

上传 server_linux_new + share.html + share.js + style.css 到 /opt/filesync/，
重启 systemd 服务并验证。
"""
import sys
import os
import hashlib
import paramiko

HOST = '8.138.174.80'
PORT = 22
USER = 'root'
PASSWORD = '***REMOVED_SSH_PASSWORD***'

LOCAL_ROOT = r'd:\STUDY\GO\StudyGolang\firstGolang\filesync'
REMOTE_SERVER = '/opt/filesync/server'
REMOTE_WEB = '/opt/filesync/web'

# 需要部署的文件：(本地路径, 远程路径, 是否可执行)
FILES = [
    (os.path.join(LOCAL_ROOT, 'server_linux_new'), REMOTE_SERVER, True),
    (os.path.join(LOCAL_ROOT, 'web', 'share.html'), f'{REMOTE_WEB}/share.html', False),
    (os.path.join(LOCAL_ROOT, 'web', 'share.js'), f'{REMOTE_WEB}/share.js', False),
    (os.path.join(LOCAL_ROOT, 'web', 'style.css'), f'{REMOTE_WEB}/style.css', False),
]


def md5_local(path):
    h = hashlib.md5()
    with open(path, 'rb') as f:
        for chunk in iter(lambda: f.read(8192), b''):
            h.update(chunk)
    return h.hexdigest()


def upload_and_verify(ssh, sftp, local_path, remote_path, executable):
    """上传文件到临时路径，校验大小和 md5，原子替换."""
    local_size = os.path.getsize(local_path)
    local_md5 = md5_local(local_path)
    fname = os.path.basename(local_path)
    print(f'\n上传 {fname} (size={local_size}, md5={local_md5[:12]}...)')

    tmp_remote = f'{remote_path}.new'

    # 上传到临时文件
    sftp.put(local_path, tmp_remote)
    if executable:
        sftp.chmod(tmp_remote, 0o755)
    else:
        sftp.chmod(tmp_remote, 0o644)

    # 校验远程文件大小和 md5
    check_cmd = f'stat -c %s {tmp_remote}; md5sum {tmp_remote} | cut -d" " -f1'
    _, stdout, _ = ssh.exec_command(check_cmd)
    out = stdout.read().decode().strip().split('\n')
    remote_size = out[0] if len(out) > 0 else ''
    remote_md5 = out[1] if len(out) > 1 else ''

    if str(local_size) != remote_size:
        print(f'  [ERROR] size mismatch: local={local_size} remote={remote_size}')
        sys.exit(1)
    if local_md5 != remote_md5:
        print(f'  [ERROR] md5 mismatch: local={local_md5} remote={remote_md5}')
        sys.exit(1)
    print(f'  [OK] size={remote_size} md5={remote_md5[:12]}...')

    # 原子替换（mv -f 强制覆盖）
    _, stdout, stderr = ssh.exec_command(f'mv -f {tmp_remote} {remote_path} && chmod {"755" if executable else "644"} {remote_path} && echo OK')
    mv_out = stdout.read().decode().strip()
    mv_err = stderr.read().decode().strip()
    if 'OK' not in mv_out:
        print(f'  [ERROR] mv failed: {mv_err}')
        sys.exit(1)
    print(f'  覆盖完成')


def main():
    print(f'连接 {USER}@{HOST}:{PORT} ...')
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(HOST, port=PORT, username=USER, password=PASSWORD, timeout=15)
    sftp = ssh.open_sftp()
    print('连接成功。')

    # 备份当前 server 二进制（仅 server 需要备份，web 文件已有 .bak 机制）
    backup_cmd = f'cp -f {REMOTE_SERVER} {REMOTE_SERVER}.bak.share_preview 2>/dev/null && echo "server backed up" || echo "no existing server (first deploy)"'
    _, stdout, _ = ssh.exec_command(backup_cmd)
    print(f'\n备份: {stdout.read().decode().strip()}')

    # 上传所有文件
    for local_path, remote_path, executable in FILES:
        upload_and_verify(ssh, sftp, local_path, remote_path, executable)

    # 重启服务
    print('\n=== 重启 filesync 服务 ===')
    _, stdout, stderr = ssh.exec_command('systemctl restart filesync && sleep 2 && systemctl is-active filesync')
    out = stdout.read().decode().strip()
    err = stderr.read().decode().strip()
    print(f'服务状态: {out}')
    if err:
        print(f'[stderr] {err}')
    if out != 'active':
        print('[ERROR] 服务未正常启动，检查日志')
        _, stdout, _ = ssh.exec_command('tail -30 /opt/filesync/server.log')
        print(stdout.read().decode())
        sys.exit(1)

    # 验证健康状态
    print('\n=== HTTPS 验证 ===')
    verify_cmds = [
        'curl -s https://aistudy.icu/api/health',
        'curl -sI "https://aistudy.icu/web/share.js?v=20260717" | head -3',
        'curl -sI "https://aistudy.icu/web/style.css?v=20260717" | head -3',
    ]
    for cmd in verify_cmds:
        print(f'$ {cmd}')
        _, stdout, _ = ssh.exec_command(cmd)
        print(stdout.read().decode())

    sftp.close()
    ssh.close()
    print('\n部署完成。')


if __name__ == '__main__':
    main()
