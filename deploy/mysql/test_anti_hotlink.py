"""
防盗链完整功能测试脚本

测试内容:
  1. 登录获取 JWT
  2. 列出文件获取 file_id
  3. 创建文件分享
  4. 获取分享信息（含 download_token）
  5. 用 token 下载（验证 token 有效）
  6. 快速重复下载 12 次（验证频率限制，第 11 次后应 429）
  7. 清理：删除测试分享

依赖: requests (pip install requests)
"""
import requests
import time
import sys

BASE = "https://aistudy.icu"
# 忽略 SSL 警告（自签名/证书链）
requests.packages.urllib3.disable_warnings()

USERNAME = "test16394"
PASSWORD = "Test1234"


def main():
    s = requests.Session()
    s.verify = False

    # === 1. 登录 ===
    print("=== 1. 登录 ===")
    r = s.post(f"{BASE}/api/login", json={"username": USERNAME, "password": PASSWORD})
    print(f"  登录: {r.status_code}")
    if r.status_code != 200:
        print(f"  登录失败: {r.text}")
        sys.exit(1)

    # === 2. 列出文件 ===
    print("\n=== 2. 列出文件 ===")
    r = s.get(f"{BASE}/api/files?prefix=")
    print(f"  列文件: {r.status_code}")
    if r.status_code != 200:
        print(f"  列文件失败: {r.text}")
        sys.exit(1)
    files = r.json()
    if not files:
        print("  没有文件可用，请先上传一个文件")
        sys.exit(1)
    file_id = files[0]["id"]
    file_name = files[0].get("filename", "")
    print(f"  选用文件: id={file_id} name={file_name}")

    # === 3. 创建分享 ===
    print("\n=== 3. 创建文件分享 ===")
    r = s.post(f"{BASE}/api/share", json={
        "file_id": file_id,
        "share_type": "file",
        "expires_in": 3600  # 1 小时有效
    })
    print(f"  创建分享: {r.status_code}")
    if r.status_code != 200:
        print(f"  创建分享失败: {r.text}")
        sys.exit(1)
    share_data = r.json()
    share_id = share_data["id"]
    print(f"  分享 ID: {share_id}")

    try:
        # === 4. 获取分享信息（含 download_token）===
        print("\n=== 4. 获取分享信息（含 download_token）===")
        # 用新 session 模拟公开访问
        pub = requests.Session()
        pub.verify = False
        r = pub.get(f"{BASE}/api/s/{share_id}")
        print(f"  获取分享信息: {r.status_code}")
        if r.status_code != 200:
            print(f"  获取失败: {r.text}")
            sys.exit(1)
        info = r.json()
        token = info.get("download_token", "")
        print(f"  download_token: {token[:30]}...{token[-10:] if len(token) > 40 else token}")
        if not token:
            print("  错误：未获取到 download_token")
            sys.exit(1)

        # === 5. 用 token 下载（验证 token 有效）===
        print("\n=== 5. 用 token 下载（验证 token 有效）===")
        r = pub.get(f"{BASE}/api/s/{share_id}/download?token={token}")
        print(f"  下载: {r.status_code} (Content-Length: {len(r.content)} bytes)")
        if r.status_code != 200:
            print(f"  下载失败: {r.text[:200]}")
            sys.exit(1)
        print("  Token 有效，下载成功")

        # === 6. 频率限制测试：快速下载 12 次 ===
        print("\n=== 6. 频率限制测试（10 次/分钟，第 11 次后应 429）===")
        codes = []
        for i in range(12):
            r = pub.get(f"{BASE}/api/s/{share_id}/download?token={token}")
            codes.append(r.status_code)
            print(f"  第 {i+1:2d} 次: HTTP {r.status_code}")
            if r.status_code == 429:
                print(f"  频率限制触发！")
                break
        if 429 in codes:
            print(f"  结果: 频率限制生效 ✅（第 {codes.index(429)+1} 次触发 429）")
        else:
            print(f"  结果: 频率限制未触发 ❌（所有状态码: {codes}）")

        # === 7. 错误 token 测试 ===
        print("\n=== 7. 错误 token 测试（应 403）===")
        r = pub.get(f"{BASE}/api/s/{share_id}/download?token=invalid.token")
        print(f"  错误 token: HTTP {r.status_code} {'✅' if r.status_code == 403 else '❌'}")

        # === 8. 无 token 测试 ===
        print("\n=== 8. 无 token 测试（应 403）===")
        r = pub.get(f"{BASE}/api/s/{share_id}/download")
        print(f"  无 token: HTTP {r.status_code} {'✅' if r.status_code == 403 else '❌'}")

    finally:
        # === 清理：删除分享 ===
        print(f"\n=== 清理：删除分享 {share_id} ===")
        r = s.delete(f"{BASE}/api/share/{share_id}")
        print(f"  删除分享: {r.status_code}")

    print("\n=== 测试完成 ===")


if __name__ == "__main__":
    main()
