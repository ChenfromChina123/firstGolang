"""
测试预览 API metadata：
1. Markdown 文件预览元数据
2. 视频文件预览元数据（验证包含 poster URL）
3. 视频海报生成（验证 ffmpeg 调用）
"""
import sys
sys.path.insert(0, r'C:\Users\Administrator\.config\ssh-mcp')
from ssh_tool import run
import json

# 测试 Markdown 预览
print('=== 1. Markdown 预览 metadata ===')
md_id = '9e132cd26f0b6825ff47e13b936ab06b'  # test/Web控制台.md
md_result = run(f"""curl -s -b /tmp/cookies_test.txt 'https://aistudy.icu/api/preview/{md_id}'""")
print(f'File ID: {md_id}')
print(f'Response: {md_result}')

# 测试视频预览 metadata（验证包含 poster URL）
print('\n=== 2. 视频预览 metadata（验证 poster URL）===')
video_id = '3e3d38e2199374ee76d343616789f52b'  # LCY算法入门培训-第1讲.mp4
video_result = run(f"""curl -s -b /tmp/cookies_test.txt 'https://aistudy.icu/api/preview/{video_id}'""")
print(f'File ID: {video_id}')
print(f'Response: {video_result}')

# 解析验证
try:
    meta = json.loads(video_result)
    print(f'\n解析结果:')
    print(f'  type: {meta.get("type")}')
    print(f'  supported: {meta.get("supported")}')
    print(f'  urls: {meta.get("urls")}')
    if 'poster' in meta.get('urls', {}):
        print(f'  ✅ poster URL 存在: {meta["urls"]["poster"]}')
    else:
        print(f'  ❌ poster URL 不存在')
except Exception as e:
    print(f'解析失败: {e}')

# 测试视频海报生成
print('\n=== 3. 视频海报生成 ===')
poster_result = run(f"""curl -s -o /tmp/poster_test.jpg -w "HTTP_CODE:%{{http_code}} SIZE:%{{size_download}}" -b /tmp/cookies_test.txt 'https://aistudy.icu/api/preview/{video_id}/poster'""")
print(f'Poster response: {poster_result}')

# 检查海报文件
print('\n=== 4. 检查海报文件 ===')
check_poster = run(f'ls -la /tmp/poster_test.jpg; file /tmp/poster_test.jpg; ls -la /opt/filesync/data/_posters/ 2>&1')
print(check_poster)

# 测试 mov 视频海报
print('\n=== 5. MOV 视频海报测试 ===')
mov_id = '42ae473e67127b11686dc3c4676c72ff'  # IMG_0427.mov
mov_poster = run(f"""curl -s -o /tmp/poster_mov.jpg -w "HTTP_CODE:%{{http_code}} SIZE:%{{size_download}}" -b /tmp/cookies_test.txt 'https://aistudy.icu/api/preview/{mov_id}/poster'""")
print(f'MOV poster response: {mov_poster}')
