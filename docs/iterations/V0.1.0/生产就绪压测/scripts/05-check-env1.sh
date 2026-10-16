#!/bin/bash
# 05-灾备恢复 · 复核：环境-1 状态 + 任务名/状态严格对齐统计
set -u
SC=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
API=http://127.0.0.1:8080
tok=$(bash "$SC/login.sh")
echo "@@ 【环境-1】详情字段:"
curl -s -H "Authorization: Bearer $tok" $API/api/tasks/2110556939766206464 | sed 's/,"/,"\n/g' | grep -E '"(name|status|output_path|worker_id)"'
echo "@@ 名-状态对齐统计（每任务一行）:"
curl -s -H "Authorization: Bearer $tok" -X POST $API/api/tasks/list -H 'Content-Type: application/json' -d '{"page":1,"page_size":200}' \
  | sed 's/},/}\n/g' | grep -oE '"name":"[^"]*"|"status":"[a-z_]+"' | sed 's/"//g;s/name://;s/status://' \
  | awk 'NR%2==1{n=$0;next}{print n" | "$0}' | sort | uniq -c | sort -rn | head -10
echo "@@ cancelled 任务名单:"
curl -s -H "Authorization: Bearer $tok" -X POST $API/api/tasks/list -H 'Content-Type: application/json' -d '{"page":1,"page_size":200}' \
  | sed 's/},/}\n/g' | grep '"status":"cancelled"' | grep -oE '"name":"[^"]*"'
