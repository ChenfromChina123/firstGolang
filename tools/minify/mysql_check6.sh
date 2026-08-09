#!/bin/bash
cd /opt/filesync
PW=$(grep -a '^MYSQL_DSN=' .env | sed -E 's|.*filesync:([^@]*)@.*|\1|')
echo "===S3-PATHS==="
docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -N -e "SELECT storage_path FROM files WHERE storage_type='s3';" 2>&1 | grep -v Warning | head -55
echo "===LOCAL-COUNTERPART-CHECK==="
for sp in $(docker exec filesync-mysql mysql -h127.0.0.1 -P3306 -ufilesync -p"$PW" filesync -N -e "SELECT storage_path FROM files WHERE storage_type='s3';" 2>/dev/null | grep -v Warning); do
  key=${sp#s3:}
  local_path="/opt/filesync/data/$key"
  if [ -f "$local_path" ]; then
    echo "LOCAL_HAS_COPY: $key"
  fi
done
echo "===DONE==="
