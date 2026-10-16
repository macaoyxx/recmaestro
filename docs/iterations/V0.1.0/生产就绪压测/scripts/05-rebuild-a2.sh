#!/bin/bash
# 05-灾备恢复 · 阶段3A2：重建后数据全量一致 + 已完成任务文件可列出/下载
set -u
BASE=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro
SC="$BASE/docs/iterations/V0.1.0/生产就绪压测/scripts"
API=http://127.0.0.1:8080
source "$BASE/deploy/.env" 2>/dev/null || true
PW="${MYSQL_PASSWORD:-root123456}"
sq() { docker exec recmaestro-mysql mysql -uroot -p"$PW" live_recorder -N -B -e "$1" 2>/dev/null; }

tok=$(bash "$SC/login.sh") || { echo "@@ FATAL 登录失败"; exit 1; }
echo "@@ LOGIN_OK（重建后凭据仍有效——用户数据保留）"
api() { curl -s -H "Authorization: Bearer $tok" "$@"; }

echo "@@ ===== 重建后 SQL 快照 vs 重建前 ====="
sq "SELECT 'users',COUNT(*),COALESCE(SUM(id),0),COALESCE(MAX(id),0) FROM live_rec_users
UNION ALL SELECT 'tasks',COUNT(*),SUM(id),MAX(id) FROM live_rec_tasks
UNION ALL SELECT 'task_files',COUNT(*),SUM(id),MAX(id) FROM live_rec_task_files
UNION ALL SELECT 'task_logs',COUNT(*),SUM(id),MAX(id) FROM live_rec_task_logs
UNION ALL SELECT 'nodes',COUNT(*),SUM(id),MAX(id) FROM live_rec_nodes
UNION ALL SELECT 'audit_logs',COUNT(*),SUM(id),MAX(id) FROM live_rec_audit_logs" > /tmp/05-rb-sql-post.txt
echo "-- tasks by status:" >> /tmp/05-rb-sql-post.txt
sq "SELECT status,COUNT(*) FROM live_rec_tasks GROUP BY status ORDER BY status" >> /tmp/05-rb-sql-post.txt
diff /tmp/05-rb-sql-pre.txt /tmp/05-rb-sql-post.txt && echo "SQL_DIFF_NONE（用户/任务/文件/日志/节点/审计全量一致）" || echo "SQL_DIFF_EXIST（见上）"
echo "-- 节点历史（live_rec_nodes 内容）:"; sq "SELECT node_id,status FROM live_rec_nodes ORDER BY node_id"

echo "@@ ===== 任务列表 diff（页面可见性）====="
api -X POST $API/api/tasks/list -H 'Content-Type: application/json' -d '{"page":1,"page_size":200}' > /tmp/05-rb-tasks-post.json
pre_n=$(grep -o '"id":"' /tmp/05-rb-tasks-pre.json | wc -l); post_n=$(grep -o '"id":"' /tmp/05-rb-tasks-post.json | wc -l)
echo "任务数 重建前=$pre_n 重建后=$post_n"
for f in /tmp/05-rb-tasks-pre.json /tmp/05-rb-tasks-post.json; do
  sed 's/},/}\n/g' "$f" | grep -oE '"id":"[0-9]+"|"name":"[^"]*"|"status":"[a-z_]+"' | paste -d' ' - - - | sort > "$f.rows"
done
echo "-- 仅状态变化的任务（预期：灾备-4）:"
diff /tmp/05-rb-tasks-pre.json.rows /tmp/05-rb-tasks-post.json.rows || true
echo "-- 名称含【灾备】/【环境】的任务:"
grep -E '灾备|环境' /tmp/05-rb-tasks-post.json.rows | sed 's/"[a-z_]*"://g'

echo "@@ ===== 已完成任务文件：可列出 + 可下载 + ffprobe 复核 ====="
ENV1=2110556939766206464
. "$SC/05-ids.env"
api $API/api/tasks/$ENV1/files > /tmp/05-rb-env1-files-post.json
echo "-- 【环境-1】files 重建后: $(grep -o '"filename"' /tmp/05-rb-env1-files-post.json | wc -l) 项（重建前 1 项）"
grep -oE '"filename":"[^"]*"|"size_bytes":[0-9]+' /tmp/05-rb-env1-files-post.json
api $API/api/tasks/$ENV1/download -o /tmp/05-rb-env1.mp4
api $API/api/tasks/$ID1/download -o /tmp/05-rb-zb1.mp4
echo "-- 【环境-1】产物: size=$(stat -c %s /tmp/05-rb-env1.mp4)B ffprobe=$(ffprobe -v error -show_entries format=duration -of csv=p=0 /tmp/05-rb-env1.mp4)s（预期 ≈74.0s）"
echo "-- 【灾备-1】产物: size=$(stat -c %s /tmp/05-rb-zb1.mp4)B ffprobe=$(ffprobe -v error -show_entries format=duration -of csv=p=0 /tmp/05-rb-zb1.mp4)s（预期 ≈424.1s）"

echo "@@ ===== 灾备-4 当前态（中断任务）====="
api $API/api/tasks/$ID4/progress
echo ""
echo "@@ [Redis 心跳 zset 中灾备-4（分值冻结=心跳停止时刻）]"
docker exec recmaestro-redis redis-cli ZSCORE recmaestro:task_heartbeats "$ID4"
echo "@@ [compose ps 全景]"
docker ps --format '{{.Names}}\t{{.Status}}' | grep -E 'recmaestro|deploy-worker' | sort
echo "@@ PHASE3_A2_DONE ts=$(date '+%H:%M:%S')"
