#!/usr/bin/env bash
# 集群扩展场景：并发阶段 —— 批量建任务 + 等全部终态 + 分布/结果统计
# 用法: cluster-concurrency.sh <任务数> <录制秒数>
set -euo pipefail
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
n=${1:?用法: cluster-concurrency.sh <任务数> <录制秒数>}
dur=${2:?}
# KEYWORD 可用环境变量覆盖（默认不变），便于修复验证等场景与本场景数据区分
KEYWORD="${KEYWORD:-集群扩展-并发}"

echo "=== $(date '+%F %T') 批量创建 $n 个任务（各录 ${dur}s）==="
# 整批只登录一次并复用 token：逐任务登录会打满 /api/login 每 IP 每分钟 10 次限流（Issue 集群扩展-1）
export RM_TOKEN
RM_TOKEN=$(bash "$S/login.sh")
for i in $(seq 1 "$n"); do
  bash "$S/cluster-create.sh" "【${KEYWORD}-$i】" "$dur"
  sleep 1
done

echo "=== $(date '+%T') 等待全部认领（10s 后快照）==="
sleep 10
python3 - <<'EOF'
import json,subprocess,urllib.request,os
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
KEYWORD=os.environ.get("KEYWORD","集群扩展-并发")
tok=subprocess.run(["bash",f"{S}/login.sh"],capture_output=True,text=True).stdout.strip()
def api(path, data=None):
    req=urllib.request.Request("http://127.0.0.1:8080"+path,
        data=json.dumps(data).encode() if data else None,
        headers={"Authorization":"Bearer "+tok,"Content-Type":"application/json"},
        method="POST" if data else "GET")
    return json.load(urllib.request.urlopen(req,timeout=15))
tasks=api("/api/tasks/list",{"page":1,"page_size":100,"keyword":KEYWORD})["data"]
print(f"并发任务总数={len(tasks)}")
from collections import Counter
c=Counter()
for t in tasks:
    c[(t.get("worker_id"),t.get("status"))]+=1
    print(f"{t['id']} | {t['name']} | {t['status']} | {t.get('worker_id')}")
print("--- 节点 × 状态分布 ---")
for k,v in sorted(c.items(),key=lambda x:str(x[0])): print(k,v)
nodes=api("/api/nodes")["data"]
print("--- 节点负载 ---")
for n in nodes: print(n["node_id"],n["status"],"current_tasks=",n["current_tasks"])
EOF
