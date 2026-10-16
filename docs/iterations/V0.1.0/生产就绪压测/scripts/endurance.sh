#!/bin/bash
# endurance.sh — 混合耐力采样：每 30s 记录 docker stats（CPU/内存）+ WSL 负载，
# 与 loadgen mixed 并行跑，输出追加到日志文件（报告用）。
# 用法: endurance.sh <采样轮数> <日志路径>
set -u
rounds=${1:-11}
out=${2:-/tmp/endurance.log}

for i in $(seq 1 "$rounds"); do
  {
    echo "== 采样 #$i $(date '+%H:%M:%S') =="
    docker stats --no-stream --format '{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.PIDs}}' \
      | grep -E 'recmaestro-scheduler|recmaestro-mysql|recmaestro-redis|deploy-worker|srs'
    echo "-- load: $(uptime | awk -F'load average:' '{print $2}')"
  } >> "$out"
  [ "$i" -lt "$rounds" ] && sleep 30
done
echo "endurance done: $rounds rounds -> $out"
