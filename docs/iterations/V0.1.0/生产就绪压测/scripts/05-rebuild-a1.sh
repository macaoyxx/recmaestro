#!/bin/bash
# 05-灾备恢复 · 阶段3A1：全栈重建（down 不带 -v → up --scale worker=3）
# 断言：卷保留、healthz 恢复、恢复总时长、worker×3 在线；中断任务【灾备-4】留待 3A2 观测续录。
set -u
BASE=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro
SC="$BASE/docs/iterations/V0.1.0/生产就绪压测/scripts"
API=http://127.0.0.1:8080
DEPLOY="$BASE/deploy"
source "$BASE/deploy/.env" 2>/dev/null || true
PW="${MYSQL_PASSWORD:-root123456}"
sq() { docker exec recmaestro-mysql mysql -uroot -p"$PW" live_recorder -N -B -e "$1" 2>/dev/null; }

tok=$(bash "$SC/login.sh") || { echo "@@ FATAL 登录失败"; exit 1; }
echo "@@ LOGIN_OK"
api() { curl -s -H "Authorization: Bearer $tok" "$@"; }
task_field() { api "$API/api/tasks/$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" | head -1; }

echo "@@ ===== 重建前快照 ====="
sq "SELECT 'users',COUNT(*),COALESCE(SUM(id),0),COALESCE(MAX(id),0) FROM live_rec_users
UNION ALL SELECT 'tasks',COUNT(*),SUM(id),MAX(id) FROM live_rec_tasks
UNION ALL SELECT 'task_files',COUNT(*),SUM(id),MAX(id) FROM live_rec_task_files
UNION ALL SELECT 'task_logs',COUNT(*),SUM(id),MAX(id) FROM live_rec_task_logs
UNION ALL SELECT 'nodes',COUNT(*),SUM(id),MAX(id) FROM live_rec_nodes
UNION ALL SELECT 'audit_logs',COUNT(*),SUM(id),MAX(id) FROM live_rec_audit_logs" | tee /tmp/05-rb-sql-pre.txt
echo "-- tasks by status:"; sq "SELECT status,COUNT(*) FROM live_rec_tasks GROUP BY status ORDER BY status" | tee -a /tmp/05-rb-sql-pre.txt
api -X POST $API/api/tasks/list -H 'Content-Type: application/json' -d '{"page":1,"page_size":200}' > /tmp/05-rb-tasks-pre.json
echo "-- 任务列表快照: $(grep -o '"id":"' /tmp/05-rb-tasks-pre.json | wc -l) 条 → /tmp/05-rb-tasks-pre.json"
echo "-- 容器IP快照（重建前）:"
for c in deploy-worker-1 deploy-worker-2 deploy-worker-3 recmaestro-scheduler; do
  echo "$c $(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' $c)"
done | tee /tmp/05-rb-ip-pre.txt
ENV1=2110556939766206464
. "$SC/05-ids.env" 2>/dev/null || true
api $API/api/tasks/$ENV1/files > /tmp/05-rb-env1-files-pre.json
echo "-- 【环境-1】files: $(grep -o '"filename"' /tmp/05-rb-env1-files-pre.json | wc -l) 项"
echo "-- 卷快照:"; docker volume ls --format '{{.Name}}' | grep -E 'mysql_data|redis_data|consul_data|worker_records'

# ---- 创建【灾备-4】：立即、窗口 12 分钟，将被打断 ----
end4=$(date -d '+720 seconds' '+%Y-%m-%dT%H:%M:%S+08:00')
resp=$(curl -s -X POST $API/api/tasks -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
  -d '{"stream_url":"rtmp://recmaestro-srs-e2e:1935/live/e2e","name":"\u3010\u707e\u5907-4\u3011","segment_seconds":30,"end_time":"'$end4'"}')
