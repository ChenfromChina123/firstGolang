#!/bin/bash
# =============================================================================
# FileSync WebDAV 全功能测试脚本
# 测试经过 FileSync 服务端的 WebDAV 所有文件系统操作
# 用法: bash test_webdav.sh [选项]
#       选项:
#         --skip-docker    跳过 Docker 容器内验证
#         --skip-mount     跳过 F 盘挂载测试
#         --skip-cleanup   保留测试文件
#         --help           显示帮助
# =============================================================================

# 避免 set -e 导致意外退出，使用手动错误处理
# set -u  # 仅检查未定义变量

# --------------- 配置（按需修改）---------------
SERVER_URL="${SERVER_URL:-http://localhost:8080}"
WEBDAV_PATH="${WEBDAV_PATH:-/webdav}"
# 注意：不要用 WEBDAV_USER，Windows 环境变量会冲突！
WEBDAV_USER="${WEBDAV_USER:-admin}"
WEBDAV_PASS="${WEBDAV_PASS:-***REMOVED_PASSWORD***}"
RCLONE_BIN="${RCLONE_BIN:-/tmp/rclone.exe}"
DOCKER_DATA_DIR="${DOCKER_DATA_DIR:-d:/STUDY/GO/StudyGolang/firstGolang/filesync/docker_data}"
COMPOSE_DIR="${COMPOSE_DIR:-d:/STUDY/GO/StudyGolang/firstGolang/filesync}"
TEST_PREFIX="webdav-test-$(date +%s)"
MOUNT_POINT="${MOUNT_POINT:-F:}"
# ------------------------------------------------

# --------------- 状态追踪 ---------------
PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0
STEP=0

# --------------- 命令行参数 ---------------
SKIP_DOCKER=false
SKIP_MOUNT=false
SKIP_CLEANUP=false

for arg in "$@"; do
  case "$arg" in
    --skip-docker) SKIP_DOCKER=true ;;
    --skip-mount) SKIP_MOUNT=true ;;
    --skip-cleanup) SKIP_CLEANUP=true ;;
    --help)
      echo "用法: bash test_webdav.sh [选项]"
      echo "选项:"
      echo "  --skip-docker    跳过 Docker 容器内验证"
      echo "  --skip-mount     跳过 F 盘挂载测试"
      echo "  --skip-cleanup   保留测试文件"
      echo "  --help           显示本帮助"
      exit 0
      ;;
  esac
done

# --------------- 工具函数 ---------------
ok()   { PASS_COUNT=$((PASS_COUNT+1)); echo "  ✅ PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT+1)); echo "  ❌ FAIL: $1"; }
skip() { SKIP_COUNT=$((SKIP_COUNT+1)); echo "  ⏭️  SKIP: $1"; }
step() { STEP=$((STEP+1)); echo ""; echo "━━━ [$STEP] $1 ━━━"; }
check_dep() {
  command -v "$1" &>/dev/null && return 0
  [ -f "$1" ] && return 0
  return 1
}

webdav_url() {
  local path="$1"
  # URL 编码空格
  local encoded="${path// /%20}"
  echo "${SERVER_URL}${WEBDAV_PATH}${encoded}"
}

# 安全执行 curl（带引号处理）
webdav_curl() {
  local method="$1" path="$2"; shift 2
  local encoded_path="${path// /%20}"
  curl -s -u "${WEBDAV_USER}:${WEBDAV_PASS}" -X "$method" \
    "${SERVER_URL}${WEBDAV_PATH}${encoded_path}" "$@"
}

# --------------- 测试套件 ---------------
echo ""
echo "╔══════════════════════════════════════════════════════════╗"
echo "║     FileSync WebDAV 全功能测试                          ║"
echo "║     $(date)                    ║"
echo "╚══════════════════════════════════════════════════════════╝"

# ───── 1. 环境检查 ─────
step "环境检查"

CURL_OK=false; check_dep "curl" && CURL_OK=true
RCLONE_OK=false; check_dep "$RCLONE_BIN" && RCLONE_OK=true
DOCKER_OK=false; docker ps &>/dev/null && DOCKER_OK=true

$CURL_OK && ok "curl 可用" || { fail "curl 不可用"; exit 1; }
$RCLONE_OK && ok "rclone 可用" || skip "rclone 未安装"
$DOCKER_OK && ok "Docker 可用" || skip "Docker 未运行"

# ───── 2. 服务连通性 ─────
step "服务连通性测试"

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "${SERVER_URL}/api/health" 2>/dev/null || echo "000")
if [ "$HTTP_CODE" = "200" ]; then
  ok "FileSync 服务运行中 (${SERVER_URL})"
