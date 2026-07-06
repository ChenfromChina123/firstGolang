# FileSync Share Password Feature Test
# Tests: create share with/without password, password gate, auth, download after auth
# All output in English to avoid PowerShell encoding issues

$ErrorActionPreference = "Continue"
$baseUrl = "http://localhost:8080"
$cookieFile = "$env:TEMP\filesync_share_test_cookies.txt"
$visitorCookieFile = "$env:TEMP\filesync_share_visitor_cookies.txt"
$testFile = "$env:TEMP\filesync_share_test_file.txt"
$testFile2 = "$env:TEMP\filesync_share_test_file2.txt"
$results = @()
$pass = 0
$fail = 0

# Remove old cookie files
Remove-Item $cookieFile -ErrorAction SilentlyContinue
Remove-Item $visitorCookieFile -ErrorAction SilentlyContinue

# Create test files
"This is test file content for share password testing." | Out-File -FilePath $testFile -Encoding utf8
"Second test file for directory share." | Out-File -FilePath $testFile2 -Encoding utf8

function Test-Case($name, $condition, $detail = "") {
    if ($condition) {
        Write-Host "[PASS] $name" -ForegroundColor Green
        $script:pass++
        $script:results += "[PASS] $name"
    } else {
        Write-Host "[FAIL] $name $detail" -ForegroundColor Red
        $script:fail++
        $script:results += "[FAIL] $name $detail"
    }
}

function Http-Get($url, $cookieJar) {
    $tmp = "$env:TEMP\filesync_resp.json"
    $code = & curl.exe -s -c $cookieJar -b $cookieJar -o $tmp -w "%{http_code}" $url
    $body = if (Test-Path $tmp) { Get-Content $tmp -Raw } else { "" }
    return @{ status = "$code"; body = $body }
}

function Http-Post($url, $body, $cookieJar, $contentType = "application/json") {
    $tmp = "$env:TEMP\filesync_resp.json"
    $bodyFile = "$env:TEMP\filesync_post_body.json"
    # Write body to file WITHOUT BOM to avoid JSON parse errors
    [System.IO.File]::WriteAllText($bodyFile, $body, [System.Text.UTF8Encoding]::new($false))
    $code = & curl.exe -s -c $cookieJar -b $cookieJar -o $tmp -w "%{http_code}" -X POST -H "Content-Type: $contentType" --data "@$bodyFile" $url
    $respBody = if (Test-Path $tmp) { Get-Content $tmp -Raw } else { "" }
    return @{ status = "$code"; body = $respBody }
}

Write-Host "`n========================================" -ForegroundColor Cyan
Write-Host " FileSync Share Password Feature Test" -ForegroundColor Cyan
Write-Host "========================================`n" -ForegroundColor Cyan

# === Test 1: Login as admin ===
Write-Host "`n--- Test 1: Login as admin ---" -ForegroundColor Yellow
$loginBody = '{"username":"admin","password":"***REMOVED_PASSWORD***"}'
$resp = Http-Post "$baseUrl/api/login" $loginBody $cookieFile
Test-Case "1.1 Login as admin" ($resp.body -match '"success"\s*:\s*true') "Response: $($resp.body)"

# === Test 2: Upload test file ===
Write-Host "`n--- Test 2: Upload test file ---" -ForegroundColor Yellow
$timestamp = Get-Date -Format "yyyyMMddHHmmss"
$testFilename = "share_pwd_test_$timestamp.txt"
$initBody = "{`"filename`":`"$testFilename`",`"file_size`":50,`"chunk_size`":524288,`"storage`":`"local`"}"
$resp = Http-Post "$baseUrl/api/upload/init" $initBody $cookieFile
$sessionId = ""
if ($resp.body -match '"session_id"\s*:\s*"([^"]+)"') { $sessionId = $matches[1] }
Test-Case "2.1 Init upload" ($sessionId -ne "") "Session: $sessionId, Body: $($resp.body)"

