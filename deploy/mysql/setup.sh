#!/bin/bash
set -e

mkdir -p /opt/filesync-mysql
cd /opt/filesync-mysql

# 上传 docker-compose.yml 和 my.cnf 已通过 SFTP 完成

echo "=== docker compose up ==="
docker compose up -d

echo ""
echo "=== wait for mysql ready (max 60s) ==="
for i in $(seq 1 12); do
  if docker exec filesync-mysql mysqladmin ping -h 127.0.0.1 -uroot -p***REMOVED_DB_PASSWORD*** --silent 2>/dev/null; then
    echo "MySQL ready after ${i}x5s"
    break
  fi
  echo "  waiting... ($i/12)"
  sleep 5
done

echo ""
echo "=== container status ==="
docker compose ps

echo ""
echo "=== test connect ==="
docker exec filesync-mysql mysql -uroot -p***REMOVED_DB_PASSWORD*** -e "
SELECT VERSION();
SHOW DATABASES;
SELECT User, Host FROM mysql.user WHERE User IN ('filesync','root');
" 2>&1 | grep -v "Using a password"

echo ""
echo "=== test from host ==="
mysql -h 127.0.0.1 -P 13306 -ufilesync -p'***REMOVED_DB_PASSWORD***' -e "SELECT VERSION(); SHOW DATABASES; USE filesync; SHOW TABLES;" 2>&1 | grep -v "Using a password"

echo ""
echo "=== memory usage ==="
docker stats --no-stream --format "table {{.Name}}\t{{.MemUsage}}\t{{.CPUPerc}}" filesync-mysql

echo ""
echo "=== disk usage ==="
du -sh /var/lib/docker/volumes/filesync-mysql_mysql_data 2>&1
