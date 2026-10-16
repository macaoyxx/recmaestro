#!/bin/bash
# retest-hc3-verify-stuck.sh — 补录2·任务3：历史卡住任务【压测R-78】【压测R-42】自愈收敛三表核验。
# 断言：主表 completed + 文件表恰 1 条 output.mp4 active + 日志表有 validated 与 completed（含自愈补写文案）。
set -u
DB=live_recorder
q() {
  docker exec recmaestro-mysql sh -c "exec mysql --default-character-set=utf8mb4 -uroot -p\"\$MYSQL_ROOT_PASSWORD\" $DB -e \"$1\"" 2>/dev/null
}

echo "== 0) 表结构（列名核对）=="
q "SHOW COLUMNS FROM live_rec_task_files;" | awk '{print $1}' | tr '\n' ' '; echo

echo
echo "== 1) 主表终态 =="
q "SELECT name, status, updated_at FROM live_rec_tasks WHERE name IN ('【压测R-78】','【压测R-42】');"

echo
echo "== 2) 文件表 output.mp4 记录 =="
q "SELECT t.name, f.filename, f.type, f.size_bytes, f.status, f.created_at
   FROM live_rec_task_files f JOIN live_rec_tasks t ON t.id=f.task_id
   WHERE t.name IN ('【压测R-78】','【压测R-42】');"

echo
echo "== 3) 日志表 validated/completed/自愈 记录 =="
q "SELECT t.name, l.stage, l.ts, LEFT(l.message,90) AS msg
   FROM live_rec_task_logs l JOIN live_rec_tasks t ON t.id=l.task_id
   WHERE t.name IN ('【压测R-78】','【压测R-42】')
     AND (l.stage IN ('validated','completed') OR l.message LIKE '%自愈%')
   ORDER BY l.id;"

echo
echo "== 4) 三表一致性汇总断言 =="
q "SELECT
     (SELECT COUNT(*) FROM live_rec_tasks WHERE name IN ('【压测R-78】','【压测R-42】') AND status='completed') AS main_completed,
     (SELECT COUNT(*) FROM live_rec_task_files f JOIN live_rec_tasks t ON t.id=f.task_id
        WHERE t.name IN ('【压测R-78】','【压测R-42】') AND f.filename='output.mp4' AND f.type='output') AS file_output_rows,
     (SELECT COUNT(DISTINCT l.task_id) FROM live_rec_task_logs l JOIN live_rec_tasks t ON t.id=l.task_id
        WHERE t.name IN ('【压测R-78】','【压测R-42】') AND l.stage='completed') AS log_completed_tasks,
     (SELECT COUNT(DISTINCT l.task_id) FROM live_rec_task_logs l JOIN live_rec_tasks t ON t.id=l.task_id
        WHERE t.name IN ('【压测R-78】','【压测R-42】') AND l.message LIKE '%自愈补写%') AS healed_tasks;"

echo
echo "== 5) 磁盘产物在（worker 容器内 ffprobe 探测）=="
for id in 2110647684950921223 2110647685064167424; do
  hit=$(docker ps --format '{{.Names}}' | grep deploy-worker | while read -r c; do
    if docker exec "$c" sh -c "test -f /records/$id/output.mp4" 2>/dev/null; then echo "$c"; fi
  done | head -1)
  if [ -n "$hit" ]; then
    dur=$(docker exec "$hit" sh -c "ffprobe -v error -show_entries format=duration -of csv=p=0 /records/$id/output.mp4" 2>/dev/null)
    echo "$id → 持有节点 $hit, ffprobe 时长 ${dur}s"
  else
    echo "$id → 未在任何 worker 容器找到 /records/$id/output.mp4"
  fi
done
