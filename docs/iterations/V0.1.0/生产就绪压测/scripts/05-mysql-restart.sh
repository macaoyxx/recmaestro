#!/bin/bash
# 05-灾备恢复 · 阶段2：MySQL 重启零丢失验证
# 断言：restart 前后 任务/用户/审计（含文件/日志/节点）计数完全一致；期间不做任何写操作。
set -u
BASE=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro
SC="$BASE/docs/iterations/V0.1.0/生产就绪压测/scripts"
API=http://127.0.0.1:8080
source "$BASE/deploy/.env" 2>/dev/null || true
PW="${MYSQL_PASSWORD:-root123456}"

sq() { docker exec recmaestro-mysql mysql -uroot -p"$PW" live_recorder -N -B -e "$1" 2>/dev/null; }

tok=$(bash "$SC/login.sh") || { echo "@@ FATAL 登录失败"; exit 1; }
echo "@@ LOGIN_OK（此后至快照B不再有任何登录/写操作）"
api() { curl -s -H "Authorization: Bearer $tok" "$@"; }

echo "@@ [残留 pending 任务（防计数窗口干扰，仅记录）]"
sq "SELECT id,name,status,IFNULL(DATE_FORMAT(start_time,'%H:%i:%s'),''),IFNULL(DATE_FORMAT(end_time,'%H:%i:%s'),'') FROM live_rec_tasks WHERE status IN ('pending','queued')"

snap() {
  sq "SELECT 'users',COUNT(*),COALESCE(SUM(id),0),COALESCE(MAX(id),0) FROM live_rec_users
UNION ALL SELECT 'tasks',COUNT(*),SUM(id),MAX(id) FROM live_rec_tasks
UNION ALL SELECT 'task_files',COUNT(*),SUM(id),MAX(id) FROM live_rec_task_files
UNION ALL SELECT 'task_logs',COUNT(*),SUM(id),MAX(id) FROM live_rec_task_logs
UNION ALL SELECT 'nodes',COUNT(*),SUM(id),MAX(id) FROM live_rec_nodes
UNION ALL SELECT 'audit_logs',COUNT(*),SUM(id),MAX(id) FROM live_rec_audit_logs"
  echo "-- tasks by status:"
  sq "SELECT status,COUNT(*) FROM live_rec_tasks GROUP BY status ORDER BY status"
  echo "-- nodes rows:"
  sq "SELECT node_id,status FROM live_rec_nodes ORDER BY node_id"
}

echo "@@ ===== 快照A（重启前）$(date '+%H:%M:%S') ====="
snap | tee /tmp/05-mysql-a.txt

t0=$(date +%s.%N)
docker restart recmaestro-mysql >/dev/null
t_restart_done=$(date +%s.%N)
for i in $(seq 1 180); do
  h=$(docker inspect -f '{{.State.Health.Status}}' recmaestro-mysql 2>/dev/null)
  [ "$h" = "healthy" ] && break
  sleep 1
done
t1=$(date +%s.%N)
echo "@@ MYSQL_RESTART_DUR=$(echo "$t_restart_done $t0" | awk '{printf "%.1f",$1-$2}')s  HEALTHY_AFTER=$(echo "$t1 $t0" | awk '{printf "%.1f",$1-$2}')s（不可用窗口）"

echo "@@ ===== 快照B（重启后）$(date '+%H:%M:%S') ====="
snap | tee /tmp/05-mysql-b.txt

echo "@@ [diff A → B]"
if diff /tmp/05-mysql-a.txt /tmp/05-mysql-b.txt; then echo "DIFF_NONE（计数完全一致，零丢失）"; else echo "DIFF_EXIST（见上，需甄别是否丢失）"; fi

echo "@@ [healthz] $(curl -s -o /dev/null -w '%{http_code}' $API/healthz)"
echo "@@ [任务列表可用性] $(api -X POST $API/api/tasks/list -H 'Content-Type: application/json' -d '{"page":1,"page_size":5}' | head -c 200)"
echo "@@ [stats] $(api $API/api/stats)"
echo "@@ [compose ps]"; docker ps --format '{{.Names}}\t{{.Status}}' | grep -E 'mysql|redis|scheduler|worker' | sort
echo "@@ PHASE2_DONE ts=$(date '+%H:%M:%S')"
