#!/bin/bash
# 05-灾备恢复 · 阶段4：终态核验（全栈 healthy + worker×3 + 历史测试数据可见 + 推流在）
set -u
BASE=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro
SC="$BASE/docs/iterations/V0.1.0/生产就绪压测/scripts"
API=http://127.0.0.1:8080
tok=$(bash "$SC/login.sh")
api() { curl -s -H "Authorization: Bearer $tok" "$@"; }

echo "@@ [compose ps]"
docker ps --format '{{.Names}}\t{{.Status}}' | grep -E 'recmaestro|deploy-worker' | sort
echo "@@ [healthz] $(curl -s -o /dev/null -w '%{http_code}' $API/healthz)"
echo "@@ [stats] $(api $API/api/stats)"
echo "@@ [推流探测]"
timeout 20 ffprobe -v error -show_entries stream=codec_type -of csv=p=0 rtmp://127.0.0.1:21935/live/e2e 2>/dev/null | sort | tr '\n' ' '; echo ""
echo "@@ [历史测试数据可见性：全部【场景】前缀任务]"
api -X POST $API/api/tasks/list -H 'Content-Type: application/json' -d '{"page":1,"page_size":200}' \
  | sed 's/},/}\n/g' | grep -oE '"name":"【[^】]*】[^"]*"|"status":"[a-z_]+"' | paste -d' ' - - | sed 's/"name"://;s/"status"://;s/"//g'
echo "@@ [任务总数] $(api -X POST $API/api/tasks/list -H 'Content-Type: application/json' -d '{"page":1,"page_size":200}' | grep -o '"id":"' | wc -l)"
echo "@@ FINAL_DONE ts=$(date '+%H:%M:%S')"
