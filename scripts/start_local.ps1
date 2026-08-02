# FileSync local startup script (SQLite + Aliyun OSS mode)
# Purpose: Quickly validate OSS connection without local MySQL/Redis
# Usage: powershell -ExecutionPolicy Bypass -File scripts\start_local.ps1

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $projectRoot

# Load .env file (if exists), read non-sensitive base config + OSS config
# Use -Encoding UTF8 to avoid GBK mis-decoding of Chinese comments in .env file
# (observed: default Get-Content under certain PS versions mangles UTF-8 Chinese lines,
#  which can corrupt adjacent line parsing even when ASCII content looks correct)
$envFile = Join-Path $projectRoot '.env'
if (Test-Path $envFile) {
    $skipNames = @('MYSQL_DSN', 'REDIS_ADDR', 'REDIS_SENTINEL_ADDRS', 'REDIS_SENTINEL_MASTER', 'REDIS_PASSWORD', 'REDIS_DB')
    $loadedCount = 0
    Get-Content $envFile -Encoding UTF8 | ForEach-Object {
        $line = $_.Trim()
        if (-not $line -or $line.StartsWith('#')) { return }
        $idx = $line.IndexOf('=')
        if ($idx -le 0) { return }
        $name = $line.Substring(0, $idx).Trim()
        $value = $line.Substring($idx + 1).Trim()
        # Skip MySQL/Redis config, force SQLite + no Redis mode (local test simplification)
        if ($skipNames -contains $name) { return }
        [Environment]::SetEnvironmentVariable($name, $value, 'Process')
        $loadedCount++
    }
    Write-Host "[start_local] .env loaded ($loadedCount vars, skipped MySQL/Redis)" -ForegroundColor Cyan
} else {
    Write-Host "[start_local] WARNING: .env not found at $envFile" -ForegroundColor Yellow
}

# Fallback: ensure S3_ENDPOINT is set even if .env parse had issues (defensive)
# 注意：AccessKey/Secret 必须从 .env 提供，禁止硬编码在脚本中（安全要求）
if (-not [Environment]::GetEnvironmentVariable('S3_ENDPOINT', 'Process')) {
    Write-Host "[start_local] WARNING: .env 未提供 S3_ENDPOINT，OSS 模式不可用" -ForegroundColor Yellow
    Write-Host "[start_local] 请在 .env 中配置 S3_ENDPOINT/S3_REGION/S3_BUCKET/S3_ACCESS_KEY/S3_SECRET_KEY 后重试" -ForegroundColor Yellow
}

# Required env vars for local test (override .env)
[Environment]::SetEnvironmentVariable('PORT', '8080', 'Process')
[Environment]::SetEnvironmentVariable('DOMAIN', '', 'Process')
# Referer 白名单：本地 HTTP 模式下 DOMAIN 为空，需手动添加 localhost 避免 403
[Environment]::SetEnvironmentVariable('ALLOWED_REFERERS', 'localhost:8080,127.0.0.1:8080', 'Process')
[Environment]::SetEnvironmentVariable('DATA_DIR', './data_local_oss', 'Process')
[Environment]::SetEnvironmentVariable('WEB_DIR', './web', 'Process')
[Environment]::SetEnvironmentVariable('JWT_SECRET', 'local_test_jwt_secret_for_oss_validation_only', 'Process')
[Environment]::SetEnvironmentVariable('FILESYNC_INITIAL_USERNAME', 'admin', 'Process')
[Environment]::SetEnvironmentVariable('FILESYNC_INITIAL_PASSWORD', 'admin123', 'Process')

# Display current OSS config
Write-Host ""
Write-Host "=== Current OSS Config ===" -ForegroundColor Green
Write-Host "S3_ENDPOINT   = $([Environment]::GetEnvironmentVariable('S3_ENDPOINT', 'Process'))"
Write-Host "S3_REGION     = $([Environment]::GetEnvironmentVariable('S3_REGION', 'Process'))"
Write-Host "S3_BUCKET     = $([Environment]::GetEnvironmentVariable('S3_BUCKET', 'Process'))"
Write-Host "S3_ACCESS_KEY = $([Environment]::GetEnvironmentVariable('S3_ACCESS_KEY', 'Process'))"
Write-Host "S3_USE_SSL    = $([Environment]::GetEnvironmentVariable('S3_USE_SSL', 'Process'))"
Write-Host ""
Write-Host "Starting server..." -ForegroundColor Yellow
Write-Host ""

go run ./cmd/server/
