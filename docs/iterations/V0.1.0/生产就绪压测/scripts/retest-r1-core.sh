#!/usr/bin/env bash
# 复测 R1 — 单机基线核心断言回归（worker=1 缩容状态下）：
#   A. API 抽测：/me、无 token 401 信封、任务列表 POST 分页形状、stats 五键
#   B. SPA 抽查：/ index.html + 入口 chunk + 2 个懒加载 chunk
#   C. 录制端到端【单机基线-R1-1】：时长对账（日志文案 + ffprobe 复核）+ 文件列表/日志下载/留档下载
#   D. 坏源【单机基线-R1-2】：重试上限 MaxRetry=3 + 终态不排期（30s 稳定 + Redis scheduled 无任务）
set -u
S_DIR=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
DEPLOY=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/deploy
DIST=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/internal/web/dist
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
req() { local m=$1 p=$2; shift 2; curl -s --max-time 90 -X "$m" "$API$p" -w '\n%{http_code}' "$@"; }
split() { B=$(printf '%s' "$1" | sed '$d'); H=$(printf '%s' "$1" | tail -1); }

tok=$(bash "$S_DIR/login.sh") || { echo "登录失败"; exit 1; }
AUTH=(-H "Authorization: Bearer $tok" -H 'Content-Type: application/json')

echo "===== A. API 抽测 ====="
split "$(req GET /api/me "${AUTH[@]}")"
u=$(printf '%s' "$B" | pj 'd["data"]["username"]'); r=$(printf '%s' "$B" | pj 'd["data"]["role"]'); c=$(printf '%s' "$B" | pj 'd["code"]')
{ [ "$H" = 200 ] && [ "$c" = 0 ] && [ "$u" = admin ] && [ "$r" = admin ]; } && ok "/me 200 信封 code=0 username=admin role=admin" || bad "/me" "http=$H code=$c u=$u r=$r"

split "$(req POST /api/tasks/list -H 'Content-Type: application/json' -d '{"page":1,"page_size":1}')"
c=$(printf '%s' "$B" | pj 'd["code"]'); m=$(printf '%s' "$B" | pj 'd["msg"]'); t1=$(printf '%s' "$B" | pj 'd["total"]'); a=$(printf '%s' "$B" | pj 'd["data"][0]["id"]')
split "$(req POST /api/tasks/list -H 'Content-Type: application/json' -d '{"page":2,"page_size":1}')"
b=$(printf '%s' "$B" | pj 'd["data"][0]["id"]'); t2=$(printf '%s' "$B" | pj 'd["total"]'); l2=$(printf '%s' "$B" | pj 'len(d["data"])')
{ [ "$c" = 0 ] && [ "$m" = ok ] && [ "$l2" = 1 ] && [ "$a" != "$b" ] && [ -n "$a" ] && [ "$t1" = "$t2" ]; } \
  && ok "任务列表分页：msg=ok、page_size=1 两页 id 不同（$a / $b）、total=$t1 两页一致" \
  || bad "任务列表分页" "code=$c msg=$m ln=$l2 a=$a b=$b t1=$t1 t2=$t2"

split "$(req GET /api/tasks/list)"
mc=$(printf '%s' "$B" | pj 'd["code"]'); mm=$(printf '%s' "$B" | pj 'd["msg"]')
{ [ "$H" = 401 ] || { [ "$mc" != 0 ] && [ -n "$mm" ]; }; } && ok "无 token 401 失败信封（code=$mc msg=$mm）" || bad "无 token 401" "http=$H body=$(printf '%s' "$B" | head -c 150)"

split "$(req GET /api/stats "${AUTH[@]}")"
keys=$(printf '%s' "$B" | pj 'sorted(d["data"].keys())')
{ [ "$H" = 200 ] && [ "$(printf '%s' "$B" | pj 'len(d["data"])')" -ge 5 ]; } && ok "/stats 200 data 五键（$keys）" || bad "/stats" "http=$H keys=$keys"
st=$(printf '%s' "$B" | pj 'd["data"]["tasks_by_status"]')
echo "$(ts) INFO tasks_by_status=$st nodes_online=$(printf '%s' "$B" | pj 'd["data"]["nodes_online"]')"

echo "===== B. SPA 抽查 ====="
body=$(curl -s --max-time 30 "$API/")
entry=$(printf '%s' "$body" | grep -o 'assets/index-[^"]*\.js' | head -1)
printf '%s' "$body" | grep -q 'RecMaestro 直播录制调度平台' && ok "/ 200 index.html（含标题，引用 $entry）" || bad "/ index.html" "size=$(printf '%s' "$body" | wc -c)"
h=$(curl -s -o /dev/null -w '%{http_code}' "$API/$entry"); [ "$h" = 200 ] && ok "入口 chunk /$entry 200" || bad "入口 chunk" "http=$h"
n=0
for f in $(ls "$DIST/assets" | grep -E '^[A-Z][A-Za-z]*-.*\.js$' | head -2); do
  h=$(curl -s -o /dev/null -w '%{http_code}' "$API/assets/$f"); n=$((n+1))
  [ "$h" = 200 ] && ok "懒加载 chunk /assets/$f 200" || bad "懒加载 chunk $f" "http=$h"
