#!/usr/bin/env bash
# ensure-stack.sh —— 压测战役环境复用：确认 WSL 保活 + RecMaestro 栈健康，不在则拉起。
# 用法（WSL 内绝对路径）：bash /mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts/ensure-stack.sh
# 幂等：栈已健康则只报告不动栈；healthz 不通才执行 compose up（绝不 down -v）。
set -u
REPO=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro
API=http://127.0.0.1:8080

# 1) WSL 保活：VM 空闲自动关机会让容器/推流全灭，必须先确认
if pgrep -xf 'sleep infinity' >/dev/null 2>&1; then
  echo "[保活] sleep infinity 在跑 (pid $(pgrep -xf 'sleep infinity' | head -1))"
else
  setsid nohup sleep infinity >/dev/null 2>&1 < /dev/null &
  echo "[保活] 已补挂 sleep infinity"
fi

cd "$REPO/deploy" || { echo "[栈] 进不去 $REPO/deploy" >&2; exit 1; }

healthz_ok() { curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$API/healthz" 2>/dev/null | grep -q '^200$'; }

# 2) 栈不在/不健康才 up（基线 worker=3；已在跑则不碰，worker 数由场景自行控制）
if healthz_ok; then
  echo "[栈] healthz 200，栈已在跑"
else
  echo "[栈] healthz 不通，执行 docker compose up -d --scale worker=3"
  docker compose up -d --scale worker=3 || { echo "[栈] compose up 失败" >&2; exit 1; }
  # 3) 等 healthz 就绪（scheduler 连 mysql/redis 需要几秒，上限 120s）
  ok=no
  for i in $(seq 1 60); do
    sleep 2
    if healthz_ok; then ok=yes; echo "[栈] healthz 200，就绪（等待约 $((i*2))s）"; break; fi
  done
  [ "$ok" = yes ] || { echo "[栈] 等待超时：healthz 120s 内未就绪，查 docker compose ps / scheduler 日志" >&2; exit 1; }
fi

echo "[栈] worker 实例数：$(docker compose ps -q worker | wc -l)（基线 3；需扩缩用 scaleCommand 自行调）"
docker compose ps
