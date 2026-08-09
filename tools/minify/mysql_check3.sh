#!/bin/bash
cd /opt/filesync
PW=$(grep -a '^MYSQL_DSN=' .env | sed -E 's|.*filesync:([^@]*)@.*|\1|')
echo "===S3-FILES-FULL==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT id, LEFT(filename,50) fn, status FROM files WHERE storage_type='s3' ORDER BY id DESC LIMIT 6;" 2>&1 | grep -v Warning | head -10
echo "===STATUS-DIST==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT status, COUNT(*) cnt FROM files GROUP BY status;" 2>&1 | grep -v Warning | head -6
echo "===SHARES==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT id, LEFT(file_id,32) fid, LEFT(share_code,16) code FROM shares ORDER BY id DESC LIMIT 5;" 2>&1 | grep -v Warning | head -8
echo "===LOCAL-55-DIR-AGAIN==="
ls -d /opt/filesync/data/55 2>&1
echo "===S3-CNT-PER-OWNER==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -e "SELECT owner, COUNT(*) cnt FROM files WHERE storage_type='s3' GROUP BY owner;" 2>&1 | grep -v Warning | head -8
