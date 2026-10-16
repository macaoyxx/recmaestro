#!/bin/bash
# retest-hc3-sample.sh — MySQL 连接指标采样器（停录洪峰观察用，后台 nohup 跑）。
# 用法: nohup bash retest-hc3-sample.sh <持续秒数> <输出tsv> &
# 每 3s 记录: 本地时间 Threads_connected Max_used_connections Connection_errors_max_connections
set -u
dur=${1:-600}
out=${2:?输出文件}
: > "$out"
end=$(( $(date +%s) + dur ))
while [ "$(date +%s)" -lt "$end" ]; do
  v=$(docker exec recmaestro-mysql sh -c 'exec mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -N -e "SHOW STATUS LIKE \"Threads_connected\"; SHOW STATUS LIKE \"Max_used_connections\"; SHOW STATUS LIKE \"Connection_errors_max_connections\";"' 2>/dev/null | awk '{print $2}' | tr '\n' '\t')
  echo "$(date '+%T')	${v}" >> "$out"
  sleep 3
done
echo "$(date '+%T')	SAMPLE_END" >> "$out"
