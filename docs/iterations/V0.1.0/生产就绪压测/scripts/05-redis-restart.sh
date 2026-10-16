#!/bin/bash
# 05-灾备恢复 · 阶段1：Redis 重启灾备
# 断言：AOF 数据恢复 / worker 重新注册 / 心跳与队列恢复 / 运行中录制不受扰 / 恢复后派发继续；记录不可用窗口。
set -u
BASE=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro
SC="$BASE/docs/iterations/V0.1.0/生产就绪压测/scripts"
API=http://127.0.0.1:8080

rcli() { docker exec recmaestro-redis redis-cli "$@"; }
tok=$(bash "$SC/login.sh") || { echo "@@ FATAL 登录失败"; exit 1; }
echo "@@ LOGIN_OK token_len=${#tok}"
api() { curl -s -H "Authorization: Bearer $tok" "$@"; }
task_field() { api "$API/api/tasks/$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" | head -1; }

redis_snapshot() {
  echo "[keys]"; rcli --scan --pattern 'recmaestro:*' | sort
  echo "[scheduled zset]"; rcli ZRANGE recmaestro:scheduled 0 -1 WITHSCORES
  echo "[task_queue llen] $(rcli LLEN recmaestro:task_queue)"
  echo "[task_heartbeats]"; rcli ZRANGE recmaestro:task_heartbeats 0 -1 WITHSCORES
  echo "[worker lease TTL]"; for k in $(rcli --scan --pattern 'recmaestro:worker:*' | sort); do echo "$k TTL=$(rcli TTL "$k")"; done
  echo "[registry keys]"; rcli --scan --pattern 'recmaestro:registry:*' | sort
}

echo "@@ ===== 重启前基线 Redis 快照 ====="
redis_snapshot

# ---- 创建【灾备-1】立即任务：窗口 7 分钟，穿越 Redis 重启全程 ----
end1=$(date -d '+420 seconds' '+%Y-%m-%dT%H:%M:%S+08:00')
resp=$(curl -s -X POST $API/api/tasks -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
  -d '{"stream_url":"rtmp://recmaestro-srs-e2e:1935/live/e2e","name":"\u3010\u707e\u5907-1\u3011","segment_seconds":30,"end_time":"'$end1'"}')
echo "@@ CREATE_灾备1 resp=$(echo "$resp" | head -c 260)"
id1=$(echo "$resp" | sed -n 's/.*"id":"\([0-9]*\)".*/\1/p' | head -1)
echo "@@ ID1=$id1 end_time=$end1"

