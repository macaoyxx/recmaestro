#!/bin/bash
# 05-灾备恢复 · 阶段1收尾：灾备1/2 完成态 + 时长对账 + 下载 ffprobe 复核 + 派发日志取证
set -u
BASE=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro
SC="$BASE/docs/iterations/V0.1.0/生产就绪压测/scripts"
API=http://127.0.0.1:8080
. "$SC/05-ids.env"
tok=$(bash "$SC/login.sh")
api() { curl -s -H "Authorization: Bearer $tok" "$@"; }
task_field() { api "$API/api/tasks/$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" | head -1; }

for id in "$ID1" "$ID2"; do
  st=""
  for i in $(seq 1 90); do st=$(task_field "$id" status); [ "$st" = "completed" ] && break; sleep 2; done
  echo "@@ 任务 $id status=$st worker=$(task_field "$id" worker_id)"
done

echo "@@ ===== 关键日志（对账/隔离/合并/派发）====="
for id in "$ID1" "$ID2"; do
  echo "--- 任务 $id 日志 ---"
  api "$API/api/tasks/$id/logs" | sed 's/},{/}\n{/g' | grep -oE '"message":"[^"]*"' | grep -E "对账|隔离|合并|开始录制|恢复|到点|认领|完成" | tail -12
done

echo "@@ ===== 下载产物 ffprobe 复核 ====="
api "$API/api/tasks/$ID1/download" -o /tmp/05-zb1.mp4
api "$API/api/tasks/$ID2/download" -o /tmp/05-zb2.mp4
for f in /tmp/05-zb1.mp4 /tmp/05-zb2.mp4; do
  sz=$(stat -c %s "$f" 2>/dev/null || echo 0)
  d=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$f" 2>/dev/null)
  echo "@@ $f size=${sz}B ffprobe_duration=${d}s"
done

echo "@@ ===== scheduler 派发日志取证（不限时间窗）====="
docker logs recmaestro-scheduler 2>&1 | grep -E "到点调度|队列重建" | grep -E "$ID1|$ID2" | tail -5
echo "@@ [scheduler 队列重建日志（全程）]"
docker logs recmaestro-scheduler 2>&1 | grep "队列重建完成" | tail -3
echo "@@ PHASE1_B_DONE ts=$(date '+%H:%M:%S')"
