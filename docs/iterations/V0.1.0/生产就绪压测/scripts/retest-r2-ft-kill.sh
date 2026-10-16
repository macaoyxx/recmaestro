#!/usr/bin/env bash
# 复测回归 2（原场景 2）：worker 强杀（stop -t 0）→ recovering → 原节点恢复续录 → 对账。
# 注入期间持续采样 scheduler healthz + POST /api/tasks/list（读接口可用性）。
set -u
S=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
SAMPLE=/tmp/r2_ft2_sample.log
: > "$SAMPLE"

echo "=== 建任务【容错注入R1-2】强杀续录回归（end=now+540s）==="
id=$(bash "$S/ft-create-task.sh" "【容错注入R1-2】强杀续录回归" 540)
echo "task_id=$id"
[ -n "$id" ] || exit 1

echo "=== 等 running ==="
bash "$S/ft-wait-status.sh" "$id" 120 running || { echo "未到 running"; exit 1; }
worker=$(bash "$S/ft-find-worker.sh" "$id")
echo "worker=$worker"
[ -n "$worker" ] || exit 1

echo "=== 启动采样器（healthz + list，5s 间隔，同会话后台）==="
( tok=$(bash "$S/login.sh"); i=0
  while [ $i -lt 150 ]; do
    hz=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 http://127.0.0.1:8080/healthz)
    ls=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 -X POST http://127.0.0.1:8080/api/tasks/list -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d '{"page":1,"page_size":5}')
    echo "$(date '+%H:%M:%S') healthz=$hz list=$ls" >> "$SAMPLE"
    sleep 5; i=$((i+1))
  done ) &
SAMPLER=$!

echo "=== 录制 100s（积累停机前素材）==="
sleep 100
started_line=$(bash "$S/ft-task-logs.sh" "$id" 50 | grep '|started|' | head -1)
echo "停机前 started 行: $started_line"
echo "停机时刻: $(date '+%H:%M:%S')"

echo "=== 注入：docker stop -t 0 $worker ==="
t_stop0=$(date +%s)
docker stop -t 0 "$worker"
t_stop1=$(date +%s)
echo "stop 耗时 $((t_stop1-t_stop0))s；容器退出码 $(docker inspect -f '{{.State.ExitCode}}' "$worker")"
docker logs --tail 4 "$worker" 2>&1 | sed 's/^/  worker日志| /'

echo "=== 等 recovering（recover_window 3m + tick，最长 400s）==="
bash "$S/ft-wait-status.sh" "$id" 400 recovering || echo "（400s 内未见 recovering，继续）"

echo "=== docker start $worker ==="
docker start "$worker"
t_start=$(date +%s)
echo "停机->重启间隔 $((t_start-t_stop0))s"

echo "=== start 后 1s 间隔轮询状态（断言 15s 内续录 running）==="
tok=$(bash "$S/login.sh")
resume=timeout
for i in $(seq 30); do
  st=$(curl -s --max-time 3 -H "Authorization: Bearer $tok" "http://127.0.0.1:8080/api/tasks/$id" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d['data']['status'])" 2>/dev/null)
  echo "  start+${i}s status=${st:-查询失败}"
  if [ "$st" = "running" ]; then resume=$i; break; fi
  sleep 1
done
echo "RESUME=${resume}s（SLA 15s 内）"

echo "=== 续录衔接日志 ==="
bash "$S/ft-task-logs.sh" "$id" 50 | grep -E 'started|claimed|recover' || true

echo "=== 等 completed（最长 420s）==="
bash "$S/ft-wait-status.sh" "$id" 420 completed

kill "$SAMPLER" 2>/dev/null
wait "$SAMPLER" 2>/dev/null

echo "=== 采样汇总（注入期间 scheduler API 可用性）==="
total=$(wc -l < "$SAMPLE")
ok=$(grep -c 'healthz=200 list=200' "$SAMPLE" || true)
bad=$((total-ok))
echo "样本总数=$total 正常(healthz=200且list=200)=$ok 异常=$bad"
grep -v 'healthz=200 list=200' "$SAMPLE" | head -10 || true

echo "=== 任务全量日志 ==="
bash "$S/ft-task-logs.sh" "$id" 300

echo "=== 下载复核（经 scheduler→worker /internal/download 链路）==="
bash "$S/ft-download.sh" "$id" "$HOME/recmaestro-r2-ft2.mp4"
cp "$SAMPLE" "$HOME/r2_ft2_sample.log"
echo "DONE_B"
