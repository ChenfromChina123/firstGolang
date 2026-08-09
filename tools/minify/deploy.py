# -*- coding: utf-8 -*-
"""上传 web/dist 混淆产物到服务器 /opt/filesync/web/"""
import os
import sys

sys.path.insert(0, 'C:/Users/Administrator/.config/ssh-mcp')
from ssh_tool import upload, run

LOCAL = r'd:/STUDY/GO/StudyGolang/firstGolang/filesync/web/dist'
REMOTE = '/opt/filesync/web'

def main():
    files = []
    for root, dirs, names in os.walk(LOCAL):
        for name in names:
            if name.lower() == 'nul':
                print(f"[SKIP] {name}")
                continue
            full = os.path.join(root, name)
            rel = os.path.relpath(full, LOCAL).replace('\\', '/')
            files.append((full, rel))

    print(f"共 {len(files)} 个文件待上传")
    ok = fail = 0
    for full, rel in sorted(files):
        remote_path = f"{REMOTE}/{rel}"
        res = upload(full, remote_path)
        if res.startswith("[OK]"):
            ok += 1
        else:
            fail += 1
            print(f"[FAIL] {rel}: {res}")
    print(f"上传完成: OK={ok} FAIL={fail}")

    # 验证关键文件
    print("=== 验证 ===")
    print(run('ls -la /opt/filesync/web/app.js /opt/filesync/web/dist/app.js 2>/dev/null; '
              'echo ---SIZES---; wc -c /opt/filesync/web/app.js /opt/filesync/web/admin.js /opt/filesync/web/share.js '
              '/opt/filesync/web/js/crypto.js /opt/filesync/web/js/login.js 2>/dev/null'))

if __name__ == '__main__':
    main()
