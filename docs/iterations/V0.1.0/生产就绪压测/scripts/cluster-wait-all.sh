#!/usr/bin/env bash
# 集群扩展场景：等 keyword 命中的任务全部到终态（无 running/queued/recovering），输出结果统计
# 用法: cluster-wait-all.sh <keyword> [超时秒=420]
set -euo pipefail
keyword=${1:?用法: cluster-wait-all.sh <keyword> [timeout]}
timeout=${2:-420}
python3 - "$keyword" "$timeout" <<'EOF'
import json,subprocess,urllib.request,time,sys,datetime
kw,timeout=sys.argv[1],int(sys.argv[2])
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
def tok():
    return subprocess.run(["bash",f"{S}/login.sh"],capture_output=True,text=True).stdout.strip()
t=tok()
def api(path, data=None):
    req=urllib.request.Request("http://127.0.0.1:8080"+path,
        data=json.dumps(data).encode() if data else None,
        headers={"Authorization":"Bearer "+t,"Content-Type":"application/json"},
        method="POST" if data else "GET")
    return json.load(urllib.request.urlopen(req,timeout=15))
start=time.time(); last=None
while True:
    el=int(time.time()-start)
    if el>timeout: print(f"TIMEOUT after {el}s"); sys.exit(1)
    tasks=api("/api/tasks/list",{"page":1,"page_size":100,"keyword":kw})["data"]
    active=[x for x in tasks if x["status"] in ("pending","queued","running","recovering")]
    stat={}
    for x in tasks: stat[x["status"]]=stat.get(x["status"],0)+1
    cur=json.dumps(stat,ensure_ascii=False)
    if cur!=last:
        print(f"{datetime.datetime.now().strftime('%T')} [+{el}s] {cur} active={len(active)}",flush=True); last=cur
    if not active: 
        print(f"ALL_DONE after {el}s")
        bad=[x for x in tasks if x["status"] not in ("completed",)]
        print(f"非 completed 数={len(bad)}")
        for x in bad: print("  ",x["id"],x["name"],x["status"],x.get("error_msg"))
        sys.exit(1 if bad else 0)
    time.sleep(10)
EOF
