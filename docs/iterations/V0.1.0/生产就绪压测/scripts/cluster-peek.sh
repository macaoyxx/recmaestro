#!/usr/bin/env bash
# 集群扩展场景：节点与任务快照（幂等，只读）
set -euo pipefail
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
API="http://127.0.0.1:8080"
tok=$(bash "$S/login.sh")

echo "=== $(date '+%F %T') 节点列表 GET /api/nodes ==="
curl -s -H "Authorization: Bearer $tok" "$API/api/nodes" | python3 -c "
import json,sys
d=json.load(sys.stdin)
ns=d.get('data') or []
print('node_id | ip | status | current_tasks | capacity | last_heartbeat_at')
for n in ns:
    print(f\"{n.get('node_id')} | {n.get('ip')} | {n.get('status')} | {n.get('current_tasks')} | {n.get('capacity')} | {n.get('last_heartbeat_at')}\")
print('nodes_total=',len(ns))
"
echo "=== 任务列表 POST /api/tasks/list (page_size=50) ==="
curl -s -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
  -d '{"page":1,"page_size":50}' "$API/api/tasks/list" | python3 -c "
import json,sys
d=json.load(sys.stdin)
rows=d.get('data') or []
print('id | name | status | worker_id | start | end')
for t in rows:
    print(f\"{t.get('id')} | {t.get('name')} | {t.get('status')} | {t.get('worker_id')} | {t.get('start_time')} | {t.get('end_time')}\")
print('total=',d.get('total'))
"