echo "@@ CREATE_灾备4 resp=$(echo "$resp" | head -c 260)"
id4=$(echo "$resp" | sed -n 's/.*"id":"\([0-9]*\)".*/\1/p' | head -1)
echo "@@ ID4=$id4 end_time=$end4"
st4=""
for i in $(seq 1 45); do st4=$(task_field "$id4" status); [ "$st4" = "running" ] && break; sleep 2; done
wid4=$(task_field "$id4" worker_id)
echo "@@ 灾备4 status=$st4 worker_id=$wid4（原节点）"
echo "$id4" > /tmp/05-id4.txt
sleep 60
ip4=${wid4#recmaestro-worker-}; ip4=${ip4%-8080}
C4=""
for c in deploy-worker-1 deploy-worker-2 deploy-worker-3; do
  cip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$c")
  [ "$cip" = "$ip4" ] && C4=$c
done
seg4=$(docker exec "$C4" sh -c "ls /records/$id4/seg_*.ts 2>/dev/null | wc -l")
echo "@@ down前: 灾备4 已录段数=$seg4（容器 $C4）ts=$(date '+%H:%M:%S')"
printf 'ID4=%s\nWID4=%s\nC4=%s\nSEG4=%s\n' "$id4" "$wid4" "$C4" "$seg4" >> "$SC/05-ids.env"

# ---- 注入：docker compose down（严禁 -v）----
t0=$(date +%s.%N)
cd "$DEPLOY" && docker compose down 2>&1 | tail -12
t_down=$(date +%s.%N)
echo "@@ COMPOSE_DOWN_DUR=$(echo "$t_down $t0" | awk '{printf "%.1f",$1-$2}')s"
echo "@@ down 后容器: $(docker ps --format '{{.Names}}' | grep -cE 'recmaestro|deploy-worker') 个 recmaestro 系容器在跑（应为 0，SRS 除外）"
echo "@@ 卷保留核验:"; docker volume ls --format '{{.Name}}' | grep -E 'mysql_data|redis_data|consul_data|worker_records'
echo "@@ SRS（不在 compose 内）: $(docker inspect -f '{{.State.Status}}' recmaestro-srs-e2e)"

# ---- 恢复：up -d --scale worker=3 ----
t_up0=$(date +%s.%N)
cd "$DEPLOY" && docker compose up -d --scale worker=3 2>&1 | tail -8
for i in $(seq 1 120); do
  code=$(curl -s -o /dev/null -w '%{http_code}' $API/healthz 2>/dev/null)
  [ "$code" = "200" ] && break
  sleep 1
done
t_up1=$(date +%s.%N)
echo "@@ UP_DUR=$(echo "$t_up1 $t_up0" | awk '{printf "%.1f",$1-$2}')s"
echo "@@ 全栈恢复总时长（down开始→healthz 200）= $(echo "$t_up1 $t0" | awk '{printf "%.1f",$1-$2}')s  healthz=$code"

# ---- worker 回归：/api/nodes 3 在线 ----
for i in $(seq 1 60); do
  online=$(api $API/api/stats | sed -n 's/.*"nodes_online":\([0-9]*\).*/\1/p')
  [ "$online" = "3" ] && break
  sleep 2
done
echo "@@ nodes_online=$online  ts=$(date '+%H:%M:%S')"
echo "-- 容器IP（重建后）:"
for c in deploy-worker-1 deploy-worker-2 deploy-worker-3 recmaestro-scheduler; do
  echo "$c $(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' $c)"
done | tee /tmp/05-rb-ip-post.txt
echo "@@ [diff IP 重建前→后]"; diff /tmp/05-rb-ip-pre.txt /tmp/05-rb-ip-post.txt && echo "IP_STABLE（node_id 不变）" || echo "IP_CHANGED（node_id 漂移，见上）"
echo "@@ [scheduler 启动日志（migrate+队列重建）]"
docker logs recmaestro-scheduler 2>&1 | grep -E "数据库迁移|队列重建完成|scheduler 启动|warn" | head -8
echo "@@ PHASE3_A1_DONE ts=$(date '+%H:%M:%S')"
