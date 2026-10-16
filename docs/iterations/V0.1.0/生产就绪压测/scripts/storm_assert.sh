#!/bin/bash
# storm_assert.sh — 任务风暴断言 SQL（库名 live_recorder，密码经容器内环境变量，不回显）。
# 断言口径见 02-高并发压测.md 第 3 节。
set -u
DB=live_recorder
PAT='%压测-%'

q() {
  docker exec recmaestro-mysql sh -c "exec mysql --default-character-set=utf8mb4 -uroot -p\"\$MYSQL_ROOT_PASSWORD\" $DB -e \"$1\"" 2>/dev/null
}

echo "== 1) 风暴任务总量 =="
q "SELECT COUNT(*) AS total FROM live_rec_tasks WHERE name LIKE '$PAT';"

echo "== 2) 状态分布 =="
q "SELECT status, COUNT(*) AS c FROM live_rec_tasks WHERE name LIKE '$PAT' GROUP BY status;"

echo "== 3) 每任务 claimed 日志次数分布（1=仅初认领；>1=自动重试再认领） =="
q "SELECT cl.c AS claimed_times, COUNT(*) AS tasks FROM (
     SELECT task_id, COUNT(*) c FROM live_rec_task_logs
     WHERE stage='claimed'
       AND task_id IN (SELECT id FROM live_rec_tasks WHERE name LIKE '$PAT')
     GROUP BY task_id) cl GROUP BY cl.c;"

echo "== 4) claimed 日志 node_id 与任务最终 worker_id 不一致数（认领漂移） =="
q "SELECT COUNT(*) AS mismatch FROM live_rec_task_logs l
     JOIN live_rec_tasks t ON t.id = l.task_id
   WHERE l.stage='claimed' AND t.name LIKE '$PAT' AND l.node_id <> t.worker_id;"

echo "== 5) claimed 时刻重叠检查：同任务两条 claimed 且时间差 < 180s 且 node 不同（真重复认领） =="
q "SELECT COUNT(*) AS dup FROM live_rec_task_logs a
     JOIN live_rec_task_logs b ON a.task_id=b.task_id AND a.id < b.id
     AND a.node_id <> b.node_id
     AND TIMESTAMPDIFF(SECOND, a.ts, b.ts) BETWEEN 0 AND 179
   WHERE a.stage='claimed' AND b.stage='claimed'
     AND a.task_id IN (SELECT id FROM live_rec_tasks WHERE name LIKE '$PAT');"

echo "== 6) 时长对账通过数（worker.go:599 文案） =="
q "SELECT COUNT(DISTINCT task_id) AS reconciled FROM live_rec_task_logs
   WHERE message LIKE '%对账通过%'
     AND task_id IN (SELECT id FROM live_rec_tasks WHERE name LIKE '$PAT');"

echo "== 7) completed 产物时长明细（对账文案前 10 条） =="
q "SELECT t.name, t.status, t.worker_id, l.message FROM live_rec_tasks t
     JOIN live_rec_task_logs l ON l.task_id=t.id AND l.message LIKE '%对账通过%'
   WHERE t.name LIKE '$PAT' ORDER BY t.id LIMIT 10;"

echo "== 8) 非 completed 任务错误分布 =="
q "SELECT status, error_code, COUNT(*) AS c FROM live_rec_tasks
   WHERE name LIKE '$PAT' AND status <> 'completed' GROUP BY status, error_code;"

echo "== 9) 各 worker 认领数（负载均衡观察） =="
q "SELECT worker_id, COUNT(*) AS c FROM live_rec_tasks
   WHERE name LIKE '$PAT' AND worker_id <> '' GROUP BY worker_id;"
