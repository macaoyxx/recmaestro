#!/bin/bash
# retest-hc2-ffprobe.sh — 风暴产物下载复核：抽 2 个不同 worker 的 completed 任务下载 + ffprobe；附风暴窗口 5xx/1040 断言。
set -u
S=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
q() {
  docker exec recmaestro-mysql sh -c "exec mysql --default-character-set=utf8mb4 -uroot -p\"\$MYSQL_ROOT_PASSWORD\" live_recorder -N -e \"$1\"" 2>/dev/null
}
q "SELECT id, name, worker_id FROM (
     SELECT t.id, t.name, t.worker_id,
            ROW_NUMBER() OVER (PARTITION BY t.worker_id ORDER BY t.id) AS rn
     FROM live_rec_tasks t
     WHERE t.name LIKE '%压测R-%' AND t.status='completed'
   ) x WHERE rn=1 LIMIT 2;" > "$S/retest-hc2-ffprobe-ids.txt"
echo "== 抽样任务 =="
cat "$S/retest-hc2-ffprobe-ids.txt"
echo
while IFS=$'\t' read -r id name worker; do
  [ -z "$id" ] && continue
  echo "--- $name ($worker) id=$id ---"
  bash "$S/ft-download.sh" "$id" "$S/retest-hc2-dl-sample.mp4"
  echo "任务日志对账文案:"
  q "SELECT message FROM live_rec_task_logs WHERE task_id='$id' AND message LIKE '%对账通过%';"
  echo
done < "$S/retest-hc2-ffprobe-ids.txt"

echo "== 风暴窗口 scheduler GIN 5xx 计数（16:23:00 起）=="
docker logs recmaestro-scheduler --since "2026-10-15T08:23:00Z" 2>&1 | grep 'GIN' | grep -cE ' 50[0-9] '
echo "== 风暴窗口各 worker 容器 Error 1040 计数（16:23:00 起）=="
for c in $(docker ps --format '{{.Names}}' | grep deploy-worker); do
  n=$(docker logs "$c" --since "2026-10-15T08:23:00Z" 2>&1 | grep -c 'Too many connections')
  echo "$c: $n"
done