done
[ "$n" = 2 ] || bad "懒加载 chunk 数量" "只找到 $n 个"

echo "===== C+D. 创建录制任务 + 坏源任务 ====="
ET=$(date -d '+190 seconds' +%Y-%m-%dT%H:%M:%S%:z)
split "$(req POST /api/tasks "${AUTH[@]}" -d "{\"name\":\"【单机基线-R1-1】\",\"stream_url\":\"rtmp://recmaestro-srs-e2e:1935/live/e2e\",\"end_time\":\"$ET\"}")"
TID=$(printf '%s' "$B" | pj 'd["data"]["id"]'); s=$(printf '%s' "$B" | pj 'd["data"]["status"]')
{ [ "$H" = 201 ] && [ "$s" = queued ]; } && ok "创建【单机基线-R1-1】201 status=queued（id=$TID）" || bad "创建录制任务" "http=$H s=$s body=$(printf '%s' "$B" | head -c 200)"

ET2=$(date -d '+20 minutes' +%Y-%m-%dT%H:%M:%S%:z)
split "$(req POST /api/tasks "${AUTH[@]}" -d "{\"name\":\"【单机基线-R1-2】\",\"stream_url\":\"rtmp://127.0.0.1:1/no-such\",\"end_time\":\"$ET2\"}")"
BID=$(printf '%s' "$B" | pj 'd["data"]["id"]'); s2=$(printf '%s' "$B" | pj 'd["data"]["status"]')
{ [ "$H" = 201 ] && [ "$s2" = queued ]; } && ok "创建【单机基线-R1-2】坏源任务 201 status=queued（id=$BID）" || bad "创建坏源任务" "http=$H s=$s2 body=$(printf '%s' "$B" | head -c 200)"

echo "===== 轮询两任务至终态（录制 completed / 坏源 record_failed）====="
lastR=""; lastB=""; s=""; bs=""; DONE_R=0; DONE_B=0; BFINAL_AT=0
for i in $(seq 1 90); do
  if [ "$DONE_R" = 0 ]; then
    split "$(req GET "/api/tasks/$TID" "${AUTH[@]}")"
    s=$(printf '%s' "$B" | pj 'd["data"]["status"]'); w=$(printf '%s' "$B" | pj 'd["data"]["worker_id"]')
    [ "$s" != "$lastR" ] && { echo "$(ts) 录制状态变迁: $s（worker=$w）"; lastR=$s; }
    [ "$s" = completed ] && DONE_R=1
    { [ "$s" = record_failed ] || [ "$s" = failed ] || [ "$s" = aborted ]; } && { bad "录制任务进入失败终态" "status=$s"; DONE_R=1; }
  fi
  if [ "$DONE_B" = 0 ]; then
    split "$(req GET "/api/tasks/$BID" "${AUTH[@]}")"
    bs=$(printf '%s' "$B" | pj 'd["data"]["status"]'); rc=$(printf '%s' "$B" | pj 'd["data"]["retry_count"]')
    [ "$bs" != "$lastB" ] && { echo "$(ts) 坏源状态变迁: $bs retry_count=$rc"; lastB=$bs; }
    [ "$bs" = record_failed ] && [ "$rc" -ge 3 ] 2>/dev/null && { DONE_B=1; BFINAL_AT=$(date +%s); }
  fi
  [ "$DONE_R" = 1 ] && [ "$DONE_B" = 1 ] && break
  sleep 10
done
[ "$s" = completed ] && ok "【单机基线-R1-1】reached completed（worker=$w）" || bad "录制终态" "status=$s"
[ "$bs" = record_failed ] && ok "【单机基线-R1-2】reached record_failed" || bad "坏源终态" "status=$bs"

echo "===== C. 录制端到端验证 ====="
split "$(req GET "/api/tasks/$TID/logs/download" "${AUTH[@]}")"
printf '%s' "$B" > /tmp/r1-task.log
LINE=$(grep -o '产物时长 [0-9.]*s（段合计 [0-9.]*s，对账通过）' /tmp/r1-task.log | tail -1)
RECON=$(printf '%s' "$LINE" | grep -o '[0-9.]*' | head -1)
[ "$H" = 200 ] && [ -n "$LINE" ] && ok "任务日志下载 200，对账文案：『$LINE』" || bad "时长对账文案" "http=$H line=$LINE（尾部：$(tail -3 /tmp/r1-task.log | tr '\n' ' ' | head -c 200)）"

split "$(req GET "/api/tasks/$TID/files" "${AUTH[@]}")"
files=$(printf '%s' "$B" | pj '[(x["type"], x["filename"], x["size_bytes"]) for x in d["data"]]')
cnt=$(printf '%s' "$B" | pj 'len(d["data"])')
rch=$(printf '%s' "$B" | pj 'sum(1 for x in d["data"] if x["reachable"])')
{ [ "$H" = 200 ] && [ "$rch" = "$cnt" ] && [ "$cnt" -ge 1 ]; } && ok "文件列表 200 共 $cnt 项全部 reachable（$files）" || bad "文件列表" "http=$H cnt=$cnt rch=$rch"

