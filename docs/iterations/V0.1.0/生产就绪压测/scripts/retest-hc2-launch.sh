#!/bin/bash
# retest-hc2-launch.sh — Issue 高并发-2 复测·启动：扩 worker=5（100 槽）→ 等注册 → 创建 100 任务风暴【压测R-N】。
# start 统一 now+90s，end 统一 start+180s（与原场景口径一致：end=start+3min）。
set -u
S=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
cd "$S"

echo "== 1) 扩容 worker=5 =="
cd /mnt/c/Users/macao/.zcode/workspace/live/recmaestro/deploy
docker compose up -d --scale worker=5 2>&1 | tail -5
cd "$S"

echo "== 2) 等待 5 节点注册 =="
bash cluster-wait-nodes.sh 5 120 || { echo "[hc2] 节点未到 5，中止"; exit 1; }

echo "== 3) 创建 100 任务风暴 =="
tok=$(bash login.sh)
[ -n "$tok" ] || { echo "[hc2] 登录失败"; exit 1; }
start=$(date -d '+90 seconds' +%Y-%m-%dT%H:%M:%S%:z)
end=$(date -d "$start +180 seconds" +%Y-%m-%dT%H:%M:%S%:z)
echo "start_time=$start  end_time=$end  (now=$(date '+%T'))"
./loadgen/loadgen.bin storm -n 100 -c 10 -prefix 压测R \
  -start "$start" -end "$end" -token "$tok" \
  -out "$S/retest-hc2-ids.txt"
echo "[hc2] 创建完成，ids 落盘 retest-hc2-ids.txt"
