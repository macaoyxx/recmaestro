#!/bin/sh
# tcpstat.sh — 采样压测期间的关键状态（供 02-高并发压测复用）。
# 输出：scheduler 容器 netns 内 TCP 状态分布（/proc/net/tcp st 列，06=TIME_WAIT、01=ESTABLISHED），
#       MySQL 连接指标，docker stats 摘要，WSL top 摘要。
set -u

echo "== scheduler 容器 TCP 状态分布 (st: 01=ESTAB 06=TIME_WAIT) =="
docker exec recmaestro-scheduler sh -c "awk 'NR>1 {print \$4}' /proc/net/tcp" | sort | uniq -c | sort -rn

echo "== MySQL 连接指标 =="
docker exec recmaestro-mysql sh -c 'exec mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e "SHOW STATUS LIKE \"Threads_connected\"; SHOW STATUS LIKE \"Threads_running\";"' 2>/dev/null

echo "== docker stats =="
docker stats --no-stream --format 'table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}' | grep -E 'NAME|recmaestro-scheduler|recmaestro-mysql|recmaestro-redis|deploy-worker|srs'

echo "== WSL top（前 8 进程 + 汇总行） =="
top -bn1 | head -5 | tail -2
top -bn1 | sed -n '7,12p'
