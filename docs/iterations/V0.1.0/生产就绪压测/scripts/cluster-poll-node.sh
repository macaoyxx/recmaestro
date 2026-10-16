#!/usr/bin/env bash
# 集群扩展场景：轮询节点表中指定 IP 的 node_id 出现(vanish的反义)/消失
# 用法: cluster-poll-node.sh <IP> <appear|vanish> [超时秒=180] [间隔秒=2]
set -euo pipefail
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
ip=${1:?用法: cluster-poll-node.sh <IP> <appear|vanish> [timeout] [interval]}
mode=${2:?}
timeout=${3:-180}
iv=${4:-2}
tok=$(bash "$S/login.sh")
start=$(date +%s)
while :; do
  now=$(date +%s); el=$((now-start))
  if [ "$el" -gt "$timeout" ]; then echo "TIMEOUT after ${el}s (mode=$mode ip=$ip)"; exit 1; fi
  found=$(curl -s -H "Authorization: Bearer $tok" http://127.0.0.1:8080/api/nodes | python3 -c "
import json,sys
ns=json.load(sys.stdin).get('data') or []
m=[n for n in ns if '$ip' in (n.get('node_id') or '')]
print(m[0]['node_id']+','+m[0]['status'] if m else '')
" 2>/dev/null || echo ERR)
  if [ "$mode" = "vanish" ]; then
    [ -z "$found" ] && { echo "VANISHED after ${el}s"; exit 0; }
  else
    [ -n "$found" ] && { echo "APPEARED after ${el}s ($found)"; exit 0; }
  fi
  sleep "$iv"
done
