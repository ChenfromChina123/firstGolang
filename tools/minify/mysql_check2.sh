#!/bin/bash
cd /opt/filesync
PW=$(grep -a '^MYSQL_DSN=' .env | sed -E 's|.*filesync:([^@]*)@.*|\1|')
echo "===BY-ID==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT id, LEFT(filename,50) fn, storage_type, LEFT(storage_path,70) sp, status FROM files WHERE id LIKE '5597098ee%';" 2>&1 | grep -v Warning | head -8
echo "===BY-SHARD-55==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT id, LEFT(filename,40) fn, storage_type, LEFT(storage_path,60) sp FROM files WHERE storage_path LIKE '%55/97/%';" 2>&1 | grep -v Warning | head -8
echo "===ALL-PNG-S3==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT COUNT(*) total, SUM(storage_type='s3') s3cnt FROM files WHERE filename LIKE '%.png';" 2>&1 | grep -v Warning | head -5
echo "===TABLES==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SHOW TABLES;" 2>&1 | grep -v Warning | head -20
