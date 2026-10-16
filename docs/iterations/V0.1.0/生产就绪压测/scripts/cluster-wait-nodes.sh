#!/usr/bin/env bash
# 集群扩展场景：轮询节点总数直到等于期望值
# 用法: cluster-wait-nodes.sh <期望数> [超时秒=180]
set -euo pipefail
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
want=${1:?用法: cluster-wait-nodes.sh <N> [timeout]}
timeout=${2:-180}
tok=$(bash "$S/login.sh")
start=$(date +%s)
while :; do
  now=$(date +%s); el=$((now-start))
  if [ "$el" -gt "$timeout" ]; then echo "TIMEOUT after ${el}s (want=$want)"; exit 1; fi
  total=$(curl -s -H "Authorization: Bearer $tok" http://127.0.0.1:8080/api/nodes | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('data') or []))" 2>/dev/null || echo ERR)
  echo "$(date '+%T') nodes_total=$total (want=$want, ${el}s)"
  [ "$total" = "$want" ] && { echo "REACHED_N=$want after ${el}s"; exit 0; }
  sleep 5
done