st=""
for i in $(seq 1 45); do st=$(task_field "$id1" status); [ "$st" = "recording" ] && break; sleep 2; done
wid1=$(task_field "$id1" worker_id)
echo "@@ 灾备1 status=$st worker_id=$wid1"
ip1=${wid1#recmaestro-worker-}; ip1=${ip1%-8080}
C1=""
for c in deploy-worker-1 deploy-worker-2 deploy-worker-3; do
  cip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$c")
  echo "[容器] $c ip=$cip"
  [ "$cip" = "$ip1" ] && C1=$c
done
echo "@@ 灾备1 所在容器=$C1"
ffpid_pre=$(docker top "$C1" -o pid,cmd 2>/dev/null | grep '[f]fmpeg' | awk '{print $1}' | sort | tr '\n' ',')
seg_pre=$(docker exec "$C1" sh -c "ls /records/$id1/seg_*.ts 2>/dev/null | wc -l")
echo "@@ 重启前: ffmpeg_pids=[$ffpid_pre] 段数=$seg_pre ts=$(date '+%H:%M:%S')"

# ---- 创建【灾备-2】定时任务：start=+170s，落 scheduled zset，验证重启后派发链路 ----
start2=$(date -d '+170 seconds' '+%Y-%m-%dT%H:%M:%S+08:00')
end2=$(date -d '+280 seconds' '+%Y-%m-%dT%H:%M:%S+08:00')
resp2=$(curl -s -X POST $API/api/tasks -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
  -d '{"stream_url":"rtmp://recmaestro-srs-e2e:1935/live/e2e","name":"\u3010\u707e\u5907-2\u3011","segment_seconds":30,"start_time":"'$start2'","end_time":"'$end2'"}')
echo "@@ CREATE_灾备2 resp=$(echo "$resp2" | head -c 260)"
id2=$(echo "$resp2" | sed -n 's/.*"id":"\([0-9]*\)".*/\1/p' | head -1)
echo "@@ ID2=$id2 start=$start2 end=$end2"
sleep 2
echo "@@ scheduled zset 中灾备2 score=$(rcli ZSCORE recmaestro:scheduled "$id2")"
echo "@@ 灾备1 心跳 zset score=$(rcli ZSCORE recmaestro:task_heartbeats "$id1")"

echo "@@ ===== 注入前终快照（diff 基准）====="
redis_snapshot | tee /tmp/05-redis-pre2.txt

# ---- 注入：docker restart recmaestro-redis，测不可用窗口 ----
t0=$(date +%s.%N)
docker restart recmaestro-redis >/dev/null
t_restart_done=$(date +%s.%N)
for i in $(seq 1 300); do rcli PING 2>/dev/null | grep -q PONG && break; sleep 0.2; done
t1=$(date +%s.%N)
echo "@@ REDIS_RESTART_DUR=$(echo "$t_restart_done $t0" | awk '{printf "%.1f",$1-$2}')s  PING恢复窗口=$(echo "$t1 $t0" | awk '{printf "%.1f",$1-$2}')s"

# ---- 恢复后 +2s 快照与 AOF diff ----
sleep 2
echo "@@ ===== 重启后 Redis 快照（+2s）====="
redis_snapshot | tee /tmp/05-redis-post.txt
echo "@@ [diff 注入前 → 重启后]（心跳 score 变化属正常，其余应一致）"
diff /tmp/05-redis-pre2.txt /tmp/05-redis-post.txt && echo "DIFF_NONE"

echo "@@ [容器状态]"; docker ps --format '{{.Names}}\t{{.Status}}' | grep -E 'worker|redis|scheduler|mysql|consul' | sort
echo "@@ [healthz] $(curl -s -o /dev/null -w '%{http_code}' $API/healthz)"

# ---- worker 重新注册/租约刷新断言：+15s 再采 TTL ----
sleep 13
echo "@@ [worker lease TTL +15s（应在续期，TTL 回到 ~20-30s）]"
for k in $(rcli --scan --pattern 'recmaestro:worker:*' | sort); do echo "$k TTL=$(rcli TTL "$k")"; done
echo "@@ [stats] $(api $API/api/stats)"

# ---- 录制不受扰断言：ffmpeg PID 不变 + 段数持续增长 + 状态仍 recording ----
ffpid_post=$(docker top "$C1" -o pid,cmd 2>/dev/null | grep '[f]fmpeg' | awk '{print $1}' | sort | tr '\n' ',')
seg_mid=$(docker exec "$C1" sh -c "ls /records/$id1/seg_*.ts 2>/dev/null | wc -l")
echo "@@ 重启后即时: ffmpeg_pids=[$ffpid_post]（重启前 [$ffpid_pre]）段数=$seg_mid（前 $seg_pre）状态=$(task_field "$id1" status)"
sleep 35
seg_post=$(docker exec "$C1" sh -c "ls /records/$id1/seg_*.ts 2>/dev/null | wc -l")
echo "@@ +35s 后: 段数=$seg_post（前 $seg_pre → 即时 $seg_mid → 现 $seg_post）状态=$(task_field "$id1" status)"
echo "@@ [灾备1 心跳 zset score（重启后）] $(rcli ZSCORE recmaestro:task_heartbeats "$id1")"
echo "@@ [灾备2 心跳 zset score（重启后）] $(rcli ZSCORE recmaestro:task_heartbeats "$id2")"

# ---- 日志窗口证据 ----
echo "@@ [scheduler 近3分钟 warn/调度/重建日志]"
docker logs recmaestro-scheduler --since 3m 2>&1 | grep -E "warn|队列重建|到点调度|redis|Redis" | tail -20
echo "@@ [worker $C1 近3分钟 warn 日志]"
docker logs "$C1" --since 3m 2>&1 | grep -iE "warn|redis" | tail -10

# ---- 派发继续断言：灾备2 到点离开 pending → claimed → recording ----
echo "@@ 等待灾备2 派发（start=$start2）..."
st2="pending"
for i in $(seq 1 130); do st2=$(task_field "$id2" status); [ "$st2" != "pending" ] && [ -n "$st2" ] && break; sleep 2; done
echo "@@ 灾备2 离开 pending: status=$st2 ts=$(date '+%H:%M:%S')"
for i in $(seq 1 60); do st2=$(task_field "$id2" status); [ "$st2" = "recording" ] && break; sleep 2; done
echo "@@ 灾备2 status=$st2 worker=$(task_field "$id2" worker_id) ts=$(date '+%H:%M:%S')"
echo "@@ [scheduler 灾备2 派发日志]"
docker logs recmaestro-scheduler --since 6m 2>&1 | grep "$id2" | tail -5

printf 'ID1=%s\nID2=%s\nC1=%s\nWID1=%s\n' "$id1" "$id2" "$C1" "$wid1" > "$SC/05-ids.env"
echo "@@ PHASE1_A_DONE ts=$(date '+%H:%M:%S')"
