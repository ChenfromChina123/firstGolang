"""通过 curl 验证前端页面加载"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== 1. 验证首页可访问 ===')
print(run('curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}" https://aistudy.icu/web/'))

print('\n=== 2. 验证 style.css 可访问 ===')
print(run('curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}" https://aistudy.icu/web/style.css'))

print('\n=== 3. 验证 app.js 可访问 ===')
print(run('curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}" https://aistudy.icu/web/app.js'))

print('\n=== 4. 验证 prism.min.js 可访问 ===')
print(run('curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}" https://aistudy.icu/web/lib/prism/prism.min.js'))

print('\n=== 5. 验证 Markdown 内容流（获取 .md 文件内容前 200 字符）===')
md_id = '9e132cd26f0b6825ff47e13b936ab06b'
print(run(f'curl -s -b /tmp/cookies_test.txt "https://aistudy.icu/api/preview/{md_id}/content" | head -c 200'))

print('\n\n=== 6. 验证视频海报图片（检查文件类型）===')
video_id = '3e3d38e2199374ee76d343616789f52b'
print(run(f'curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download} CONTENT_TYPE:%{{content_type}}" -b /tmp/cookies_test.txt "https://aistudy.icu/api/preview/{video_id}/poster"'))

print('\n=== 7. 验证视频内容流 Range 请求 ===')
print(run(f'curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}" -H "Range: bytes=0-1023" -b /tmp/cookies_test.txt "https://aistudy.icu/api/preview/{video_id}/content"'))
