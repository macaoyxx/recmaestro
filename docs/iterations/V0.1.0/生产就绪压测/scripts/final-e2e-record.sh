#!/usr/bin/env bash
# 终验端到端录制：【终验-1】约 120s 录制 → completed → 时长对账 → 下载 ffprobe 复核
set -u
BASE=http://127.0.0.1:8080
S='/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts'

tok=$(bash "$S/login.sh")
if [ -z "$tok" ]; then echo "[FAIL] 登录失败"; exit 1; fi
AUTH="Authorization: Bearer $tok"

end=$(date -d '+120 seconds' '+%Y-%m-%dT%H:%M:%S%:z')
echo "[create] end_time=$end"
r=$(curl -s -X POST $BASE/api/tasks -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"stream_url":"rtmp://recmaestro-srs-e2e:1935/live/e2e","name":"【终验-1】终验端到端录制","segment_seconds":30,"end_time":"'$end'"}')
echo "[create] 返回: $(echo "$r" | head -c 300)"
id=$(echo "$r" | grep -oP '"id":\s*"?\K[0-9]+' | head -1)
if [ -z "$id" ]; then echo "[FAIL] 未取到任务 id"; exit 1; fi
echo "[create] 任务 id=$id（注意雪花ID完整值：$id）"

for i in $(seq 1 60); do
  sleep 5
  st=$(curl -s $BASE/api/tasks/$id -H "$AUTH" | grep -oP '"status":\s*"\K[^"]+' | head -1)
  echo "[poll $((i*5))s] status=$st"
  case "$st" in
    completed|record_failed|merge_failed|cancelled) break;;
  esac
done

echo "=== 任务详情 ==="
curl -s $BASE/api/tasks/$id -H "$AUTH" | head -c 700; echo

echo "=== 任务链路日志（对账与分段行）==="
curl -s "$BASE/api/tasks/$id/logs" -H "$AUTH" | grep -oP '"message":\s*"\K[^"]*' | grep -E '产物时长|段|合并|恢复|完成' | head -20

echo "=== 下载产物 ==="
curl -s -o /tmp/final-e2e-out.mp4 -w 'download HTTP:%{http_code} size:%{size_download}\n' "$BASE/api/tasks/$id/download" -H "$AUTH"
ls -la /tmp/final-e2e-out.mp4

echo "=== ffprobe 复核产物时长 ==="
ffprobe -v error -show_entries format=duration,format_name -of default=noprint_wrappers=1 /tmp/final-e2e-out.mp4

echo "=== 收尾：任务终态 ==="
curl -s $BASE/api/tasks/$id -H "$AUTH" | grep -oP '"(status|duration|output_duration|segment_count)":\s*("?[^,}]*"?)' | head -8
