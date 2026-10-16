#!/usr/bin/env bash
# e2e-record.sh —— 【单机基线-1】SRS 源真实录制端到端
# 创建（end_time≈3 分钟后）→ 轮询至 completed → 时长对账（任务日志文案 + 下载 ffprobe 复核）
# → 文件列表 / 任务日志下载 / ffmpeg 留档（record、merge）下载
set -u
S_DIR=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
API=http://127.0.0.1:8080
PASS=0; FAIL=0
T0=$(date +%s)
ts() { printf '[%5ds]' $(( $(date +%s) - T0 )); }
ok()  { PASS=$((PASS+1)); echo "$(ts) PASS $1"; }
bad() { FAIL=$((FAIL+1)); echo "$(ts) FAIL $1 | $2"; }
pj() { python3 -c '
import sys, json
raw = sys.stdin.read()
try:
    d = json.loads(raw)
    v = eval(sys.argv[1])
    print("None" if v is None else v)
except Exception:
    print("PYERR")
' "$1"; }
req() { local m=$1 p=$2; shift 2; curl -s --max-time 60 -X "$m" "$API$p" -w '\n%{http_code}' "$@"; }
split() { B=$(printf '%s' "$1" | sed '$d'); H=$(printf '%s' "$1" | tail -1); }

tok=$(bash "$S_DIR/login.sh") || { echo "登录失败"; exit 1; }
AUTH=(-H "Authorization: Bearer $tok")

# 1. 创建任务：立即执行，end_time = now + 190s（约 3 分钟）
ET=$(date -d '+190 seconds' +%Y-%m-%dT%H:%M:%S%:z)
split "$(req POST /api/tasks "${AUTH[@]}" -H 'Content-Type: application/json' \
  -d "{\"name\":\"【单机基线-1】\",\"stream_url\":\"rtmp://recmaestro-srs-e2e:1935/live/e2e\",\"end_time\":\"$ET\"}")"
TID=$(printf '%s' "$B" | pj 'd["data"]["id"]'); s=$(printf '%s' "$B" | pj 'd["data"]["status"]')
{ [ "$H" = 201 ] && [ "$s" = "queued" ]; } \
  && ok "创建【单机基线-1】201 立即任务 status=queued（id=$TID，end_time=$ET）" \
  || bad "创建任务" "http=$H status=$s body=$(printf '%s' "$B" | head -c 200)"

# 2. 轮询至终态（completed），最多 15 分钟
last=""
for i in $(seq 1 90); do
  split "$(req GET "/api/tasks/$TID" "${AUTH[@]}")"
  s=$(printf '%s' "$B" | pj 'd["data"]["status"]'); w=$(printf '%s' "$B" | pj 'd["data"]["worker_id"]')
  [ "$s" != "$last" ] && { echo "$(ts) 状态变迁: $s（worker=$w）"; last=$s; }
  [ "$s" = "completed" ] && break
  [ "$s" = "record_failed" ] || [ "$s" = "failed" ] || [ "$s" = "aborted" ] && { bad "轮询" "任务进入失败终态 $s"; break; }
  sleep 10
done
if [ "$s" = "completed" ]; then ok "任务 reached completed（status=$s, worker=$w）"; else bad "轮询终态" "最终 status=$s"; fi

# 3. 时长对账：下载任务日志全文，提取对账文案
split "$(req GET "/api/tasks/$TID/logs/download" "${AUTH[@]}")"
printf '%s' "$B" > /tmp/baseline1-task.log
LINE=$(grep -o '产物时长 [0-9.]*s（段合计 [0-9.]*s，对账通过）' /tmp/baseline1-task.log | tail -1)
RECON=$(printf '%s' "$LINE" | grep -o '[0-9.]*' | head -1)
[ "$H" = 200 ] && [ -n "$LINE" ] \
  && ok "任务日志下载 200，对账文案：『$LINE』" \
  || bad "时长对账文案" "http=$H line=$LINE（日志尾部：$(tail -5 /tmp/baseline1-task.log | tr '\n' ' ' | head -c 300)）"
SEGS=$(grep -c '段文件' /tmp/baseline1-task.log || true)
echo "$(ts) INFO 任务日志行数=$(wc -l < /tmp/baseline1-task.log) 段相关日志行=$SEGS"

# 4. 文件列表
split "$(req GET "/api/tasks/$TID/files" "${AUTH[@]}")"
files=$(printf '%s' "$B" | pj '[(x["type"], x["filename"], x["size_bytes"]) for x in d["data"]]')
cnt=$(printf '%s' "$B" | pj 'len(d["data"])')
mp4=$(printf '%s' "$B" | pj 'sum(1 for x in d["data"] if x["type"]=="output")')
rch=$(printf '%s' "$B" | pj 'sum(1 for x in d["data"] if x["reachable"])')
{ [ "$H" = 200 ] && [ "$mp4" -ge 1 ] && [ "$rch" = "$cnt" ]; } \
  && ok "文件列表 200 共 $cnt 项全部 reachable，含 output mp4（$files）" \
  || bad "文件列表" "http=$H cnt=$cnt mp4=$mp4 reachable=$rch files=$files"

# 5. 下载产物 → ffprobe 时长复核
curl -s --max-time 120 -o /tmp/baseline1.mp4 -w '%{http_code} %{size_download}' \
  -H "Authorization: Bearer $tok" "$API/api/tasks/$TID/download" > /tmp/dl1.txt
dl=$(cat /tmp/dl1.txt)
dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 /tmp/baseline1.mp4 2>/dev/null)
if [ -n "$RECON" ] && [ -n "$dur" ]; then
  diff=$(python3 -c "print(abs($dur - $RECON))")
  python3 -c "import sys; sys.exit(0 if abs($dur - $RECON) <= 1.0 else 1)" \
    && ok "产物下载 $dl 字节；ffprobe 时长 ${dur}s vs 对账 ${RECON}s，差 ${diff}s（≤1s 一致）" \
    || bad "ffprobe 对账" "dur=$dur recon=$RECON diff=$diff dl=$dl"
else
  bad "ffprobe 对账" "dur=$dur recon=$RECON（下载信息 $dl）"
fi

# 6. ffmpeg 留档下载（record / merge）
for ty in record merge; do
  code=$(curl -s --max-time 60 -o /tmp/baseline1-ffmpeg-$ty.log -w '%{http_code} %{size_download}' \
    -H "Authorization: Bearer $tok" "$API/api/tasks/$TID/ffmpeg-log/$ty")
  h=$(printf '%s' "$code" | awk '{print $1}'); sz=$(printf '%s' "$code" | awk '{print $2}')
  [ "$h" = 200 ] && [ "$sz" -gt 0 ] \
    && ok "ffmpeg-$ty 留档下载 200（$sz 字节，首行：$(head -c 120 /tmp/baseline1-ffmpeg-$ty.log | tr '\n' ' ')）" \
    || bad "ffmpeg-$ty 留档" "http=$h size=$sz"
done

echo "===== 汇总：PASS=$PASS FAIL=$FAIL ====="
[ "$FAIL" = 0 ] && echo "E2E-RECORD: ALL PASS" || echo "E2E-RECORD: HAS FAILURES"
