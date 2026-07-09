"""部署视频预览轻量优化到生产服务器.

上传 web/style.css、web/share.html、web/share.js 到 /opt/filesync/web/，
并通过 HTTPS 验证文件可访问（200）且版本号正确。

本次改动：
- 前端新增 loading/error 覆盖层 + spinner 动画
- share.js bindMediaEvents 监听 loadstart/waiting/seeking/canplay/playing/seeked/stalled/error
  - loadstart/waiting/seeking/stalled → 显示 loading（"加载中/缓冲中/跳转中/加载停滞"）
  - canplay/playing/seeked → 隐藏 loading
  - error → 显示错误码对应文案
- 版本号升级到 v=20260719 防缓存
"""
import sys
import os
import hashlib
import paramiko


HOST = '8.138.174.80'
PORT = 22
USER = 'root'
PASSWORD = '***REMOVED_SSH_PASSWORD***'

LOCAL_WEB = r'd:\STUDY\GO\StudyGolang\firstGolang\filesync\web'
REMOTE_WEB = '/opt/filesync/web'

# 本次需要上传的文件（loading 覆盖层 + 事件监听 + 版本号升级）
FILES = ['style.css', 'share.html', 'share.js']


def md5_local(path):
    """计算本地文件 MD5 用于校验."""
    h = hashlib.md5()
    with open(path, 'rb') as f:
        for chunk in iter(lambda: f.read(8192), b''):
            h.update(chunk)
    return h.hexdigest()


def main():
    """部署主流程：连接 → 上传 → 校验 → 关闭."""
    print(f'连接 {USER}@{HOST}:{PORT} ...')
    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(HOST, port=PORT, username=USER, password=PASSWORD, timeout=15)
    sftp = ssh.open_sftp()
    print('连接成功。\n')

    for fname in FILES:
        local_path = os.path.join(LOCAL_WEB, fname)
        remote_path = f'{REMOTE_WEB}/{fname}'
        local_size = os.path.getsize(local_path)
        local_md5 = md5_local(local_path)

        print(f'上传 {fname} (size={local_size}, md5={local_md5[:12]}...)')

        # 备份原文件
        backup_cmd = f'cp -f {remote_path} {remote_path}.bak 2>/dev/null; echo ok'
        _, stdout, _ = ssh.exec_command(backup_cmd)
        stdout.read()

        # 上传到临时文件再原子替换
        tmp_remote = f'{remote_path}.new'
        sftp.put(local_path, tmp_remote)
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

        # 原子替换（mv -f 强制覆盖已存在的目标文件）
        _, stdout, stderr = ssh.exec_command(f'mv -f {tmp_remote} {remote_path} && chmod 644 {remote_path} && echo OK')
        mv_out = stdout.read().decode().strip()
        mv_err = stderr.read().decode().strip()
        if 'OK' not in mv_out:
            print(f'  [ERROR] mv failed: {mv_err}')
            sys.exit(1)
        print(f'  覆盖完成\n')

    # 通过 HTTPS 验证生产可访问且版本号正确
    print('=== HTTPS 验证 ===')
    verify_cmd = (
        'curl -sI "https://aistudy.icu/web/style.css?v=20260719" | head -1; '
        'curl -sI "https://aistudy.icu/web/share.js?v=20260719" | head -1; '
        'curl -sI "https://aistudy.icu/web/share.html" | head -1; '
        'curl -s "https://aistudy.icu/web/share.html" | grep -E "style\\.css\\?v=|share\\.js\\?v=" | head -5'
    )
    _, stdout, _ = ssh.exec_command(verify_cmd)
    print(stdout.read().decode())

    sftp.close()
    ssh.close()
    print('部署完成。')


if __name__ == '__main__':
    main()