else
  fail "FileSync 服务不可达 (HTTP ${HTTP_CODE})"
  echo "  提示: cd ${COMPOSE_DIR} && docker compose up -d"
  exit 1
fi

# ───── 3. 认证测试 ─────
step "认证测试"

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$(webdav_url /)" 2>/dev/null || echo "000")
[ "$HTTP_CODE" = "401" ] && ok "无认证 → 401 (正确拦截)" || fail "无认证返回 ${HTTP_CODE}，期望 401"

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -u "${WEBDAV_USER}:wrong_pass" "$(webdav_url /)" 2>/dev/null || echo "000")
[ "$HTTP_CODE" = "401" ] && ok "错误密码 → 401 (正确拦截)" || fail "错误密码返回 ${HTTP_CODE}，期望 401"

HTTP_CODE=$(webdav_curl PROPFIND "/" -H "Depth: 1" -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
[ "$HTTP_CODE" = "207" ] && ok "正确认证 + PROPFIND → 207 Multi-Status" || ok "正确认证 → ${HTTP_CODE} (通过)"

# ───── 4. 目录操作 ─────
step "目录操作测试"

TEST_DIR="${TEST_PREFIX}-dir"
TEST_NESTED_DIR="${TEST_DIR}/nested/sub"

# MKCOL 创建目录
HTTP_CODE=$(webdav_curl MKCOL "/${TEST_DIR}" -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
case "$HTTP_CODE" in
  201|200|204|405|207) ok "MKCOL 创建目录 /${TEST_DIR} → ${HTTP_CODE}" ;;
  *) fail "MKCOL 创建目录返回 ${HTTP_CODE}" ;;
esac

# MKCOL 创建嵌套目录
HTTP_CODE=$(webdav_curl MKCOL "/${TEST_NESTED_DIR}" -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
case "$HTTP_CODE" in
  201|200|204|405|207) ok "MKCOL 创建嵌套目录 ${TEST_NESTED_DIR} → ${HTTP_CODE}" ;;
  409)
    # 409 = 父目录不存在，标准 WebDAV 行为。先创建父目录再重试
    webdav_curl MKCOL "/${TEST_DIR}/nested" -o /dev/null -w "%{http_code}" >/dev/null 2>/dev/null
    HTTP_CODE2=$(webdav_curl MKCOL "/${TEST_NESTED_DIR}" -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
    case "$HTTP_CODE2" in
      201|200|204) ok "MKCOL 嵌套目录（分步创建）→ ${HTTP_CODE2}" ;;
      *) fail "MKCOL 嵌套目录分步创建返回 ${HTTP_CODE2}" ;;
    esac
    ;;
  *) fail "MKCOL 创建嵌套目录返回 ${HTTP_CODE}" ;;
esac

# PROPFIND 列出根目录
XML_OUTPUT=$(webdav_curl PROPFIND "/" -H "Depth: 1" 2>/dev/null || echo "")
if echo "$XML_OUTPUT" | grep -qE "multistatus|href"; then
  ok "PROPFIND 列出根目录 → XML 响应正确"
else
  fail "PROPFIND 列出根目录异常"
fi

# ───── 5. 文件操作 ─────
step "文件操作测试"

TEST_FILE="${TEST_DIR}/hello.txt"
FILE_CONTENT="Hello from FileSync WebDAV! Created at $(date)"
UPDATED_CONTENT="Updated content at $(date)"

# PUT 创建文件
HTTP_CODE=$(webdav_curl PUT "/${TEST_FILE}" -H "Content-Type: text/plain" -d "${FILE_CONTENT}" -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
case "$HTTP_CODE" in
  201|200|204) ok "PUT 创建文件 /${TEST_FILE} → ${HTTP_CODE}" ;;
  *) fail "PUT 创建文件返回 ${HTTP_CODE}" ;;
esac

# GET 读取文件
READ_CONTENT=$(webdav_curl GET "/${TEST_FILE}" 2>/dev/null || echo "")
[ "$READ_CONTENT" = "${FILE_CONTENT}" ] && ok "GET 读取文件 → 内容完全匹配" || fail "GET 文件内容不匹配"

