#!/bin/bash
cd /opt/filesync
PW=$(grep -a '^MYSQL_DSN=' .env | sed -E 's|.*filesync:([^@]*)@.*|\1|')
echo "PW_LEN=${#PW}"
echo "===STORAGE_TYPE-DIST==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT storage_type, COUNT(*) cnt FROM files GROUP BY storage_type;" 2>&1 | head -10
echo "===TARGET-FILE==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT id, LEFT(filename,40) fn, storage_type, LEFT(storage_path,60) sp, size FROM files WHERE storage_path LIKE '%5597098ee%' OR filename LIKE '%5597098ee%' LIMIT 3;" 2>&1 | head -10
echo "===S3-FILES==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT id, LEFT(filename,30) fn, LEFT(storage_path,50) sp FROM files WHERE storage_type='s3' ORDER BY id DESC LIMIT 8;" 2>&1 | head -12
echo "===S3-COUNT-BYTYPE==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT id, LEFT(filename,30) fn, LEFT(storage_path,50) sp FROM files WHERE storage_type='s3' AND filename LIKE '%.png' OR (storage_type='s3' AND filename LIKE '%.jpg') LIMIT 6;" 2>&1 | head -10
