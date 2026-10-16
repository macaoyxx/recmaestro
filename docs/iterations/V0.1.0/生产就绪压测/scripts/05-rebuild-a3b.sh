#!/bin/bash
# 05-灾备恢复 · 阶段3A3b：灾备-4 完成态 + 全链路日志 + 对账 + 下载 ffprobe
set -u
BASE=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro
SC="$BASE/docs/iterations/V0.1.0/生产就绪压测/scripts"
API=http://127.0.0.1:8080
tok=$(bash "$SC/login.sh")
api() { curl -s -H "Authorization: Bearer $tok" "$@"; }
. "$SC/05-ids.env"

echo "@@ 当前 status=$(api $API/api/tasks/$ID4/progress | sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p') ts=$(date '+%H:%M:%S')"
st=""
for i in $(seq 1 90); do
  st=$(api $API/api/tasks/$ID4/progress | sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p')
  [ "$st" = "completed" ] && break
  [ "$st" = "merge_failed" -o "$st" = "record_failed" -o "$st" = "interrupted" ] && break
  sleep 5
done
echo "@@ 灾备4 终态=$st ts=$(date '+%H:%M:%S')"

echo "@@ ===== 灾备-4 全链路日志（时间线）====="
api $API/api/tasks/$ID4/logs | sed 's/},{/}\n{/g' | grep -oE '"created_at":"[^"]*"|"message":"[^"]*"' | paste -d' | ' - - | sed 's/"created_at"://;s/"message"://;s/"//g' | tail -20

echo "@@ ===== 下载产物 ffprobe 复核 ====="
api $API/api/tasks/$ID4/download -o /tmp/05-zb4.mp4
echo "-- 【灾备-4】产物: size=$(stat -c %s /tmp/05-zb4.mp4)B ffprobe=$(ffprobe -v error -show_entries format=duration -of csv=p=0 /tmp/05-zb4.mp4)s"
echo "-- 预期：down前 ≈61s（13:48:47→13:49:48）+ 续录 ≈442s（13:53:20→14:00:42）≈ 503s；空窗 ≈3m32s 为设计内损失"
echo "@@ [api/tasks/$ID4 摘要]"
api $API/api/tasks/$ID4 | head -c 600
echo ""
echo "@@ PHASE3_A3B_DONE ts=$(date '+%H:%M:%S')"
