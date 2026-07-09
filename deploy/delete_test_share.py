"""删除测试分享保护隐私.

登录生产 → 删除指定分享 ID。
用法: python deploy/delete_test_share.py <share_id>
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
    """登录 → 删除分享."""
    share_id = sys.argv[1] if len(sys.argv) > 1 else '7c62d30f'
    print(f'=== 删除分享 {share_id} ===')
    # 1. 登录
    status, body, cookie = http_request(f'{BASE}/api/login', {
        'username': USERNAME,
        'password': PASSWORD,
    }, method='POST')
    if status != 200:
        print(f'login failed: {status} {body}')
        sys.exit(1)
    cookie_str = cookie.split(';')[0] if cookie else ''
    # 2. 删除分享
    status, body, _ = http_request(f'{BASE}/api/share/{share_id}', cookies=cookie_str, method='DELETE')
    print(f'delete status={status} body={body}')
    if status == 200:
        print(f'分享 {share_id} 已删除')
    else:
        sys.exit(1)


if __name__ == '__main__':
    main()
