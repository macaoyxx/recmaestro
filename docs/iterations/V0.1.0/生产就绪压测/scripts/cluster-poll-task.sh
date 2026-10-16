#!/usr/bin/env bash
# 集群扩展场景：轮询任务状态直到到达目标状态（状态变化即打印）
# 用法: cluster-poll-task.sh <任务id> <目标状态> [超时秒=300]
set -euo pipefail
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
id=${1:?用法: cluster-poll-task.sh <id> <status> [timeout]}
want=${2:?}
timeout=${3:-300}
tok=$(bash "$S/login.sh")
start=$(date +%s)
last=""
while :; do
  now=$(date +%s); el=$((now-start))
  if [ "$el" -gt "$timeout" ]; then echo "TIMEOUT after ${el}s (last=$last want=$want)"; exit 1; fi
  cur=$(curl -s -H "Authorization: Bearer $tok" "http://127.0.0.1:8080/api/tasks/$id" | python3 -c "import json,sys; t=json.load(sys.stdin).get('data') or {}; print(t.get('status','?'))" 2>/dev/null || echo ERR)
  if [ "$cur" != "$last" ]; then echo "$(date '+%T') [+${el}s] status=$cur (worker见任务快照)"; last=$cur; fi
  [ "$cur" = "$want" ] && { echo "REACHED_$want after ${el}s"; exit 0; }
  sleep 5
done