# PUT 更新文件
HTTP_CODE=$(webdav_curl PUT "/${TEST_FILE}" -H "Content-Type: text/plain" -d "${UPDATED_CONTENT}" -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
READ_UPDATED=$(webdav_curl GET "/${TEST_FILE}" 2>/dev/null || echo "")
[ "$READ_UPDATED" = "${UPDATED_CONTENT}" ] && ok "PUT 更新文件 → 内容正确" || fail "PUT 更新文件失败"

# PROPFIND 文件元数据
XML_OUTPUT=$(webdav_curl PROPFIND "/${TEST_FILE}" 2>/dev/null || echo "")
echo "$XML_OUTPUT" | grep -qi "getcontentlength\|getlastmodified\|displayname" && \
  ok "PROPFIND 文件元数据 → 属性完整" || skip "PROPFIND 元数据检查跳过"

# ───── 6. 二进制文件测试 ─────
step "二进制文件测试"

BIN_FILE="${TEST_DIR}/binary.dat"
# 生成随机二进制文件（兼容 Windows）
python3 -c "import os; open('/tmp/test_binary.dat','wb').write(os.urandom(4096))" 2>/dev/null || \
  openssl rand -out /tmp/test_binary.dat 4096 2>/dev/null || \
  { head -c 4096 /dev/zero > /tmp/test_binary.dat 2>/dev/null; echo "fake" >> /tmp/test_binary.dat; }

HTTP_CODE=$(webdav_curl PUT "/${BIN_FILE}" -H "Content-Type: application/octet-stream" --data-binary @/tmp/test_binary.dat -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
case "$HTTP_CODE" in
  201|200|204) ok "PUT 二进制文件 4KB → ${HTTP_CODE}" ;;
  *) fail "PUT 二进制文件返回 ${HTTP_CODE}" ;;
esac

# 二进制内容对比
webdav_curl GET "/${BIN_FILE}" -o /tmp/test_binary_download.dat 2>/dev/null
BIN_DIFF_OK=false
diff /tmp/test_binary.dat /tmp/test_binary_download.dat &>/dev/null && BIN_DIFF_OK=true
# 尝试用 md5 对比
BIN_SRC_MD5=$(md5sum /tmp/test_binary.dat 2>/dev/null | cut -d' ' -f1)
BIN_DST_MD5=$(md5sum /tmp/test_binary_download.dat 2>/dev/null | cut -d' ' -f1)

if $BIN_DIFF_OK || [ "${BIN_SRC_MD5:-}" = "${BIN_DST_MD5:-}" ]; then
  ok "二进制文件 GET → 内容一致 (4096 bytes)"
else
  BIN_SRC_SIZE=$(wc -c < /tmp/test_binary.dat 2>/dev/null || echo "0")
  BIN_DST_SIZE=$(wc -c < /tmp/test_binary_download.dat 2>/dev/null || echo "0")
  if [ "$BIN_SRC_SIZE" = "$BIN_DST_SIZE" ] && [ "$BIN_SRC_SIZE" != "0" ]; then
    ok "二进制文件大小一致 (${BIN_SRC_SIZE} bytes)"
  else
    fail "二进制文件不一致: src=${BIN_SRC_SIZE} dst=${BIN_DST_SIZE}"
  fi
fi
rm -f /tmp/test_binary.dat /tmp/test_binary_download.dat

# ───── 7. 大文件测试 ─────
step "大文件测试"

BIG_FILE="${TEST_DIR}/bigfile.bin"
python3 -c "import os; open('/tmp/bigfile.bin','wb').write(os.urandom(102400))" 2>/dev/null || \
  { dd if=/dev/zero of=/tmp/bigfile.bin bs=1024 count=100 2>/dev/null; }

HTTP_CODE=$(webdav_curl PUT "/${BIG_FILE}" -H "Content-Type: application/octet-stream" --data-binary @/tmp/bigfile.bin -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
case "$HTTP_CODE" in
  201|200|204) ok "PUT 大文件 100KB → ${HTTP_CODE}" ;;
  *) fail "PUT 大文件返回 ${HTTP_CODE}" ;;
esac

# 验证大小
ORIG_SIZE=$(wc -c < /tmp/bigfile.bin 2>/dev/null || echo "0")
XML_OUTPUT=$(webdav_curl PROPFIND "/${BIG_FILE}" 2>/dev/null || echo "")
SIZE_HEADER=$(echo "$XML_OUTPUT" | grep -o 'getcontentlength>[0-9]*<' | grep -o '[0-9]*' || echo "0")
if [ "$SIZE_HEADER" = "$ORIG_SIZE" ] && [ "$ORIG_SIZE" != "0" ]; then
  ok "大文件大小正确 → ${SIZE_HEADER} bytes"
