"""创建测试分享用于验证预览按钮布局.

登录生产 → 查文件列表 → 找一个可预览的文件（图片/文本/PDF）→ 创建分享 → 输出分享链接。
"""
import json
import sys
import urllib.request
import http.cookiejar


BASE = 'https://aistudy.icu'
USERNAME = 'admin'
PASSWORD = '***REMOVED_PASSWORD***'


def http_request(url, data=None, cookies=None, method='GET'):
    """发送 HTTP 请求，返回 (status, body, cookies)."""
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
    """登录 → 查文件 → 创建分享."""
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
    # 提取 cookie 中的 token
    cookie_str = cookie.split(';')[0] if cookie else ''
    print(f'cookie: {cookie_str[:40]}...')

    # 2. 查文件列表（找可预览的文件）
    print('\n=== 查文件列表 ===')
    status, body, _ = http_request(f'{BASE}/api/files', cookies=cookie_str)
    print(f'files status={status}')
    if status != 200:
        print(body)
        sys.exit(1)
    files_data = json.loads(body)
    # /api/files 可能返回 list 或 {files: [...]} 结构，兼容两种
    if isinstance(files_data, list):
        files = files_data
    else:
        files = files_data.get('files', []) or files_data.get('data', [])
    print(f'共 {len(files)} 个文件')
    if len(files) > 0:
        print(f'第一个文件结构: {json.dumps(files[0], ensure_ascii=False)}')

    # 找可预览的文件：优先文本（避免敏感图片），其次 PDF，最后图片
    # 本次仅验证 UI 按钮布局，用非敏感文本文件即可
    text_exts = ['txt', 'md', 'log', 'json']
    pdf_exts = ['pdf']
    image_exts = ['jpg', 'jpeg', 'png', 'gif', 'webp']

    def get_ext(f):
        fname = f.get('filename', '') or f.get('name', '')
        return fname.rsplit('.', 1)[-1].lower() if '.' in fname else ''

    def get_fname(f):
        return f.get('filename', '') or f.get('name', '')

    target = None
    for f in files:
        if get_ext(f) in text_exts:
            target = f
            break
    if not target:
        for f in files:
            if get_ext(f) in pdf_exts:
                target = f
                break
    if not target:
        for f in files:
            if get_ext(f) in image_exts:
                target = f
                break

    if not target:
        print('未找到可预览的文件，请先上传一个测试文件')
        sys.exit(1)

    print(f'目标文件: id={target.get("id")} name={get_fname(target)} size={target.get("size")}')

    # 3. 删除之前创建的图片测试分享（避免敏感信息泄露）
    old_share_id = '2bdba32a'
    print(f'\n=== 删除旧测试分享 {old_share_id} ===')
    status, body, _ = http_request(f'{BASE}/api/share/{old_share_id}', cookies=cookie_str, method='DELETE')
    print(f'delete status={status} body={body[:200] if body else ""}')

    # 4. 创建分享
    print('\n=== 创建分享 ===')
    status, body, _ = http_request(f'{BASE}/api/share', {
        'file_id': target.get('id'),
        'share_type': 'file',
        'expires_in': 3600,  # 1小时有效期
    }, cookies=cookie_str, method='POST')
    print(f'share status={status}')
    if status != 200:
        print(body)
        sys.exit(1)
    share_data = json.loads(body)
    share_id = share_data.get('id') or share_data.get('share_id')
    print(f'分享 ID: {share_id}')
    print(f'分享链接: {BASE}/web/share.html?id={share_id}')
    print(f'\n访问该链接验证预览按钮位置（应在下载按钮上方，带间距）')


if __name__ == '__main__':
    main()
