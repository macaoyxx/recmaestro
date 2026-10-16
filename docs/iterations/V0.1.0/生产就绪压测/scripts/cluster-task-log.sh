#!/usr/bin/env bash
# 集群扩展场景：拉任务日志并过滤关键行（对账/续录/恢复/完成）
# 用法: cluster-task-log.sh <任务id>
set -euo pipefail
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
id=${1:?用法: cluster-task-log.sh <id>}
tok=$(bash "$S/login.sh")
curl -s -H "Authorization: Bearer $tok" "http://127.0.0.1:8080/api/tasks/$id/logs" | python3 -c "
import json,sys
d=json.load(sys.stdin)
rows=d.get('data') or []
print(f'--- 任务 $id 日志 {len(rows)} 条，关键行： ---')
for r in rows:
    m=r.get('message','')
    if any(k in m for k in ['对账','续录','恢复','完成','合并','认领','录制开始','recovering','宕机','产物']):
        print(f\"{r.get('created_at','')} [{r.get('source','')}/{r.get('stage','')}] {m}\")
"
