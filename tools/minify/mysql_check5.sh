#!/bin/bash
cd /opt/filesync
PW=$(grep -a '^MYSQL_DSN=' .env | sed -E 's|.*filesync:([^@]*)@.*|\1|')
echo "===DELETED-COUNT==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT COUNT(*) cnt FROM files WHERE deleted_at IS NOT NULL;" 2>&1 | grep -v Warning | head -5
echo "===DELETED-RECENT==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT id, LEFT(filename,40) fn, deleted_at FROM files WHERE deleted_at IS NOT NULL ORDER BY deleted_at DESC LIMIT 8;" 2>&1 | grep -v Warning | head -10
echo "===S3-FILES-DELETED==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT id, LEFT(filename,40) fn, deleted_at FROM files WHERE storage_type='s3' AND deleted_at IS NOT NULL;" 2>&1 | grep -v Warning | head -10
echo "===ALL-S3-IDS==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT id FROM files WHERE storage_type='s3';" 2>&1 | grep -v Warning | head -60
