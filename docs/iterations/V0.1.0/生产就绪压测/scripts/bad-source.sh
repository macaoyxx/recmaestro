#!/usr/bin/env bash
# bad-source.sh —— 【单机基线-2】坏源任务失败路径
# rtmp://127.0.0.1:1/no-such（连接拒绝）→ 断言：
#   1) 自动重试有上限：retry_count 达 MaxRetry=3 后终态 record_failed
#   2) 达终态后不再排期：等 75s（覆盖 2 个 backoff 周期）状态/计数不变 + Redis 定时队列无该任务
#   3) 坏源 ffmpeg stderr 留档非空（反向验证留档机制）
set -u
S_DIR=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
REPO=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro
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
AUTH=(-H "Authorization: Bearer $tok" -H 'Content-Type: application/json')

# 1. 创建坏源任务（end_time +20min 保持未来，走自动重试路径）
ET=$(date -d '+20 minutes' +%Y-%m-%dT%H:%M:%S%:z)
split "$(req POST /api/tasks "${AUTH[@]}" -d "{\"name\":\"【单机基线-2】\",\"stream_url\":\"rtmp://127.0.0.1:1/no-such\",\"end_time\":\"$ET\"}")"
TID=$(printf '%s' "$B" | pj 'd["data"]["id"]'); s=$(printf '%s' "$B" | pj 'd["data"]["status"]')
{ [ "$H" = 201 ] && [ "$s" = "queued" ]; } \
  && ok "创建【单机基线-2】坏源任务 201 status=queued（id=$TID）" \
  || bad "创建坏源任务" "http=$H status=$s body=$(printf '%s' "$B" | head -c 200)"

# 2. 轮询至终态 record_failed（8 分钟超时）
last=""
for i in $(seq 1 96); do
  split "$(req GET "/api/tasks/$TID" "${AUTH[@]}")"
  s=$(printf '%s' "$B" | pj 'd["data"]["status"]'); rc=$(printf '%s' "$B" | pj 'd["data"]["retry_count"]')
  [ "$s" != "$last" ] && { echo "$(ts) 状态变迁: $s retry_count=$rc"; last=$s; }
  [ "$s" = "record_failed" ] && break
  [ "$s" = "completed" ] && { bad "坏源任务竟然 completed" "不合预期"; break; }
  sleep 5
done
rc=$(printf '%s' "$B" | pj 'd["data"]["retry_count"]'); em=$(printf '%s' "$B" | pj 'd["data"]["error_msg"]')
{ [ "$s" = "record_failed" ] && [ "$rc" = 3 ]; } \
  && ok "终态 record_failed 且 retry_count=3（=MaxRetry，error_msg=${em:0:60}）" \
  || bad "终态" "status=$s retry_count=$rc error=$em"

# 3. 任务日志证据：重试排期 + 用尽文案
curl -s --max-time 30 -H "Authorization: Bearer $tok" "$API/api/tasks/$TID/logs/download" > /tmp/baseline2-task.log
retryLines=$(grep -c '后自动重试' /tmp/baseline2-task.log || true)
exhaust=$(grep -o '自动重试次数已用尽（3/3），终态失败' /tmp/baseline2-task.log | tail -1)
{ [ "$retryLines" -ge 2 ] && [ -n "$exhaust" ]; } \
  && ok "任务日志含 ${retryLines} 条『后自动重试』排期记录 + 『$exhaust』" \
  || bad "重试日志" "retryLines=$retryLines exhaust=$exhaust（日志全文：$(cat /tmp/baseline2-task.log | tr '\n' '|' | head -c 500)）"

# 4. 终态不排期：等 75s（>2×10s backoff），状态与 retry_count 稳定不变
sleep 75
split "$(req GET "/api/tasks/$TID" "${AUTH[@]}")"
s2=$(printf '%s' "$B" | pj 'd["data"]["status"]'); rc2=$(printf '%s' "$B" | pj 'd["data"]["retry_count"]')
{ [ "$s2" = "record_failed" ] && [ "$rc2" = 3 ]; } \
  && ok "终态 75s 后仍 record_failed / retry_count=3（不再自动排期）" \
  || bad "终态稳定性" "status=$s2 retry_count=$rc2"

# 5. Redis 定时队列硬证据：scheduled zset 中无该任务
zr=$(cd "$REPO/deploy" && { [ -f .env ] && . ./.env 2>/dev/null; } && \
  docker exec -e REDISCLI_AUTH="${REDIS_PASSWORD:-}" recmaestro-redis \
  redis-cli --no-auth-warning ZRANGE recmaestro:scheduled 0 -1 2>&1)
if printf '%s' "$zr" | grep -q "$TID"; then
  bad "Redis scheduled 队列" "终态任务仍在定时队列: $(printf '%s' "$zr" | tr '\n' ' ')"
else
  ok "Redis recmaestro:scheduled 中无终态任务（队列内容：$(printf '%s' "$zr" | tr '\n' ' ' | head -c 120)）"
fi

# 6. 坏源 ffmpeg-record 留档非空（失败场景反向验证留档机制）
code=$(curl -s --max-time 60 -o /tmp/baseline2-ffmpeg-record.log -w '%{http_code} %{size_download}' \
  -H "Authorization: Bearer $tok" "$API/api/tasks/$TID/ffmpeg-log/record")
h=$(printf '%s' "$code" | awk '{print $1}'); sz=$(printf '%s' "$code" | awk '{print $2}')
[ "$h" = 200 ] && [ "$sz" -gt 0 ] \
  && ok "坏源任务 ffmpeg-record 留档下载 200 非空（$sz 字节，首行：$(head -c 150 /tmp/baseline2-ffmpeg-record.log | tr '\n' ' ')）" \
  || bad "坏源留档" "http=$h size=$sz"

echo "===== 汇总：PASS=$PASS FAIL=$FAIL ====="
[ "$FAIL" = 0 ] && echo "BAD-SOURCE: ALL PASS" || echo "BAD-SOURCE: HAS FAILURES"
