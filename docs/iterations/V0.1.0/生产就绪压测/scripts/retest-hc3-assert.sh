#!/bin/bash
# retest-hc3-assert.sh — 补录2·高并发-3 复测·断言：三表一致性（核心）+ 洪峰指标 + 窗口日志扫描。
# 用法: bash retest-hc3-assert.sh   （前置：retest-hc3-launch.sh 已跑完且任务全终态）
set -u
S=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
DB=live_recorder
WIN=$(cat "$S/retest-hc3-window.txt" 2>/dev/null || date -u '+%Y-%m-%dT%H:%M:%SZ')
q() {
  docker exec recmaestro-mysql sh -c "exec mysql --default-character-set=utf8mb4 -uroot -p\"\$MYSQL_ROOT_PASSWORD\" $DB -e \"$1\"" 2>/dev/null
}

echo "== 1) 风暴任务总量与状态分布（断言：100 个全 completed，无 running/record_failed）=="
q "SELECT COUNT(*) AS total FROM live_rec_tasks WHERE name LIKE '%压测S-%';"
q "SELECT status, COUNT(*) AS c FROM live_rec_tasks WHERE name LIKE '%压测S-%' GROUP BY status;"

echo
echo "== 2) 三表一致性（核心断言）：主表 completed + 文件表恰 1 条 output 记录 + 日志表有 completed =="
echo "-- 不一致任务明细（期望空集）--"
q "SELECT t.name, t.status, t.worker_id,
     (SELECT COUNT(*) FROM live_rec_task_files f WHERE f.task_id=t.id AND f.filename='output.mp4' AND f.type='output') AS out_rows,
     (SELECT COUNT(*) FROM live_rec_task_logs l WHERE l.task_id=t.id AND l.stage='completed') AS completed_logs
   FROM live_rec_tasks t WHERE t.name LIKE '%压测S-%'
     AND NOT (t.status='completed'
       AND (SELECT COUNT(*) FROM live_rec_task_files f WHERE f.task_id=t.id AND f.filename='output.mp4' AND f.type='output')=1
       AND (SELECT COUNT(*) FROM live_rec_task_logs l WHERE l.task_id=t.id AND l.stage='completed')>=1);"
echo "-- 一致性汇总 --"
q "SELECT
     COUNT(*) AS total,
     SUM(t.status='completed') AS main_completed,
     SUM((SELECT COUNT(*) FROM live_rec_task_files f WHERE f.task_id=t.id AND f.filename='output.mp4' AND f.type='output')=1) AS file_ok,
     SUM((SELECT COUNT(*) FROM live_rec_task_logs l WHERE l.task_id=t.id AND l.stage='completed')>=1) AS log_ok
   FROM live_rec_tasks t WHERE t.name LIKE '%压测S-%';"

echo
echo "== 3) 调度正确性回归（认领一次/无漂移/时长对账）=="
q "SELECT cl.c AS claimed_times, COUNT(*) AS tasks FROM (
     SELECT task_id, COUNT(*) c FROM live_rec_task_logs WHERE stage='claimed'
       AND task_id IN (SELECT id FROM live_rec_tasks WHERE name LIKE '%压测S-%')
     GROUP BY task_id) cl GROUP BY cl.c;"
q "SELECT COUNT(*) AS claim_drift FROM live_rec_task_logs l JOIN live_rec_tasks t ON t.id=l.task_id
   WHERE l.stage='claimed' AND t.name LIKE '%压测S-%' AND l.node_id <> t.worker_id;"
q "SELECT COUNT(DISTINCT task_id) AS reconciled FROM live_rec_task_logs
   WHERE message LIKE '%对账通过%' AND task_id IN (SELECT id FROM live_rec_tasks WHERE name LIKE '%压测S-%');"
q "SELECT worker_id, COUNT(*) AS c FROM live_rec_tasks WHERE name LIKE '%压测S-%' AND worker_id <> '' GROUP BY worker_id ORDER BY worker_id;"

echo
echo "== 4) 停录洪峰 MySQL 连接指标（mysql 容器本轮重建后计数器归零，全部为本轮值）=="
docker exec recmaestro-mysql sh -c 'exec mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e "SHOW STATUS LIKE \"Threads_connected\"; SHOW STATUS LIKE \"Max_used_connections\"; SHOW STATUS LIKE \"Connection_errors_max_connections\"; SHOW STATUS LIKE \"Aborted_connects\";"' 2>/dev/null
if [ -f "$S/retest-hc3-conn.tsv" ]; then
  echo "-- 采样峰值（retest-hc3-conn.tsv，列: 时间/Threads_connected/Max_used/Errors_max）--"
  awk -F'\t' 'NF>=4 {if($2+0>tx){tx=$2+0;txt=$1} if($3+0>mx){mx=$3+0;mxt=$1}} END{printf "Threads_connected 峰值=%d(@%s)  Max_used_connections 峰值=%d(@%s)\n", tx, txt, mx, mxt}' "$S/retest-hc3-conn.tsv"
  echo "-- 停录窗口(17:43:54~17:44:30)原始采样行 --"
  awk -F'\t' '$1>="17:43:54" && $1<="17:44:30"' "$S/retest-hc3-conn.tsv" | head -15
fi

echo
echo "== 5) 窗口日志扫描（起点 $WIN）=="
echo "-- scheduler GIN 5xx 计数 --"
docker logs recmaestro-scheduler --since "$WIN" 2>&1 | grep 'GIN' | grep -cE ' 5[0-9][0-9] ' || true
echo "-- 各 worker 容器：Too many connections / [error] / 自愈补写 计数 --"
for c in $(docker ps --format '{{.Names}}' | grep deploy-worker | sort); do
  tm=$(docker logs "$c" --since "$WIN" 2>&1 | grep -c 'Too many connections' || true)
  er=$(docker logs "$c" --since "$WIN" 2>&1 | grep -c '\[error\]' || true)
  hl=$(docker logs "$c" --since "$WIN" 2>&1 | grep -c '自愈补写' || true)
  echo "$c: too_many=$tm error=$er heal=$hl"
done
