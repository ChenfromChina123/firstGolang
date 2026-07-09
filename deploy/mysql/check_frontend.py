"""
检查前端文件完整性、marked.js、prism 库等。
"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== 1. web 目录文件列表 ===')
print(run('ls -la /opt/filesync/web/'))

print('\n=== 2. lib 目录 ===')
print(run('ls -la /opt/filesync/web/lib/'))

print('\n=== 3. marked 目录 ===')
print(run('ls -la /opt/filesync/web/lib/marked/'))

print('\n=== 4. prism 目录 ===')
print(run('ls -la /opt/filesync/web/lib/prism/'))

print('\n=== 5. app.js 大小和行数 ===')
print(run('wc -l /opt/filesync/web/app.js; wc -c /opt/filesync/web/app.js'))

print('\n=== 6. marked.min.js 大小 ===')
print(run('wc -c /opt/filesync/web/lib/marked/marked.min.js'))

print('\n=== 7. 测试 marked.min.js 是否可访问 ===')
print(run('curl -s -k -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}\\n" https://aistudy.icu/web/lib/marked/marked.min.js'))

print('\n=== 8. 测试 prism.min.js 是否可访问 ===')
print(run('curl -s -k -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}\\n" https://aistudy.icu/web/lib/prism/prism.min.js'))

print('\n=== 9. 测试 app.js 是否可访问 ===')
print(run('curl -s -k -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}\\n" https://aistudy.icu/web/app.js?v=20260709a'))

print('\n=== 10. 测试 style.css 是否可访问 ===')
print(run('curl -s -k -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}\\n" https://aistudy.icu/web/style.css?v=20260709a'))

print('\n=== 11. app.js 前 100 字节 ===')
print(run('head -c 100 /opt/filesync/web/app.js'))

print('\n\n=== 12. app.js 最后 100 字节 ===')
print(run('tail -c 100 /opt/filesync/web/app.js'))

print('\n\n=== 13. 检查 app.js 是否有语法错误（node 检查） ===')
print(run('node -c /opt/filesync/web/app.js 2>&1 | head -20'))
