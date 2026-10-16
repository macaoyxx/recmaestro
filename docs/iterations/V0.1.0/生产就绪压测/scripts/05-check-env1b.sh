#!/bin/bash
SC=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
API=http://127.0.0.1:8080
tok=$(bash "$SC/login.sh")
echo "@@ raw GET /api/tasks/2110556939766206464（前500字符）:"
curl -s -H "Authorization: Bearer $tok" $API/api/tasks/2110556939766206464 | head -c 500
echo ""
echo "@@ 【环境-1】下载头（验证文件可下载仍 OK）:"
curl -s -o /dev/null -w 'http=%{http_code} size=%{size_download}\n' -H "Authorization: Bearer $tok" $API/api/tasks/2110556939766206464/download
