#!/bin/bash
# FileSync 前端 API 端到端测试脚本
set -e
BASE=http://127.0.0.1:8888

echo "=== 1. 健康检查 ==="
curl -s $BASE/api/health
echo

echo "=== 2. 初始化上传 ==="
INIT=$(curl -s -X POST "$BASE/api/upload/init" \
  -H "Content-Type: application/json" \
  -d '{"filename":"web_test.txt","file_size":12,"chunk_size":512,"storage":"local"}')
echo "$INIT"
SID=$(echo "$INIT" | grep -o '"session_id":"[^"]*"' | sed 's/"session_id":"//;s/"//')
echo "session_id=$SID"

echo "=== 3. 上传分片 0 ==="
echo "hello world" > /tmp/web_test.txt
curl -s -X POST "$BASE/api/upload/chunk" \
  -F "session_id=$SID" \
  -F "chunk_index=0" \
  -F "chunk_data=@/tmp/web_test.txt"
echo

echo "=== 4. 查询状态 ==="
curl -s "$BASE/api/upload/status?session_id=$SID"
echo

echo "=== 5. 完成上传 ==="
curl -s -X POST "$BASE/api/upload/complete" \
  -H "Content-Type: application/json" \
  -d "{\"session_id\":\"$SID\"}"
echo

echo "=== 6. 文件列表 ==="
curl -s $BASE/api/files
echo

echo "=== 7. 前端页面可访问 ==="
curl -s -o /dev/null -w "index.html: %{http_code}\n" $BASE/web/
curl -s -o /dev/null -w "style.css: %{http_code}\n" $BASE/web/style.css
curl -s -o /dev/null -w "app.js: %{http_code}\n" $BASE/web/app.js

echo "=== 测试完成 ==="
