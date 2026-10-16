#!/bin/bash
# retest-hc2-poll.sh — 轮询风暴任务终态数（completed/record_failed 达到期望即退出）。
# 用法: retest-hc2-poll.sh <前缀> <期望终态总数> [超时秒=540]
set -u
pat=${1:?前缀}; want=${2:?期望数}; timeout=${3:-540}
DB=live_recorder
q() {
  docker exec recmaestro-mysql sh -c "exec mysql --default-character-set=utf8mb4 -uroot -p\"\$MYSQL_ROOT_PASSWORD\" $DB -N -e \"$1\"" 2>/dev/null
}
start=$(date +%s)
while :; do
  now=$(date +%s); el=$((now-start))
  [ "$el" -gt "$timeout" ] && { echo "TIMEOUT after ${el}s"; exit 1; }
  fin=$(q "SELECT COUNT(*) FROM live_rec_tasks WHERE name LIKE '%${pat}-%' AND status IN ('completed','record_failed');")
  nonfin=$(q "SELECT COUNT(*) FROM live_rec_tasks WHERE name LIKE '%${pat}-%' AND status NOT IN ('completed','record_failed');")
  echo "$(date '+%T') terminal=$fin nonterminal=$nonfin (want=$want, ${el}s)"
  [ "$fin" = "$want" ] && [ "$nonfin" = "0" ] && { echo "ALL_TERMINAL after ${el}s"; exit 0; }
  sleep 15
done
