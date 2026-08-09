@echo off
set PORT=8099
set DOMAIN=
set ALLOWED_REFERERS=localhost:8099,127.0.0.1:8099
set DATA_DIR=./data_v2_test
set WEB_DIR=./web
set LANDING_PAGE=/web/v2/intro.html
set JWT_SECRET=local_test_jwt_secret
set FILESYNC_INITIAL_USERNAME=admin
set FILESYNC_INITIAL_PASSWORD=admin123
start "filesync-v2-test" filesync_server_test.exe