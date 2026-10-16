#!/bin/bash
# retest-ro-launch.sh — 溢出语义验证·启动：缩回 worker=3（60 槽）→ 等注册 → 创建 80 任务【压测RO-N】（20 个超容量）。
# 期待：60 completed + 20 record_failed，全部恰好认领一次、不丢失不重复。
set -u
S=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
cd "$S"

echo "== 1) 缩回 worker=3 =="
cd /mnt/c/Users/macao/.zcode/workspace/live/recmaestro/deploy
docker compose up -d --scale worker=3 2>&1 | tail -4
cd "$S"

echo "== 2) 等待 3 节点 =="
bash cluster-wait-nodes.sh 3 120 || { echo "[ro] 节点未到 3，中止"; exit 1; }

echo "== 3) 创建 80 任务溢出批 =="
tok=$(bash login.sh)
[ -n "$tok" ] || { echo "[ro] 登录失败"; exit 1; }
start=$(date -d '+90 seconds' +%Y-%m-%dT%H:%M:%S%:z)
end=$(date -d "$start +180 seconds" +%Y-%m-%dT%H:%M:%S%:z)
echo "start_time=$start  end_time=$end  (now=$(date '+%T'))"
./loadgen/loadgen.bin storm -n 80 -c 10 -prefix 压测RO \
  -start "$start" -end "$end" -token "$tok" \
  -out "$S/retest-ro-ids.txt"
echo "[ro] 创建完成"
