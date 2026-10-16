#!/usr/bin/env bash
# 复测回归 1（原场景 3 路径 A）：ffmpeg 子进程死亡 → 就地重连 → 完成对账。
# 附带：pkill 后探测该 worker healthz（容错-1 修复后应 200）+ 僵尸检查。
set -u
S=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts

echo "=== 建任务【容错注入R1-1】ffmpeg子进程死亡回归 ==="
id=$(bash "$S/ft-create-task.sh" "【容错注入R1-1】ffmpeg子进程死亡回归" 240)
echo "task_id=$id"
[ -n "$id" ] || exit 1

echo "=== 等 running ==="
bash "$S/ft-wait-status.sh" "$id" 120 running || { echo "未到 running"; exit 1; }
worker=$(bash "$S/ft-find-worker.sh" "$id")
echo "worker=$worker"
[ -n "$worker" ] || exit 1
sleep 25   # 让它录一会儿（约 1 段）

echo "=== 注入前确认 ffmpeg 进程在跑 ==="
docker exec "$worker" sh -c 'for p in /proc/[0-9]*/cmdline; do tr "\0" " " < $p 2>/dev/null | grep -q ffmpeg && echo "ffmpeg_pid=${p#/proc/}"; done; true'

echo "=== 注入：docker exec $worker pkill -9 ffmpeg ==="
docker exec "$worker" pkill -9 ffmpeg; echo "pkill_rc=$?"
sleep 2
n=$(docker exec "$worker" sh -c 'for p in /proc/[0-9]*/cmdline; do tr "\0" " " < $p 2>/dev/null | grep -q ffmpeg && echo 1; done | wc -l')
echo "2s后 ffmpeg 进程数=$n"

echo "=== 注入后该 worker healthz（容错-1 修复后应 200）==="
port=$(docker ps --format '{{.Names}}\t{{.Ports}}' | grep "^$worker	" | grep -oE '[0-9]+->8080/tcp' | head -1 | cut -d- -f1)
echo "worker_port=$port"
curl -s -w '\nhealthz_http=%{http_code}\n' "http://127.0.0.1:${port}/healthz"

echo "=== 僵尸检查（/proc state 应无 Z）==="
docker exec "$worker" sh -c 'z=0; for s in /proc/[0-9]*/stat; do [ "$(awk "{print \$3}" $s 2>/dev/null)" = Z ] && { echo ZOMBIE $s; z=1; }; done; [ $z -eq 0 ] && echo "无 Z 态进程"'

echo "=== 注入即时日志（reconnect 行应与注入同刻出现）==="
bash "$S/ft-task-logs.sh" "$id" 30 | grep -E 'started|reconnect|failed' || true

echo "=== 等 completed（end=now+240s，最长 400s）==="
bash "$S/ft-wait-status.sh" "$id" 400 completed

echo "=== 任务全量日志 ==="
bash "$S/ft-task-logs.sh" "$id" 200

echo "=== 下载复核（经 scheduler→worker /internal/download 链路）==="
bash "$S/ft-download.sh" "$id" "$HOME/recmaestro-r2-ft1.mp4"
echo "DONE_A"
