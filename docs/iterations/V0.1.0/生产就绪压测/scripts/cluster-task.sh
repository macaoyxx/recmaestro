#!/usr/bin/env bash
# 集群扩展场景：查单个任务状态 / 一批任务的 worker_id 快照
# 用法: cluster-task.sh <任务id>...   （多个 id 时输出紧凑表，用于绑定断言）
set -euo pipefail
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
tok=$(bash "$S/login.sh")
for id in "$@"; do
  curl -s -H "Authorization: Bearer $tok" "http://127.0.0.1:8080/api/tasks/$id" | python3 -c "
import json,sys
d=json.load(sys.stdin); t=d.get('data') or {}
print(f\"$id | {t.get('name')} | status={t.get('status')} | worker_id={t.get('worker_id')} | end={t.get('end_time')} | error={t.get('error_msg') or '-'}\")
"
done
