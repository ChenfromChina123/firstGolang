#!/bin/bash
cd /opt/filesync
PW=$(grep -a '^MYSQL_DSN=' .env | sed -E 's|.*filesync:([^@]*)@.*|\1|')
echo "===EXACT-ID==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT COUNT(*) cnt FROM files WHERE id='5597098ee16442ff266df6e54d914337'; SELECT id, LEFT(filename,40) fn, storage_type, LEFT(storage_path,60) sp FROM files WHERE id='5597098ee16442ff266df6e54d914337';" 2>&1 | grep -v Warning | head -8
echo "===FILES-SCHEMA==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SHOW CREATE TABLE files\G" 2>&1 | grep -v Warning | head -40
echo "===SHARES-SCHEMA==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SHOW CREATE TABLE shares\G" 2>&1 | grep -v Warning | head -25
echo "===S3-LOCAL-DUP-CHECK==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT LEFT(storage_path,4) pfx, COUNT(*) cnt FROM files GROUP BY LEFT(storage_path,4);" 2>&1 | grep -v Warning | head -10
