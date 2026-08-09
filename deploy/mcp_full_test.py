#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""FileSync MCP 端到端测试：生成全权限令牌，测试全部 16 个工具。

测试链路：
  登录 → 生成令牌(read+write+share) → MCP 工具调用 → 创建/读取/重命名/上传/分享/删除/恢复/清理

MCP 端点：POST /mcp （Stateless + JSONResponse，PAT 认证）
"""
import requests
import json
import base64
import sys
import time

BASE = "https://aistudy.icu"
ADMIN_USER = "admin"
ADMIN_PASS = "Admin123!"
TEST_DIR = "mcp-test-2026/"
TIMEOUT = 60

from cryptography.hazmat.primitives.asymmetric import padding
from cryptography.hazmat.primitives import serialization


# ============================================================
# 1. 登录 + 生成令牌
# ============================================================

def rsa_encrypt_password(password):
    """获取 RSA 公钥并加密密码（PKCS1v15）。"""
    r = requests.get(f"{BASE}/api/pubkey", timeout=TIMEOUT)
    r.raise_for_status()
    pubkey_pem = r.json()["public_key"]
    pubkey = serialization.load_pem_public_key(pubkey_pem.encode())
    encrypted = pubkey.encrypt(password.encode(), padding.PKCS1v15())
    return base64.b64encode(encrypted).decode()


def login():
    """登录获取 JWT（通过 HttpOnly cookie fs_access_token 传递）。"""
    sess = requests.Session()
    enc_pass = rsa_encrypt_password(ADMIN_PASS)
    r = sess.post(f"{BASE}/api/login",
                  json={"username": ADMIN_USER, "password": enc_pass},
                  timeout=TIMEOUT)
    data = r.json()
    if not data.get("success"):
        print(f"[FAIL] 登录失败: {data}")
        sys.exit(1)
    # JWT 在 fs_access_token cookie 中（HttpOnly，Set-Cookie 返回）
    jwt = sess.cookies.get("fs_access_token", "")
    if not jwt:
        print(f"[FAIL] 登录成功但未找到 fs_access_token cookie")
        sys.exit(1)
    return jwt, sess


def create_full_scope_token(sess, jwt):
    """生成全权限令牌（read+write+share）。同时用 cookie 和 Authorization 头确保认证。"""
    r = sess.post(f"{BASE}/api/tokens",
                  headers={"Authorization": f"Bearer {jwt}"},
                  json={
                      "name": "mcp-full-test",
                      "scopes": ["filesync:read", "filesync:write", "filesync:share"],
                      "expires_in": 3600,
                  }, timeout=TIMEOUT)
    data = r.json()
    if not data.get("token"):
        print(f"[FAIL] 生成令牌失败: status={r.status_code} body={data}")
        sys.exit(1)
    return data["token"]


# ============================================================
# 2. MCP 工具调用（Stateless，直接 tools/call）
# ============================================================

_msg_id = 0


def mcp_call(token, method, params=None):
    """调用 MCP JSON-RPC 方法。"""
    global _msg_id
    _msg_id += 1
    body = {"jsonrpc": "2.0", "id": _msg_id, "method": method}
    if params is not None:
        body["params"] = params
    r = requests.post(f"{BASE}/mcp",
                      headers={
                          "Authorization": f"Bearer {token}",
                          "Content-Type": "application/json",
                          "Accept": "application/json, text/event-stream",
                      }, json=body, timeout=TIMEOUT)
    try:
        return r.json()
    except Exception:
        return {"error": f"HTTP {r.status_code}: {r.text[:200]}"}


def call_tool(token, name, args=None):
    """调用 MCP 工具并提取结果。"""
    resp = mcp_call(token, "tools/call", {"name": name, "arguments": args or {}})
    if "error" in resp and "result" not in resp:
        return {"_error": resp["error"]}
    result = resp.get("result", {})
    if result.get("isError"):
        # 工具返回错误
        content = result.get("content", [])
        if content and isinstance(content, list):
            return {"_error": content[0].get("text", "unknown error")}
        return {"_error": "unknown tool error"}
    # 优先返回 structuredContent
    if "structuredContent" in result:
        return result["structuredContent"]
    # 回退到 content text
    content = result.get("content", [])
    if content and isinstance(content, list):
        try:
            return json.loads(content[0].get("text", "{}"))
        except Exception:
            return {"_text": content[0].get("text", "")}
    return {}


# ============================================================
# 3. 测试用例
# ============================================================

def run_tests(token):
    """运行全部 16 个工具的端到端测试。"""
    results = []  # (工具名, 状态, 详情)

    def check(name, ok, detail=""):
        status = "✅ PASS" if ok else "❌ FAIL"
        results.append((name, status, detail))
        print(f"  {status}  {name}: {detail[:120]}")

    print("\n" + "=" * 70)
    print("MCP 端到端测试（全权限令牌 read+write+share）")
    print("=" * 70)

    # --- 3.1 whoami：验证身份和 scope ---
    print("\n[1/16] whoami - 验证身份和权限边界")
    r = call_tool(token, "whoami")
    identity = r.get("identity", {})
    scopes = identity.get("scopes", [])
    check("whoami", "filesync:read" in scopes and "filesync:write" in scopes and "filesync:share" in scopes,
          f"scopes={scopes} user={identity.get('username')}")

    # --- 3.2 fs_list：初始状态 ---
    print("\n[2/16] fs_list - 列出根目录（初始）")
    r = call_tool(token, "fs_list", {"path": ""})
    check("fs_list(初始)", "dirs" in r or "files" in r,
          f"dirs={len(r.get('dirs', []))} files={len(r.get('files', []))}")

    # --- 3.3 fs_mkdir：创建测试目录 ---
    print(f"\n[3/16] fs_mkdir - 创建测试目录 {TEST_DIR}")
    r = call_tool(token, "fs_mkdir", {"path": TEST_DIR})
    check("fs_mkdir", r.get("created") is True, f"created={r.get('created')} path={r.get('path')}")

    # --- 3.4 fs_write：写入文本文件 ---
    test_content = "Hello from MCP test! 这是端到端测试文件。"
    test_file = TEST_DIR + "hello.txt"
    print(f"\n[4/16] fs_write - 写入文本文件 {test_file}")
    r = call_tool(token, "fs_write", {"path": test_file, "content": test_content})
    file_id = r.get("file", {}).get("id", "")
    check("fs_write", r.get("created") is True, f"created={r.get('created')} file_id={file_id} size={r.get('file', {}).get('size')}")

    # --- 3.5 fs_list：确认文件存在 ---
    print(f"\n[5/16] fs_list - 列出 {TEST_DIR} 确认文件存在")
    r = call_tool(token, "fs_list", {"path": TEST_DIR})
    files = r.get("files", [])
    found = any(f.get("name", "").endswith("hello.txt") for f in files)
    check("fs_list(确认)", found, f"files={len(files)} found_hello={found}")

    # --- 3.6 fs_read：读取文件内容 ---
    print(f"\n[6/16] fs_read - 读取 {test_file} 内容")
    r = call_tool(token, "fs_read", {"path": test_file})
    content = r.get("content", "")
    check("fs_read", content == test_content, f"content='{content[:50]}' match={content == test_content}")

    # --- 3.7 fs_stat：获取文件元信息 ---
    print(f"\n[7/16] fs_stat - 获取 {test_file} 元信息")
    r = call_tool(token, "fs_stat", {"path": test_file})
    f_info = r.get("file", {})
    check("fs_stat", f_info.get("size", 0) > 0, f"size={f_info.get('size')} hash={f_info.get('hash', '')[:16]} owner={f_info.get('owner')}")

    # --- 3.8 fs_rename：重命名文件 ---
    renamed_file = TEST_DIR + "hello-renamed.txt"
    print(f"\n[8/16] fs_rename - 重命名 {test_file} → {renamed_file}")
    r = call_tool(token, "fs_rename", {"path": test_file, "new_path": renamed_file})
    check("fs_rename", r.get("renamed") is True, f"renamed={r.get('renamed')} new_path={r.get('new_path')}")

    # --- 3.9 fs_upload：上传二进制文件（base64） ---
    binary_data = bytes(range(256)) * 4  # 1024 字节二进制
    b64_content = base64.b64encode(binary_data).decode()
    binary_file = TEST_DIR + "binary.bin"
    print(f"\n[9/16] fs_upload - 上传二进制文件 {binary_file} ({len(binary_data)} bytes)")
    r = call_tool(token, "fs_upload", {"path": binary_file, "content_base64": b64_content})
    check("fs_upload", r.get("uploaded") is True, f"uploaded={r.get('uploaded')} size={r.get('file', {}).get('size')}")

    # --- 3.10 fs_download：生成下载链接 ---
    print(f"\n[10/16] fs_download - 为 {renamed_file} 生成下载链接")
    r = call_tool(token, "fs_download", {"path": renamed_file})
    dl_url = r.get("download_url", "")
    check("fs_download", dl_url != "", f"url={dl_url[:80]}... expires_in={r.get('expires_in')}")

    # --- 3.11 share_create：创建文件分享 ---
    print(f"\n[11/16] share_create - 为 {renamed_file} 创建分享")
    r = call_tool(token, "share_create", {"file_id": file_id, "share_type": "file", "expires_in": 3600})
    share = r.get("share", {})
    share_id = share.get("id", "")
    share_url = share.get("url", "")
    check("share_create", share_id != "", f"share_id={share_id} url={share_url[:80]}")

    # --- 3.12 share_list：列出分享 ---
    print(f"\n[12/16] share_list - 列出当前用户分享")
    r = call_tool(token, "share_list", {})
    shares = r.get("shares", [])
    check("share_list", len(shares) > 0, f"shares={len(shares)}")

    # --- 3.13 fs_delete：删除文件 ---
    print(f"\n[13/16] fs_delete - 删除 {renamed_file}")
    r = call_tool(token, "fs_delete", {"paths": [renamed_file]})
    check("fs_delete", r.get("deleted", 0) >= 1, f"deleted={r.get('deleted')}")

    # --- 3.14 fs_trash_list：查看回收站 ---
    print(f"\n[14/16] fs_trash_list - 查看回收站")
    r = call_tool(token, "fs_trash_list", {})
    trash = r.get("trash", [])
    check("fs_trash_list", "trash" in r, f"trash_items={len(trash)}")

    # --- 3.15 fs_trash_restore：恢复文件 ---
    print(f"\n[15/16] fs_trash_restore - 恢复删除的文件")
    restored = False
    if trash:
        # 找到刚删除的文件
        target = None
        for t in trash:
            if "hello-renamed" in str(t.get("filename", "")) or t.get("id") == file_id:
                target = t
                break
        if target:
            r = call_tool(token, "fs_trash_restore", {"file_id": target.get("id", file_id)})
            restored = r.get("restored") is True
            check("fs_trash_restore", restored, f"restored={restored} file_id={target.get('id', file_id)}")
        else:
            check("fs_trash_restore", False, "回收站中未找到目标文件")
    else:
        check("fs_trash_restore", False, "回收站为空")

    # --- 3.16 share_delete：删除分享 ---
    print(f"\n[16/16] share_delete - 删除分享 {share_id}")
    r = call_tool(token, "share_delete", {"share_id": share_id})
    check("share_delete", r.get("deleted") is True, f"deleted={r.get('deleted')} share_id={share_id}")

    return results


# ============================================================
# 4. 清理测试数据
# ============================================================

def cleanup(token, results):
    """清理测试产生的文件和分享。"""
    print("\n" + "=" * 70)
    print("清理测试数据")
    print("=" * 70)

    # 删除测试目录下所有文件
    r = call_tool(token, "fs_list", {"path": TEST_DIR, "recursive": True})
    files = r.get("files", [])
    if files:
        paths = [f.get("name", "") for f in files if f.get("name")]
        if paths:
            r = call_tool(token, "fs_delete", {"paths": paths})
            print(f"  删除 {len(paths)} 个测试文件: deleted={r.get('deleted', 0)}")

    # 再次检查根目录
    r = call_tool(token, "fs_list", {"path": ""})
    print(f"  清理后根目录: dirs={len(r.get('dirs', []))} files={len(r.get('files', []))}")


# ============================================================
# 主流程
# ============================================================

def main():
    print("=" * 70)
    print("FileSync MCP 端到端测试")
    print(f"服务器: {BASE}")
    print("=" * 70)

    # 1. 登录
    print("\n>> 步骤 1: 登录 admin 账号")
    jwt, sess = login()
    print(f"  [OK] JWT 获取成功 (cookie fs_access_token, 长度 {len(jwt)})")

    # 2. 生成全权限令牌
    print("\n>> 步骤 2: 生成全权限令牌 (read+write+share)")
    token = create_full_scope_token(sess, jwt)
    print(f"  [OK] 令牌生成成功: {token[:20]}...{token[-8:]}")

    # 3. 运行测试
    print("\n>> 步骤 3: 运行 16 个工具端到端测试")
    results = run_tests(token)

    # 4. 清理
    cleanup(token, results)

    # 5. 汇总
    print("\n" + "=" * 70)
    print("测试汇总")
    print("=" * 70)
    passed = sum(1 for _, s, _ in results if "PASS" in s)
    failed = sum(1 for _, s, _ in results if "FAIL" in s)
    for name, status, detail in results:
        print(f"  {status}  {name}")
    print(f"\n  总计: {passed} 通过 / {failed} 失败 / {len(results)} 项")
    print("=" * 70)

    if failed > 0:
        print("\n[FAIL] 存在失败项，请检查上方详情。")
        sys.exit(1)
    else:
        print("\n[OK] 全部 16 个工具测试通过，文件操作链路完整可用。")


if __name__ == "__main__":
    main()