# Upload chunk
if ($sessionId) {
    & curl.exe -s -c $cookieFile -b $cookieFile -X POST -F "session_id=$sessionId" -F "chunk_index=0" -F "chunk_data=@$testFile" "$baseUrl/api/upload/chunk" 2>$null | Out-Null
    $completeBody = "{`"session_id`":`"$sessionId`"}"
    $resp = Http-Post "$baseUrl/api/upload/complete" $completeBody $cookieFile
    $fileId = ""
    if ($resp.body -match '"file_id"\s*:\s*"([^"]+)"') { $fileId = $matches[1] }
    Test-Case "2.2 Complete upload" ($fileId -ne "") "FileID: $fileId"
}

# === Test 3: Create share WITHOUT password ===
Write-Host "`n--- Test 3: Create share without password ---" -ForegroundColor Yellow
$shareBody = "{`"file_id`":`"$fileId`",`"share_type`":`"file`",`"expires_in`":0}"
$resp = Http-Post "$baseUrl/api/share" $shareBody $cookieFile
$shareIdNoPwd = ""
if ($resp.body -match '"id"\s*:\s*"([^"]+)"') { $shareIdNoPwd = $matches[1] }
Test-Case "3.1 Create share without password" ($shareIdNoPwd -ne "") "ShareID: $shareIdNoPwd"

# === Test 4: Create share WITH password ===
Write-Host "`n--- Test 4: Create share with password ---" -ForegroundColor Yellow
$shareBodyPwd = "{`"file_id`":`"$fileId`",`"share_type`":`"file`",`"expires_in`":0,`"password`":`"secret123`"}"
$resp = Http-Post "$baseUrl/api/share" $shareBodyPwd $cookieFile
$shareIdWithPwd = ""
if ($resp.body -match '"id"\s*:\s*"([^"]+)"') { $shareIdWithPwd = $matches[1] }
Test-Case "4.1 Create share with password" ($shareIdWithPwd -ne "") "ShareID: $shareIdWithPwd"

# === Test 5: Access share without password (public) ===
Write-Host "`n--- Test 5: Access share without password ---" -ForegroundColor Yellow
$resp = Http-Get "$baseUrl/api/s/$shareIdNoPwd" $visitorCookieFile
Test-Case "5.1 Get share info (no password)" ($resp.body -match '"has_password"\s*:\s*false') "Response: $($resp.body)"
Test-Case "5.2 Has download_token (no password)" ($resp.body -match '"download_token"') "Should have download_token"

# === Test 6: Access share WITH password (unauthenticated) ===
Write-Host "`n--- Test 6: Access share with password (unauthenticated) ---" -ForegroundColor Yellow
# Use fresh cookie jar to simulate unauthenticated visitor
$freshCookie = "$env:TEMP\filesync_fresh_visitor.txt"
Remove-Item $freshCookie -ErrorAction SilentlyContinue
$resp = Http-Get "$baseUrl/api/s/$shareIdWithPwd" $freshCookie
Test-Case "6.1 Get share info (has password)" ($resp.body -match '"has_password"\s*:\s*true') "Response: $($resp.body)"
Test-Case "6.2 No download_token (unauthenticated)" ($resp.body -notmatch '"download_token"') "Should NOT have download_token"

# === Test 7: Try download without auth (should 401) ===
Write-Host "`n--- Test 7: Download without auth ---" -ForegroundColor Yellow
$resp = Http-Get "$baseUrl/api/s/$shareIdWithPwd/download?token=fake" $freshCookie
Test-Case "7.1 Download blocked without auth" ($resp.status -eq 401 -or $resp.status -eq 403) "Status: $($resp.status)"

# === Test 8: Try wrong password (should 401) ===
Write-Host "`n--- Test 8: Wrong password ---" -ForegroundColor Yellow
$wrongPwdBody = '{"password":"wrongpassword"}'
$resp = Http-Post "$baseUrl/api/s/$shareIdWithPwd/auth" $wrongPwdBody $freshCookie
Test-Case "8.1 Wrong password rejected" ($resp.status -eq 401) "Status: $($resp.status), Body: $($resp.body)"

