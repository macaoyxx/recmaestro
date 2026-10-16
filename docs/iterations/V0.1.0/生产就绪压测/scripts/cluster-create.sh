#!/usr/bin/env bash
# 集群扩展场景：创建立即执行的录制任务
# 用法: cluster-create.sh <任务名> <录制秒数>  （end_time = now+秒，start_time 空立即执行）
# 可设 RM_TOKEN 复用已有 token：/api/login 有每 IP 每分钟 10 次限流（login_ratelimit.go），
# 批量建任务若逐次登录会在窗口内打满配额触发 429 → 空 token → 401（Issue 集群扩展-1）。
set -euo pipefail
name=${1:?用法: cluster-create.sh <任务名> <录制秒数>}
dur=${2:?用法: cluster-create.sh <任务名> <录制秒数>}
python3 - "$name" "$dur" <<'EOF'
import json,sys,urllib.request,urllib.error,datetime,os,subprocess
name,dur=sys.argv[1],int(sys.argv[2])
# 登录：优先复用 RM_TOKEN；需新登录时失败立即终止并透传原因（不再拿空 token 打创建接口造成误导性 401）
tok=os.environ.get("RM_TOKEN","").strip()
if not tok:
    p=subprocess.run(["bash","/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts/login.sh"],capture_output=True,text=True)
    tok=p.stdout.strip()
    if not tok:
        sys.stderr.write("cluster-create.sh：获取 token 失败，已终止（未发起创建请求）。login.sh 输出：\n"+p.stderr)
        sys.exit(2)
end=(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(seconds=dur)).astimezone().isoformat(timespec='seconds')
body=json.dumps({"name":name,"stream_url":"rtmp://recmaestro-srs-e2e:1935/live/e2e","end_time":end}).encode()
req=urllib.request.Request("http://127.0.0.1:8080/api/tasks",data=body,
    headers={"Content-Type":"application/json","Authorization":"Bearer "+tok},method="POST")
try:
    r=urllib.request.urlopen(req,timeout=15); print("HTTP",r.status,r.read().decode())
except urllib.error.HTTPError as e:
    print("HTTP",e.code,e.read().decode()); sys.exit(1)
EOF
