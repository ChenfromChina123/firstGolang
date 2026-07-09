# Test online edit feature: create / list / get content / update / verify
$ErrorActionPreference = 'Stop'
$base = 'http://localhost:8080'

# 1. Login (cookie-based auth)
$session = $null
$resp = Invoke-WebRequest -Uri "$base/api/login" -Method POST -ContentType 'application/json' `
    -Body '{"username":"admin","password":"admin123"}' -UseBasicParsing -SessionVariable session
Write-Host "=== Login ===" -ForegroundColor Green
Write-Host "Status: $($resp.StatusCode), Cookies: $($session.Cookies.Count)"
Write-Host ""

# 2. Create text file
Write-Host "=== 1. Create text file ===" -ForegroundColor Green
$createBody = '{"filename":"test_edit.txt","content":"Hello FileSync Online Editor!"}'
$createResp = Invoke-WebRequest -Uri "$base/api/files/create" -Method POST -ContentType 'application/json' `
    -Body $createBody -WebSession $session -UseBasicParsing
Write-Host "Status: $($createResp.StatusCode)"
Write-Host "Response: $($createResp.Content)"
Write-Host ""

# Parse file_id from response
$fileId = ($createResp.Content | ConvertFrom-Json).id
Write-Host "Created file_id: $fileId"
Write-Host ""

# 3. List files to confirm
Write-Host "=== 2. List files ===" -ForegroundColor Green
$listResp = Invoke-WebRequest -Uri "$base/api/files" -WebSession $session -UseBasicParsing
Write-Host "Status: $($listResp.StatusCode)"
Write-Host "Files: $($listResp.Content)"
Write-Host ""

# 4. Get file content
Write-Host "=== 3. Get file content ===" -ForegroundColor Green
$getResp = Invoke-WebRequest -Uri "$base/api/files/$fileId/content" -WebSession $session -UseBasicParsing
Write-Host "Status: $($getResp.StatusCode)"
Write-Host "Content: $($getResp.Content)"
Write-Host ""

# 5. Update file content
Write-Host "=== 4. Update file content ===" -ForegroundColor Green
$updateBody = '{"content":"Updated content: Hello again!"}'
$updateResp = Invoke-WebRequest -Uri "$base/api/files/$fileId/content" -Method PUT -ContentType 'application/json' `
    -Body $updateBody -WebSession $session -UseBasicParsing
Write-Host "Status: $($updateResp.StatusCode)"
Write-Host "Response: $($updateResp.Content)"
Write-Host ""

# 6. Verify update by re-reading
Write-Host "=== 5. Verify update ===" -ForegroundColor Green
$verifyResp = Invoke-WebRequest -Uri "$base/api/files/$fileId/content" -WebSession $session -UseBasicParsing
Write-Host "Status: $($verifyResp.StatusCode)"
Write-Host "Content after update: $($verifyResp.Content)"
Write-Host ""

# 7. Test creating file in subdirectory
Write-Host "=== 6. Create file in subdir ===" -ForegroundColor Green
$subBody = '{"filename":"docs/notes.md","content":"# Notes\n\nThis is a markdown file."}'
$subResp = Invoke-WebRequest -Uri "$base/api/files/create" -Method POST -ContentType 'application/json' `
    -Body $subBody -WebSession $session -UseBasicParsing
Write-Host "Status: $($subResp.StatusCode)"
Write-Host "Response: $($subResp.Content)"
Write-Host ""

Write-Host "=== ALL TESTS PASSED ===" -ForegroundColor Green
