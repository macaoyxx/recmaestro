#!/bin/bash
# retest-hc1.sh — Issue 高并发-1 复测：50/100 并发 60s 混合读压测（连续两档）+ 压测中段 TCP 采样 + scheduler 日志断言。
# 断言：错误率 0；日志无 'cannot assign requested address'；无 GIN 500。
set -u
S=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
cd "$S"
T0=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "[retest-hc1] 压测窗口起点(UTC) $T0"

tok=$(bash login.sh)
[ -n "$tok" ] || { echo "[retest-hc1] 登录失败"; exit 1; }
echo "[retest-hc1] 登录成功"

for C in 50 100; do
  echo; echo "########## 档位 ${C} 并发 60s ##########"
  ./loadgen/loadgen.bin mixed -c "$C" -d 60 -token "$tok" &
  LG=$!
  sleep 35   # 进入持续负载中段（原错误为负载 30s+ 后阶段爆发）
  echo "---- 压测中段 tcpstat 采样 ----"
  bash tcpstat.sh
  wait $LG
  echo "---- 档位结束 ----"
done

echo; echo "== 日志断言（窗口 ${T0} 起）=="
L=$(docker logs recmaestro-scheduler --since "$T0" 2>&1)
echo -n "cannot assign requested address 计数: "
printf '%s\n' "$L" | grep -c 'cannot assign requested address'
echo -n "GIN 500 计数: "
printf '%s\n' "$L" | grep 'GIN' | grep -c ' 500 '
echo -n "GIN 5xx 总计: "
printf '%s\n' "$L" | grep 'GIN' | grep -cE ' 50[0-9] '