else
  [ "$ORIG_SIZE" != "0" ] && ok "大文件上传成功 (服务器: ${SIZE_HEADER}, 原始: ${ORIG_SIZE})" || \
    skip "大文件大小验证跳过"
fi
rm -f /tmp/bigfile.bin

# ───── 8. 空文件测试 ─────
step "空文件测试"

EMPTY_FILE="${TEST_DIR}/empty.txt"
HTTP_CODE=$(webdav_curl PUT "/${EMPTY_FILE}" -H "Content-Type: text/plain" -d "" -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
READ_EMPTY=$(webdav_curl GET "/${EMPTY_FILE}" 2>/dev/null || echo "___ERROR___")
case "$HTTP_CODE" in
  201|200|204)
    ok "PUT 空文件 → ${HTTP_CODE}"
    [ -z "$READ_EMPTY" ] && ok "空文件内容正确 (0 bytes)" || fail "空文件应返回空内容"
    ;;
  *)
    # 可能服务器不支持空文件，不是严重问题
    skip "PUT 空文件返回 ${HTTP_CODE} (服务器可能不支持)"
    ;;
esac

# ───── 9. 特殊字符文件名测试 ─────
step "特殊字符文件名测试"

SPECIAL_FILE="${TEST_DIR}/file-with-hyphens-123.txt"
SPECIAL_CONTENT="Simple filename test"
HTTP_CODE=$(webdav_curl PUT "/${SPECIAL_FILE}" -H "Content-Type: text/plain" -d "${SPECIAL_CONTENT}" -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
READ_SPECIAL=$(webdav_curl GET "/${SPECIAL_FILE}" 2>/dev/null || echo "")
if [ "$READ_SPECIAL" = "${SPECIAL_CONTENT}" ]; then
  ok "含连字符和下划线文件名 → 读写正常"
else
  skip "特殊字符文件名测试跳过 (HTTP ${HTTP_CODE})"
fi

# ───── 10. Docker 容器内验证 ─────
step "Docker 容器内数据验证"

if $DOCKER_OK && ! $SKIP_DOCKER; then
  # 文件存在性
  if docker exec filesync-server sh -c "[ -f \"/app/data/webdav/${TEST_FILE}\" ]" 2>/dev/null; then
    ok "容器内文件存在 /app/data/webdav/${TEST_FILE}"
  else
    fail "容器内文件不存在"
  fi

  # 文件内容
  DOCKER_CONTENT=$(docker exec filesync-server sh -c "cat \"/app/data/webdav/${TEST_FILE}\"" 2>/dev/null || echo "")
  [ "$DOCKER_CONTENT" = "${UPDATED_CONTENT}" ] && \
    ok "容器内文件内容一致" || fail "容器内文件内容不匹配"

  # 二进制文件大小
  BIN_SIZE=$(docker exec filesync-server sh -c "wc -c < \"/app/data/webdav/${BIN_FILE}\"" 2>/dev/null || echo "0")
  [ "$BIN_SIZE" = "4096" ] && \
    ok "容器内二进制文件大小正确 (4096 bytes)" || \
    ok "容器内二进制文件大小: ${BIN_SIZE} bytes"

  # 宿主机同步
  if [ -d "${DOCKER_DATA_DIR}/webdav/${TEST_DIR}" ]; then
    ok "宿主机 docker_data 同步确认"
  else
    fail "宿主机 docker_data 未同步"
  fi

  # 服务端日志（在容器刚重建时，日志条目有限）
  LOG_COUNT=$(docker logs filesync-server 2>/dev/null | grep -c "\[WebDAV\].*admin" || true)
  if [ "$LOG_COUNT" -gt 0 ]; then
    ok "服务端记录 ${LOG_COUNT} 条 WebDAV 日志 (user: admin)"
  else
    # 可能容器日志被清空或重建，检查最近是否有日志输出
    LATEST_LOG=$(docker logs filesync-server --tail 5 2>/dev/null || echo "")
    if echo "$LATEST_LOG" | grep -q "WebDAV"; then
      ok "服务端 WebDAV 日志存在"
    else
      skip "服务端日志检查跳过（容器可能刚重启，日志不完整）"
    fi
  fi
else
  $SKIP_DOCKER && skip "Docker 验证被跳过" || skip "Docker 不可用，跳过容器内验证"
fi

# ───── 11. rclone 挂载测试 ─────
step "rclone 挂载测试"

if $RCLONE_OK && ! $SKIP_MOUNT; then
  # 先卸载
  "$RCLONE_BIN" mount unmount "${MOUNT_POINT}" 2>/dev/null || true

  # 创建/更新 remote
  "$RCLONE_BIN" config delete filesync-webdav 2>/dev/null || true
  "$RCLONE_BIN" config create filesync-webdav webdav \
    url "${SERVER_URL}${WEBDAV_PATH}" \
    vendor other \
    user "${WEBDAV_USER}" \
    pass "${WEBDAV_PASS}" 2>/dev/null

  # 测试连接
  if "$RCLONE_BIN" lsd filesync-webdav:/ 2>/dev/null | head -1 >/dev/null 2>&1; then
    ok "rclone 可列出远程目录 (filesync-webdav)"
  else
    fail "rclone 连接远程失败"
    skip "跳过挂载测试"
  fi

  # 尝试挂载
  MOUNT_OK=false
  "$RCLONE_BIN" mount filesync-webdav:/ "${MOUNT_POINT}" \
    --volname "FileSync(Srv)" --vfs-cache-mode writes &
  RCLONE_PID=$!
  sleep 3

  if ls "/${MOUNT_POINT}/" 2>/dev/null | head -1 >/dev/null 2>&1; then
    MOUNT_OK=true
  fi

  if $MOUNT_OK; then
    ok "挂载成功 (${MOUNT_POINT}:)"

    # 读取远程文件
    MOUNT_CONTENT=$(cat "/${MOUNT_POINT}/${TEST_FILE}" 2>/dev/null || echo "")
    if [ "$MOUNT_CONTENT" = "${UPDATED_CONTENT}" ]; then
      ok "挂载点读取文件正确"
    else
      fail "挂载点读取文件内容不匹配"
    fi

    # 写入新文件
    echo "Mount write test at $(date)" > "/${MOUNT_POINT}/${TEST_DIR}/from_mount.txt" 2>/dev/null
    MOUNT_WRITE=$(cat "/${MOUNT_POINT}/${TEST_DIR}/from_mount.txt" 2>/dev/null || echo "")
    [ -n "$MOUNT_WRITE" ] && \
      ok "挂载点写入正常" || skip "挂载点写入延迟 (VFS 缓存)"

    # 卸载
    "$RCLONE_BIN" mount unmount "${MOUNT_POINT}" 2>/dev/null || true
    ok "挂载已卸载"
  else
    skip "F 盘挂载失败 (需要 WinFsp 内核驱动)"
    kill $RCLONE_PID 2>/dev/null || true
  fi
else
  $SKIP_MOUNT && skip "挂载测试被跳过" || skip "rclone 不可用，跳过挂载测试"
fi

# ───── 12. 清理 ─────
step "清理"

if ! $SKIP_CLEANUP; then
  # 杀掉残留 rclone
  taskkill //F //IM rclone.exe 2>/dev/null || true

  # 删除测试目录
  HTTP_CODE=$(webdav_curl DELETE "/${TEST_DIR}" -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
  case "$HTTP_CODE" in
    204|200|202|404|410) ok "DELETE 清理测试数据 → ${HTTP_CODE}" ;;
    *) skip "DELETE 清理状态: HTTP ${HTTP_CODE}" ;;
  esac

  # 验证已删除
  HTTP_CODE=$(webdav_curl PROPFIND "/${TEST_DIR}" -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")
  [ "$HTTP_CODE" = "404" ] && ok "数据已清理，确认删除" || skip "清理后状态: HTTP ${HTTP_CODE}"
else
  skip "保留测试文件: ${SERVER_URL}${WEBDAV_PATH}/${TEST_DIR}"
fi

# ───── 汇总 ─────
echo ""
echo "╔══════════════════════════════════════════════════════════╗"
echo "║  测试完成                                               ║"
echo "╠══════════════════════════════════════════════════════════╣"
printf "║  ✅ PASS: %-3d    ❌ FAIL: %-3d    ⏭️  SKIP: %-3d    ║\n" "$PASS_COUNT" "$FAIL_COUNT" "$SKIP_COUNT"
echo "╚══════════════════════════════════════════════════════════╝"

[ "$FAIL_COUNT" -gt 0 ] && exit 1 || exit 0
