"""
测试第二阶段预览功能 API：
1. 登录获取 cookie
2. 查询现有文件列表，找出 md/json/csv/mp4 文件
3. 调用预览 API 验证 metadata
"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run
import json

# 1. 登录获取 cookie
print('=== 1. 登录 ===')
login_result = run("""curl -s -c /tmp/cookies_test.txt -X POST https://aistudy.icu/api/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"***REMOVED_PASSWORD***"}'""")
print(login_result)

# 2. 查询文件列表
print('\n=== 2. 查询文件列表 ===')
files_result = run("""curl -s -b /tmp/cookies_test.txt 'https://aistudy.icu/api/files?prefix='""")
try:
    data = json.loads(files_result)
    if isinstance(data, list):
        print(f'Total files: {len(data)}')
        # 找出各类型文件
        md_files = [f for f in data if f.get('filename', '').endswith('.md')]
        json_files = [f for f in data if f.get('filename', '').endswith('.json')]
        csv_files = [f for f in data if f.get('filename', '').endswith('.csv')]
        video_files = [f for f in data if any(f.get('filename', '').endswith(ext) for ext in ['.mp4', '.webm', '.mkv', '.avi', '.mov'])]

        print(f'\nMarkdown files ({len(md_files)}):')
        for f in md_files[:5]:
            print(f"  id={f.get('id')} filename={f.get('filename')}")

        print(f'\nJSON files ({len(json_files)}):')
        for f in json_files[:5]:
            print(f"  id={f.get('id')} filename={f.get('filename')}")

        print(f'\nCSV files ({len(csv_files)}):')
        for f in csv_files[:5]:
            print(f"  id={f.get('id')} filename={f.get('filename')}")

        print(f'\nVideo files ({len(video_files)}):')
        for f in video_files[:5]:
            print(f"  id={f.get('id')} filename={f.get('filename')}")
    else:
        print('Response:', files_result[:500])
except Exception as e:
    print(f'Parse error: {e}')
    print('Response:', files_result[:500])