# === Test 9: Correct password (should 200 + set cookie) ===
Write-Host "`n--- Test 9: Correct password ---" -ForegroundColor Yellow
$correctPwdBody = '{"password":"secret123"}'
$resp = Http-Post "$baseUrl/api/s/$shareIdWithPwd/auth" $correctPwdBody $freshCookie
Test-Case "9.1 Correct password accepted" ($resp.status -eq 200 -or $resp.body -match '"success"\s*:\s*true') "Status: $($resp.status), Body: $($resp.body)"
# Check cookie was set
$cookieContent = if (Test-Path $freshCookie) { Get-Content $freshCookie -Raw } else { "" }
Test-Case "9.2 Auth cookie set" ($cookieContent -match "share_auth_") "Cookie: $cookieContent"

# === Test 10: Access share after auth (should return download_token) ===
Write-Host "`n--- Test 10: Access share after auth ---" -ForegroundColor Yellow
$resp = Http-Get "$baseUrl/api/s/$shareIdWithPwd" $freshCookie
Test-Case "10.1 Has download_token after auth" ($resp.body -match '"download_token"') "Response: $($resp.body)"

# === Test 11: List shares (should show has_password field) ===
Write-Host "`n--- Test 11: List shares ---" -ForegroundColor Yellow
$resp = Http-Get "$baseUrl/api/share" $cookieFile
Test-Case "11.1 List shares has has_password field" ($resp.body -match '"has_password"') "Response: $($resp.body)"
Test-Case "11.2 Password share marked" ($resp.body -match '"has_password"\s*:\s*true') "Should have at least one with password"

# === Test 12: Auth on no-password share (should 400) ===
Write-Host "`n--- Test 12: Auth on no-password share ---" -ForegroundColor Yellow
$resp = Http-Post "$baseUrl/api/s/$shareIdNoPwd/auth" $correctPwdBody $freshCookie
Test-Case "12.1 Auth on no-password share rejected" ($resp.status -eq 400) "Status: $($resp.status)"

# === Test 13: Password too long (should 400) ===
Write-Host "`n--- Test 13: Password too long ---" -ForegroundColor Yellow
$longPwd = "a" * 65
$longPwdBody = "{`"file_id`":`"$fileId`",`"share_type`":`"file`",`"expires_in`":0,`"password`":`"$longPwd`"}"
$resp = Http-Post "$baseUrl/api/share" $longPwdBody $cookieFile
Test-Case "13.1 Password >64 chars rejected" ($resp.status -eq 400) "Status: $($resp.status)"

# === Cleanup: Delete test shares ===
Write-Host "`n--- Cleanup: Delete test shares ---" -ForegroundColor Yellow
if ($shareIdNoPwd) {
    & curl.exe -s -c $cookieFile -b $cookieFile -X DELETE "$baseUrl/api/share/$shareIdNoPwd" 2>$null | Out-Null
}
if ($shareIdWithPwd) {
    & curl.exe -s -c $cookieFile -b $cookieFile -X DELETE "$baseUrl/api/share/$shareIdWithPwd" 2>$null | Out-Null
}
Write-Host "Test shares deleted." -ForegroundColor Gray

# === Summary ===
Write-Host "`n========================================" -ForegroundColor Cyan
Write-Host " Test Summary" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host " Passed: $pass" -ForegroundColor Green
Write-Host " Failed: $fail" -ForegroundColor Red
Write-Host " Total:  $($pass + $fail)" -ForegroundColor Cyan

if ($fail -gt 0) {
    Write-Host "`nFailed tests:" -ForegroundColor Red
    foreach ($r in $results) {
        if ($r -match "^\[FAIL\]") { Write-Host "  $r" -ForegroundColor Red }
    }
}

Write-Host "`n"
exit $fail
