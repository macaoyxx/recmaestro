#!/usr/bin/env bash
# 终验端到端录制：创建【终验-1】(约2分钟) → 轮询 completed → 日志时长对账 → 下载 ffprobe 复核
# 用法：bash e2e-final.sh
set -u
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BASE=http://127.0.0.1:8080
DUR=120          # 录制窗口 120 秒（约 2 分钟）
SEG=30           # TS 分段 30s
POLL_TIMEOUT=600 # 轮询上限 10 分钟

tok=$(bash "$SCRIPT_DIR/login.sh")
[ -z "$tok" ] && { echo "FAIL: 登录失败"; exit 1; }
echo "[登录] OK"

# 起止时间：now+5s ~ now+5s+120s（给认领留 5 秒）
start=$(date -d "+5 seconds" '+%Y-%m-%dT%H:%M:%S+08:00')
end=$(date -d "+$((5+DUR)) seconds" '+%Y-%m-%dT%H:%M:%S+08:00')
body=$(python3 - "$start" "$end" "$SEG" <<'PY'
import sys, json
start, end, seg = sys.argv[1], sys.argv[2], int(sys.argv[3])
print(json.dumps({"name": "【终验-1】", "stream_url": "rtmp://recmaestro-srs-e2e:1935/live/e2e",
                  "start_time": start, "end_time": end, "segment_seconds": seg}, ensure_ascii=True))
PY
)
echo "[创建] 入参: $body"
r=$(curl -s -X POST "$BASE/api/tasks" -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d "$body")
echo "[创建] 响应: $r"
tid=$(echo "$r" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("data",{}).get("id",""))')
[ -z "$tid" ] && { echo "FAIL: 未取到任务 id"; exit 1; }
echo "[任务] id=$tid"

# 轮询状态
status=""; waited=0
while [ $waited -lt $POLL_TIMEOUT ]; do
  sleep 10; waited=$((waited+10))
  status=$(curl -s "$BASE/api/tasks/$tid" -H "Authorization: Bearer $tok" | python3 -c 'import sys,json;d=json.load(sys.stdin);t=d.get("data",{});print(t.get("status",""))' 2>/dev/null)
  echo "[轮询 ${waited}s] status=$status"
  case "$status" in
    completed|record_failed|cancelled|merged) break ;;
  esac
done
echo "[终态] status=$status （等待 ${waited}s）"
[ "$status" != "completed" ] && { echo "FAIL: 终态非 completed"; exit 1; }

# 任务详情（对账证据）
curl -s "$BASE/api/tasks/$tid" -H "Authorization: Bearer $tok" | python3 -c 'import sys,json;d=json.load(sys.stdin)["data"];print("[详情]", {k:d.get(k) for k in ("id","name","status","worker_id","output_path","actual_start_time","actual_end_time")})'

# 日志中找时长对账文案
logs=$(curl -s "$BASE/api/tasks/$tid/logs" -H "Authorization: Bearer $tok" -G --data-urlencode "page=1" --data-urlencode "page_size=200")
echo "$logs" | python3 -c '
import sys, json
d = json.load(sys.stdin)
items = d.get("data", [])
if isinstance(items, dict): items = items.get("list", [])
hits = [l for l in items if "对账" in (l.get("message") or "")]
for l in hits: print("[对账日志]", l.get("message"))
if not hits: print("[对账日志] 未在前200条找到，共", len(items), "条")'

# 下载 MP4 并 ffprobe 复核（同会话完成）
dl="/tmp/final-e2e-$tid.mp4"
code=$(curl -s -o "$dl" -w '%{http_code}' "$BASE/api/tasks/$tid/download" -H "Authorization: Bearer $tok")
echo "[下载] http=$code, file=$dl, size=$(stat -c %s "$dl" 2>/dev/null)"
if [ "$code" = "200" ] && [ -s "$dl" ]; then
  echo "[ffprobe] $(ffprobe -v error -show_entries format=duration -of default=noprint_wrappers=1 "$dl")"
  echo "[ffprobe streams] $(ffprobe -v error -show_entries stream=codec_type,codec_name -of csv=p=0 "$dl" | tr '\n' ' ')"
else
  echo "FAIL: 下载失败"
  exit 1
fi
echo "=== 端到端脚本执行完毕 ==="
