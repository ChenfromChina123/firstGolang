"""验证前端文件部署正确"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run

print('=== 1. 验证 marked.js 可访问 ===')
print(run('curl -s -o /dev/null -w "HTTP_CODE:%{http_code} SIZE:%{size_download}" https://aistudy.icu/web/lib/marked/marked.min.js'))

print('\n=== 2. 验证 index.html 引入 marked.js ===')
print(run('grep "marked" /opt/filesync/web/index.html'))

print('\n=== 3. 验证 app.js 包含 renderMarkdown ===')
print(run('grep -c "renderMarkdown\|renderJson\|renderCsv\|renderCode" /opt/filesync/web/app.js'))

print('\n=== 4. 验证 app.js 包含 poster 属性 ===')
print(run('grep "poster" /opt/filesync/web/app.js | head -3'))

print('\n=== 5. 验证 style.css 包含 markdown-body ===')
print(run('grep -c "markdown-body\|csv-table" /opt/filesync/web/style.css'))

print('\n=== 6. 验证 app.js 版本号 ===')
print(run('grep "app.js" /opt/filesync/web/index.html'))
