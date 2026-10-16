#!/bin/bash
# retest-hc3-launch.sh — 补录2·高并发-3 复测·启动：扩 worker=5 → 等注册 → 记基线 → 起连接采样 → 创建 100 任务风暴【压测S-N】。
# start 统一 now+90s，end 统一 start+180s（与 R2 口径一致：统一停录造洪峰）。
set -u
S=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
cd "$S"

echo "== 0) 记录窗口起点（UTC，供 docker logs --since）=="
date -u '+%Y-%m-%dT%H:%M:%SZ' | tee retest-hc3-window.txt

echo "== 1) 扩容 worker=5 =="
cd /mnt/c/Users/macao/.zcode/workspace/live/recmaestro/deploy
docker compose up -d --scale worker=5 2>&1 | tail -5
cd "$S"

echo "== 2) 等待 5 节点注册 =="
bash cluster-wait-nodes.sh 5 120 || { echo "[hc3] 节点未到 5，中止"; exit 1; }

echo "== 3) MySQL 连接指标基线（容器 17:4x 重建后计数器归零，任何值都是本轮新增）=="
docker exec recmaestro-mysql sh -c 'exec mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e "SHOW STATUS LIKE \"Threads_connected\"; SHOW STATUS LIKE \"Max_used_connections\"; SHOW STATUS LIKE \"Connection_errors_max_connections\"; SHOW STATUS LIKE \"Aborted_connects\";"' 2>/dev/null | tee retest-hc3-baseline.txt

echo "== 4) 后台连接采样（900s，每 3s，覆盖创建+录制+停录洪峰+自愈窗）=="
setsid nohup bash "$S/retest-hc3-sample.sh" 900 "$S/retest-hc3-conn.tsv" >/dev/null 2>&1 &
echo "采样器已启动 → retest-hc3-conn.tsv"

echo "== 5) 创建 100 任务风暴【压测S-N】=="
tok=$(bash login.sh)
[ -n "$tok" ] || { echo "[hc3] 登录失败"; exit 1; }
start=$(date -d '+90 seconds' +%Y-%m-%dT%H:%M:%S%:z)
end=$(date -d "$start +180 seconds" +%Y-%m-%dT%H:%M:%S%:z)
echo "start_time=$start  end_time=$end  (now=$(date '+%T'))" | tee retest-hc3-timing.txt
./loadgen/loadgen.bin storm -n 100 -c 10 -prefix 压测S \
  -start "$start" -end "$end" -token "$tok" \
  -out "$S/retest-hc3-ids.txt"
echo "[hc3] 创建完成 $(wc -l < retest-hc3-ids.txt) 个，ids 落盘 retest-hc3-ids.txt"
