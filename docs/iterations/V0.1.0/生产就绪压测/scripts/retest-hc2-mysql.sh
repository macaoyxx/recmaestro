#!/bin/bash
# retest-hc2-mysql.sh — 查 MySQL 16:28 窗口异常证据：连接峰值/错误日志/容器状态。
set -u
echo "== MySQL 连接指标（当前+历史峰值）=="
docker exec recmaestro-mysql sh -c 'exec mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e "SHOW STATUS LIKE \"Threads_connected\"; SHOW STATUS LIKE \"Max_used_connections\"; SHOW STATUS LIKE \"Aborted_connects\"; SHOW STATUS LIKE \"Connection_errors_max_connections\";"' 2>/dev/null

echo
echo "== mysql 容器状态（是否重启过）=="
docker inspect recmaestro-mysql --format 'StartedAt={{.State.StartedAt}} RestartCount={{.RestartCount}} OOMKilled={{.State.OOMKilled}}'

echo
echo "== mysql error log 16:2x 窗口（连接类错误）=="
docker exec recmaestro-mysql sh -c 'grep -iE "too many|refused|aborted|error" /var/log/mysql/error.log 2>/dev/null | tail -20' || echo "(error.log 路径不存在，试 docker logs)"

echo
echo "== docker logs recmaestro-mysql 16:26~16:32 窗口 =="
docker logs recmaestro-mysql --since "2026-10-15T08:26:00Z" --until "2026-10-15T08:32:30Z" 2>&1 | tail -30
