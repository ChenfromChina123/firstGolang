#!/bin/bash
# 树形结构端到端测试：路径枚举方案验证
# 测试：健康检查、带路径上传、prefix 过滤、路径校验、前端页面
BASE=http://127.0.0.1:8888
PASS=0
FAIL=0

ok()   { echo "[PASS] $1"; PASS=$((PASS+1)); }
fail() { echo "[FAIL] $1"; FAIL=$((FAIL+1)); }

echo "=== 1. 健康检查 ==="
H=$(curl -s $BASE/api/health)
echo "$H"
echo "$H" | grep -q '"healthy":true' && ok "健康检查" || fail "健康检查"

echo
echo "=== 2. 上传带路径文件 docs/test.txt ==="
RESP=$(curl -s -X POST $BASE/api/upload/init -H 'Content-Type: application/json' -d '{"filename":"docs/test.txt","file_size":12,"chunk_size":512,"storage":"local"}')
echo "init: $RESP"
SID=$(echo "$RESP" | grep -o '"session_id":"[^"]*"' | sed 's/"session_id":"//;s/"//')
if [ -n "$SID" ]; then
    echo "hello world" | curl -s -X POST $BASE/api/upload/chunk -F session_id=$SID -F chunk_index=0 -F chunk_data=@- > /dev/null
    C=$(curl -s -X POST $BASE/api/upload/complete -H 'Content-Type: application/json' -d "{\"session_id\":\"$SID\"}")
    echo "complete: $C"
    echo "$C" | grep -q '"file_id"' && ok "带路径上传 docs/test.txt" || fail "带路径上传"
else
    fail "获取 session_id 失败"
fi

echo
echo "=== 3. GET /api/files?prefix=docs/ (应含 docs/test.txt) ==="
L=$(curl -s "$BASE/api/files?prefix=docs/")
echo "$L"
echo "$L" | grep -q 'docs/test.txt' && ok "prefix=docs/ 过滤" || fail "prefix 过滤"

echo
echo "=== 4. 路径校验（应返回 400）==="
echo -n "5a ../etc/passwd: "
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/upload/init -H 'Content-Type: application/json' -d '{"filename":"../etc/passwd","file_size":1,"chunk_size":512,"storage":"local"}')
echo "$CODE"
[ "$CODE" = "400" ] && ok "拒绝 ../" || fail "应拒绝 ../"

echo -n "5b /abs/path: "
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/upload/init -H 'Content-Type: application/json' -d '{"filename":"/abs/path","file_size":1,"chunk_size":512,"storage":"local"}')
echo "$CODE"
[ "$CODE" = "400" ] && ok "拒绝 /abs" || fail "应拒绝 /abs"

echo -n "5c a//b: "
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/upload/init -H 'Content-Type: application/json' -d '{"filename":"a//b","file_size":1,"chunk_size":512,"storage":"local"}')
echo "$CODE"
[ "$CODE" = "400" ] && ok "拒绝 a//b" || fail "应拒绝 a//b"

echo -n "5d 反斜杠 a\\b: "
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/upload/init -H 'Content-Type: application/json' -d '{"filename":"a\\b","file_size":1,"chunk_size":512,"storage":"local"}')
echo "$CODE"
[ "$CODE" = "400" ] && ok "拒绝反斜杠" || fail "应拒绝反斜杠"

echo
echo "=== 5. 前端页面 ==="
for f in "/" "/web/" "/web/index.html" "/web/app.js" "/web/style.css"; do
    CODE=$(curl -s -o /dev/null -w '%{http_code}' $BASE$f)
    echo "$f -> $CODE"
    [ "$CODE" = "200" ] && ok "前端 $f" || fail "前端 $f"
done

echo
echo "=== 测试结果 ==="
echo "PASS=$PASS  FAIL=$FAIL"
[ $FAIL -eq 0 ] && echo "全部通过" || echo "存在失败用例"
