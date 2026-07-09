"""创建视频测试分享用于验证预览优化.

登录生产 → 查文件列表 → 找一个视频文件 → 创建分享 → 输出分享链接。
用法: python deploy/create_video_share.py
"""
import json
import sys
import urllib.request


BASE = 'https://aistudy.icu'
USERNAME = 'admin'
PASSWORD = '***REMOVED_PASSWORD***'


def http_request(url, data=None, cookies=None, method='GET'):
    """发送 HTTP 请求，返回 (status, body, cookie)."""
    req = urllib.request.Request(url, method=method)
    if data is not None:
        req.data = json.dumps(data).encode('utf-8')
        req.add_header('Content-Type', 'application/json')
    if cookies:
        req.add_header('Cookie', cookies)
    try:
        resp = urllib.request.urlopen(req, timeout=30)
        return resp.status, resp.read().decode('utf-8'), resp.headers.get('Set-Cookie', '')
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode('utf-8'), e.headers.get('Set-Cookie', '')


def main():
    """登录 → 查文件 → 找视频 → 创建分享."""
    # 1. 登录
    print('=== 登录 ===')
    status, body, cookie = http_request(f'{BASE}/api/login', {
        'username': USERNAME,
        'password': PASSWORD,
    }, method='POST')
    print(f'login status={status}')
    if status != 200:
        print(body)
        sys.exit(1)
    cookie_str = cookie.split(';')[0] if cookie else ''
    print(f'cookie: {cookie_str[:40]}...')

    # 2. 查文件列表
    print('\n=== 查文件列表 ===')
    status, body, _ = http_request(f'{BASE}/api/files', cookies=cookie_str)
    print(f'files status={status}')
    if status != 200:
        print(body)
        sys.exit(1)
    files_data = json.loads(body)
    if isinstance(files_data, list):
        files = files_data
    else:
        files = files_data.get('files', []) or files_data.get('data', [])
    print(f'共 {len(files)} 个文件')

    # 找视频文件
    video_exts = ['mp4', 'webm', 'ogv', 'mov', 'm4v', 'mkv']

    def get_ext(f):
        fname = f.get('filename', '') or f.get('name', '')
        return fname.rsplit('.', 1)[-1].lower() if '.' in fname else ''

    def get_fname(f):
        return f.get('filename', '') or f.get('name', '')

    target = None
    for f in files:
        if get_ext(f) in video_exts:
            target = f
            break

    if not target:
        print('未找到视频文件，请先上传一个测试视频')
        sys.exit(1)

    print(f'目标视频: id={target.get("id")} name={get_fname(target)} size={target.get("size")} storage={target.get("storage_type")}')

    # 3. 创建分享
    print('\n=== 创建分享 ===')
    status, body, _ = http_request(f'{BASE}/api/share', {
        'file_id': target.get('id'),
        'share_type': 'file',
        'expires_in': 3600,
    }, cookies=cookie_str, method='POST')
    print(f'share status={status}')
    if status != 200:
        print(body)
        sys.exit(1)
    share_data = json.loads(body)
    share_id = share_data.get('id') or share_data.get('share_id')
    print(f'分享 ID: {share_id}')
    print(f'分享链接: {BASE}/web/share.html?id={share_id}')


if __name__ == '__main__':
    main()
