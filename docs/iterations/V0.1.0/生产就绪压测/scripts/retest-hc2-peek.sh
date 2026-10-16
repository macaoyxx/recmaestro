#!/bin/bash
# retest-hc2-peek.sh — 查看非 completed 风暴任务明细与其最新任务日志。
set -u
DB=live_recorder
q() {
  docker exec recmaestro-mysql sh -c "exec mysql --default-character-set=utf8mb4 -uroot -p\"\$MYSQL_ROOT_PASSWORD\" $DB -e \"$1\"" 2>/dev/null
}
echo "== 非 completed 任务 =="
q "SELECT id, name, status, worker_id, updated_at FROM live_rec_tasks WHERE name LIKE '%压测R-%' AND status <> 'completed';"
echo
echo "== 这些任务的最近 8 条日志 =="
q "SELECT t.name, l.ts, l.stage, l.node_id, LEFT(l.message, 120) AS msg
   FROM live_rec_tasks t JOIN live_rec_task_logs l ON l.task_id = t.id
   WHERE t.name LIKE '%压测R-%' AND t.status <> 'completed'
   ORDER BY l.id DESC LIMIT 8;"
echo
echo "== 全体风暴任务当前状态分布（复查）=="
q "SELECT status, COUNT(*) FROM live_rec_tasks WHERE name LIKE '%压测R-%' GROUP BY status;"
