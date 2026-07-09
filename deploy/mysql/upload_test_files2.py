"""上传 JSON 和 CSV 测试文件到服务器，再用 curl 上传到 filesync，测试预览 API"""
import sys
import os
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run, upload
import json

# 创建本地测试文件
TEST_DIR = r'd:\STUDY\GO\StudyGolang\firstGolang\filesync\test_local\preview_test'
os.makedirs(TEST_DIR, exist_ok=True)

# JSON 测试文件
json_content = json.dumps({
    "name": "FileSync",
    "version": "2.0",
    "features": ["chunked_upload", "resumable", "preview", "share"],
    "preview": {
        "image": ["jpg", "png", "gif"],
        "video": ["mp4", "webm", "mov"],
        "text": ["md", "json", "csv"]
    }
}, ensure_ascii=False, indent=2)

json_path = os.path.join(TEST_DIR, 'test.json')
with open(json_path, 'w', encoding='utf-8') as f:
    f.write(json_content)

# CSV 测试文件
csv_content = """name,age,city,occupation
张三,28,北京,工程师
李四,32,上海,设计师
王五,25,深圳,产品经理
赵六,35,广州,项目经理
"""

csv_path = os.path.join(TEST_DIR, 'test.csv')
with open(csv_path, 'w', encoding='utf-8') as f:
    f.write(csv_content)

# 先上传测试文件到服务器临时目录
print('=== 0. 上传测试文件到服务器临时目录 ===')
print(upload(json_path, '/tmp/test_preview.json'))
print(upload(csv_path, '/tmp/test_preview.csv'))

# 登录
print('\n=== 1. 登录 ===')
print(run("""curl -s -c /tmp/cookies_test.txt -X POST https://aistudy.icu/api/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"***REMOVED_PASSWORD***"}'"""))

# 上传 JSON 文件
print('\n=== 2. 上传 JSON 测试文件 ===')
json_size = os.path.getsize(json_path)
init_result = run(f"""curl -s -b /tmp/cookies_test.txt -X POST https://aistudy.icu/api/upload/init -H 'Content-Type: application/json' -d '{{"filename":"test_preview.json","file_size":{json_size},"chunk_size":1048576,"storage":"local"}}'""")
print(f'Init: {init_result}')
try:
    init_data = json.loads(init_result)
    session_id = init_data.get('session_id')
    print(f'Session: {session_id}')

    upload_result = run(f"""curl -s -b /tmp/cookies_test.txt -X POST https://aistudy.icu/api/upload/chunk -F 'session_id={session_id}' -F 'chunk_index=0' -F 'chunk_data=@/tmp/test_preview.json'""")
    print(f'Upload chunk: {upload_result}')

    complete_result = run(f"""curl -s -b /tmp/cookies_test.txt -X POST https://aistudy.icu/api/upload/complete -H 'Content-Type: application/json' -d '{{"session_id":"{session_id}"}}'""")
    print(f'Complete: {complete_result}')
    try:
        comp_data = json.loads(complete_result)
        json_file_id = comp_data.get('file_id')
        print(f'JSON File ID: {json_file_id}')
    except:
        json_file_id = None
except Exception as e:
    print(f'Error: {e}')
    json_file_id = None

# 上传 CSV 文件
print('\n=== 3. 上传 CSV 测试文件 ===')
csv_size = os.path.getsize(csv_path)
init_result2 = run(f"""curl -s -b /tmp/cookies_test.txt -X POST https://aistudy.icu/api/upload/init -H 'Content-Type: application/json' -d '{{"filename":"test_preview.csv","file_size":{csv_size},"chunk_size":1048576,"storage":"local"}}'""")
print(f'Init: {init_result2}')
try:
    init_data2 = json.loads(init_result2)
    session_id2 = init_data2.get('session_id')

    upload_result2 = run(f"""curl -s -b /tmp/cookies_test.txt -X POST https://aistudy.icu/api/upload/chunk -F 'session_id={session_id2}' -F 'chunk_index=0' -F 'chunk_data=@/tmp/test_preview.csv'""")
    print(f'Upload chunk: {upload_result2}')

    complete_result2 = run(f"""curl -s -b /tmp/cookies_test.txt -X POST https://aistudy.icu/api/upload/complete -H 'Content-Type: application/json' -d '{{"session_id":"{session_id2}"}}'""")
    print(f'Complete: {complete_result2}')
    try:
        comp_data2 = json.loads(complete_result2)
        csv_file_id = comp_data2.get('file_id')
        print(f'CSV File ID: {csv_file_id}')
    except:
        csv_file_id = None
except Exception as e:
    print(f'Error: {e}')
    csv_file_id = None

# 测试 JSON 预览
print('\n=== 4. 测试 JSON 预览 metadata ===')
if json_file_id:
    json_meta = run(f'curl -s -b /tmp/cookies_test.txt "https://aistudy.icu/api/preview/{json_file_id}"')
    print(f'JSON metadata: {json_meta}')

    print('\n=== 5. 测试 JSON 内容流 ===')
    json_content_resp = run(f'curl -s -b /tmp/cookies_test.txt "https://aistudy.icu/api/preview/{json_file_id}/content"')
    print(f'JSON content: {json_content_resp[:300]}')

# 测试 CSV 预览
print('\n=== 6. 测试 CSV 预览 metadata ===')
if csv_file_id:
    csv_meta = run(f'curl -s -b /tmp/cookies_test.txt "https://aistudy.icu/api/preview/{csv_file_id}"')
    print(f'CSV metadata: {csv_meta}')

    print('\n=== 7. 测试 CSV 内容流 ===')
    csv_content_resp = run(f'curl -s -b /tmp/cookies_test.txt "https://aistudy.icu/api/preview/{csv_file_id}/content"')
    print(f'CSV content: {csv_content_resp[:300]}')

# 清理临时文件
print('\n=== 8. 清理临时文件 ===')
print(run('rm -f /tmp/test_preview.json /tmp/test_preview.csv'))
