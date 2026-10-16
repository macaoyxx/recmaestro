#!/bin/bash
# 05-灾备恢复 · 阶段3A3a：观测灾备-4 中断恢复（running→recovering→原node_id续录）
set -u
BASE=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro
SC="$BASE/docs/iterations/V0.1.0/生产就绪压测/scripts"
API=http://127.0.0.1:8080
tok=$(bash "$SC/login.sh")
api() { curl -s -H "Authorization: Bearer $tok" "$@"; }
. "$SC/05-ids.env"

st=$(api $API/api/tasks/$ID4/progress | sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p')
echo "@@ 起始 status=$st ts=$(date '+%H:%M:%S')"

echo "@@ 等待转 recovering（recover_window=3m，检测tick=90s，心跳冻结于 13:49:48）..."
saw_rec=0; saw_run2=0
for i in $(seq 1 60); do
  s=$(api $API/api/tasks/$ID4/progress | sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p')
  w=$(api $API/api/tasks/$ID4/progress | sed -n 's/.*"worker_id":"\([^"]*\)".*/\1/p')
  if [ "$s" = "recovering" ] && [ $saw_rec -eq 0 ]; then saw_rec=1; echo "@@ → recovering ts=$(date '+%H:%M:%S') worker=$w"; fi
  if [ "$s" = "running" ] && [ $saw_rec -eq 1 ]; then saw_run2=1; echo "@@ → 恢复 running ts=$(date '+%H:%M:%S') worker=$w（续录节点）"; break; fi
  sleep 5
done
[ $saw_rec -eq 0 ] && echo "@@ WARN: 300s 内未见 recovering"
[ $saw_run2 -eq 0 ] && echo "@@ WARN: 未见恢复 running" || echo "@@ OK 续录中"

echo "@@ [scheduler recover 日志]"
docker logs recmaestro-scheduler 2>&1 | grep "$ID4" | grep -vE "GIN" | tail -6
echo "@@ [三 worker 中谁执行了恢复]"
for c in deploy-worker-1 deploy-worker-2 deploy-worker-3; do
  hit=$(docker logs "$c" 2>&1 | grep -E "恢复任务 $ID4" | tail -1)
  [ -n "$hit" ] && echo "$c: $hit"
done
echo "@@ [灾备-4 续录日志（任务日志）]"
api $API/api/tasks/$ID4/logs | sed 's/},{/}\n{/g' | grep -oE '"message":"[^"]*"' | grep -E "恢复|续录|录制中断|start_seq|recover" | tail -6
echo "@@ PHASE3_A3A_DONE ts=$(date '+%H:%M:%S')"
