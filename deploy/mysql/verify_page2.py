"""验证前端页面和 API 完整性（修复 f-string 转义）"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== 1. 验证首页可访问 ===')
print(run('curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}" https://aistudy.icu/web/'))

print('\n=== 2. 验证 style.css 可访问 ===')
print(run('curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}" https://aistudy.icu/web/style.css'))

print('\n=== 3. 验证 app.js 可访问 ===')
print(run('curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}" https://aistudy.icu/web/app.js'))

print('\n=== 4. 检查 prism.min.js 路径 ===')
print(run('ls -la /opt/filesync/web/lib/prism/ 2>&1 | head -10'))

print('\n=== 5. 验证 Markdown 内容流 ===')
md_id = '9e132cd26f0b6825ff47e13b936ab06b'
print(run('curl -s -b /tmp/cookies_test.txt "https://aistudy.icu/api/preview/' + md_id + '/content" | head -c 300'))

print('\n\n=== 6. 验证视频海报图片 ===')
video_id = '3e3d38e2199374ee76d343616789f52b'
cmd = 'curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download} CONTENT_TYPE:%{content_type}" -b /tmp/cookies_test.txt "https://aistudy.icu/api/preview/' + video_id + '/poster"'
print(run(cmd))

print('\n=== 7. 验证视频内容流 Range 请求 ===')
cmd2 = 'curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}" -H "Range: bytes=0-1023" -b /tmp/cookies_test.txt "https://aistudy.icu/api/preview/' + video_id + '/content"'
print(run(cmd2))

print('\n=== 8. 检查 _posters 缓存目录 ===')
print(run('ls -la /opt/filesync/data/_posters/ 2>&1'))
