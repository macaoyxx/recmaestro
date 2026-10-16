#!/usr/bin/env bash
# 集群扩展场景：终态恢复 worker=3 + 收场核验
set -euo pipefail
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
tok=$(bash "$S/login.sh")

echo "=== $(date '+%T') scale down 前本场景活跃任务 ==="
curl -s -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
  -d '{"page":1,"page_size":100,"keyword":"集群扩展"}' http://127.0.0.1:8080/api/tasks/list \
  | python3 -c 'import json,sys; ts=json.load(sys.stdin)["data"]; a=[t for t in ts if t["status"] in ("pending","queued","running","recovering")]; print("active=",len(a)); [print(" ",t["id"],t["name"],t["status"]) for t in a]'
if curl -s -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
  -d '{"page":1,"page_size":100,"keyword":"集群扩展"}' http://127.0.0.1:8080/api/tasks/list \
  | grep -qE '"status":"(pending|queued|running|recovering)"'; then
  echo "存在活跃任务，中止缩容！"; exit 1
fi

echo "=== 缩容 worker=3 ==="
cd /mnt/c/Users/macao/.zcode/workspace/live/recmaestro/deploy
docker compose up -d --scale worker=3 2>&1 | grep -E 'Stopping|Removed|Running' || true

echo "=== 容器/IP 映射 ==="
bash "$S/cluster-ip2worker.sh"

echo "=== 等节点表收敛到 3 ==="
bash "$S/cluster-wait-nodes.sh" 3 180 | tail -2

echo "=== compose ps ==="
docker compose ps --format '{{.Name}} | {{.Status}}'

echo "=== 收场核验 ==="
curl -s -o /dev/null -w 'healthz=%{http_code}\n' http://127.0.0.1:8080/healthz
curl -s -H "Authorization: Bearer $tok" http://127.0.0.1:8080/api/stats \
  | python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; print("nodes_total=",d["nodes_total"],"nodes_online=",d["nodes_online"],"queue_size=",d["queue_size"],"scheduled_size=",d["scheduled_size"])'
