#!/bin/bash
# retest-hc3-ffprobe.sh — 补录2·压测S 风暴产物下载复核：抽 2 个不同 worker 的 completed 任务下载 + ffprobe。
set -u
S=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
cd "$S"
q() {
  docker exec recmaestro-mysql sh -c "exec mysql --default-character-set=utf8mb4 -uroot -p\"\$MYSQL_ROOT_PASSWORD\" live_recorder -N -e \"$1\"" 2>/dev/null
}
q "SELECT id, name, worker_id FROM (
     SELECT t.id, t.name, t.worker_id,
            ROW_NUMBER() OVER (PARTITION BY t.worker_id ORDER BY t.id) AS rn
     FROM live_rec_tasks t
     WHERE t.name LIKE '%压测S-%' AND t.status='completed'
   ) x WHERE rn=1 LIMIT 2;" > retest-hc3-ffprobe-ids.txt
echo "== 抽样任务 =="
cat retest-hc3-ffprobe-ids.txt
echo
while IFS=$'\t' read -r id name worker; do
  [ -z "$id" ] && continue
  echo "--- $name ($worker) id=$id ---"
  bash ft-download.sh "$id" "$S/retest-hc3-dl-sample.mp4"
  echo "ffprobe 下载产物:"
  ffprobe -v error -show_entries format=duration -of csv=p=0 "$S/retest-hc3-dl-sample.mp4" 2>&1
  echo "任务日志对账文案:"
  q "SELECT message FROM live_rec_task_logs WHERE task_id='$id' AND message LIKE '%对账通过%';"
  echo
done < retest-hc3-ffprobe-ids.txt