code=$(curl -s --max-time 120 -o /tmp/r1.mp4 -w '%{http_code} %{size_download}' -H "Authorization: Bearer $tok" "$API/api/tasks/$TID/download")
dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 /tmp/r1.mp4 2>/dev/null)
if [ -n "$RECON" ] && [ -n "$dur" ]; then
  diff=$(python3 -c "print(round(abs($dur - $RECON),3))")
  python3 -c "import sys; sys.exit(0 if abs($dur - $RECON) <= 1.0 else 1)" \
    && ok "产物下载 200（$code 字节）；ffprobe 时长 ${dur}s vs 对账 ${RECON}s，差 ${diff}s（≤1s 一致）" \
    || bad "ffprobe 对账" "dur=$dur recon=$RECON diff=$diff dl=$code"
else bad "ffprobe 对账" "dur=$dur recon=$RECON dl=$code"; fi

for ty in record merge; do
  code=$(curl -s --max-time 60 -o /tmp/r1-ffmpeg-$ty.log -w '%{http_code} %{size_download}' -H "Authorization: Bearer $tok" "$API/api/tasks/$TID/ffmpeg-log/$ty")
  h=$(printf '%s' "$code" | awk '{print $1}'); sz=$(printf '%s' "$code" | awk '{print $2}')
  [ "$h" = 200 ] && ok "ffmpeg-$ty 留档下载接口 200（$sz 字节，loglevel=warning 无告警时为 0 属设计行为）" || bad "ffmpeg-$ty 留档" "http=$h size=$sz"
done

echo "===== D. 坏源失败路径验证 ====="
split "$(req GET "/api/tasks/$BID" "${AUTH[@]}")"
rc=$(printf '%s' "$B" | pj 'd["data"]["retry_count"]'); ec=$(printf '%s' "$B" | pj 'd["data"]["error_code"]')
em=$(printf '%s' "$B" | pj 'd["data"]["error_msg"]')
[ "$rc" = 3 ] && ok "重试上限：retry_count=3=MaxRetry（error_code=$ec）" || bad "重试上限" "rc=$rc ec=$ec em=$em"
split "$(req GET "/api/tasks/$BID/logs/download" "${AUTH[@]}")"
printf '%s' "$B" > /tmp/r1-bad.log
grep -q '自动重试次数已用尽（3/3，终态失败）' /tmp/r1-bad.log \
  && ok "任务日志含『自动重试次数已用尽（3/3，终态失败）』" \
  || bad "重试日志" "日志尾部：$(tail -3 /tmp/r1-bad.log | tr '\n' ' ' | head -c 300)"
echo "$(ts) INFO error_msg=$em"

if [ "$BFINAL_AT" -gt 0 ]; then
  WAIT=$(( BFINAL_AT + 30 - $(date +%s) )); [ "$WAIT" -gt 0 ] && { echo "$(ts) INFO 终态后稳定观察至 30s（剩 ${WAIT}s）"; sleep "$WAIT"; }
fi
split "$(req GET "/api/tasks/$BID" "${AUTH[@]}")"
rc2=$(printf '%s' "$B" | pj 'd["data"]["retry_count"]'); bs2=$(printf '%s' "$B" | pj 'd["data"]["status"]')
{ [ "$bs2" = record_failed ] && [ "$rc2" = 3 ]; } && ok "终态 30s 后仍 record_failed / retry_count=3（不再自动排期）" || bad "终态稳定" "status=$bs2 rc=$rc2"

cd "$DEPLOY" && set -a && source .env >/dev/null 2>&1 && set +a
ZR=$(docker exec -e REDISCLI_AUTH="$REDIS_PASSWORD" recmaestro-redis redis-cli ZRANGE recmaestro:scheduled 0 -1)
ZC=$(docker exec -e REDISCLI_AUTH="$REDIS_PASSWORD" recmaestro-redis redis-cli ZCARD recmaestro:scheduled)
echo "$ZR" | grep -q "$BID" && bad "Redis scheduled 含终态坏源任务" "$ZR" || ok "Redis recmaestro:scheduled 无坏源任务（ZCARD=$ZC）"
echo "$ZR" | grep -q "$TID" && bad "Redis scheduled 含 completed 任务" "$ZR" || ok "Redis recmaestro:scheduled 无 completed 任务"

h=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $tok" "$API/api/tasks/$BID/ffmpeg-log/record")
[ "$h" = 404 ] && ok "坏源留档 404（ffprobe 预检防线挡在 ffmpeg 前，无录制目录，文案与行为一致）" || bad "坏源留档" "http=$h（预期 404）"

echo "===== 汇总：PASS=$PASS FAIL=$FAIL ====="
[ "$FAIL" = 0 ] && echo "R1-CORE: ALL PASS" || echo "R1-CORE: HAS FAILURES"
