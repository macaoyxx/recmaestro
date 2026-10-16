#!/usr/bin/env bash
# 终验-1：拉取任务全量链路日志，验证「产物时长 Xs（段合计 Ys，对账通过）」对账行
set -u
BASE=http://127.0.0.1:8080
S='/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts'
ID="${1:-2110672925836185600}"

tok=$(bash "$S/login.sh")
if [ -z "$tok" ]; then echo "[FAIL] 登录失败"; exit 1; fi
AUTH="Authorization: Bearer $tok"

curl -s "$BASE/api/tasks/$ID/logs" -H "$AUTH" > /tmp/fv-log.json
echo "日志响应字节数: $(wc -c < /tmp/fv-log.json)"

python3 - "$ID" <<'PYEOF'
import json, sys
tid = sys.argv[1]
d = json.load(open('/tmp/fv-log.json'))
data = d.get('data')
if isinstance(data, list):
    items = data
elif isinstance(data, dict):
    items = data.get('list') or data.get('items') or data.get('logs') or []
else:
    items = []
print(f'任务 {tid} 日志条数: {len(items)}')
for it in items:
    print(f"  [{it.get('stage','')}] {it.get('message','')}")
PYEOF
