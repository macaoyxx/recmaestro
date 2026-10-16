#!/bin/bash
# retest-hc3-regress.sh — 补录2·回归抽查：①50 并发 60s 混合读（错误 0）；②20 并发 60s（错误 0）；③登录限流 429 触发与恢复。
set -u
S=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
cd "$S"
PW=$(grep -E '^ADMIN_INITIAL_PASSWORD=' /mnt/c/Users/macao/.zcode/workspace/live/recmaestro/deploy/.env | cut -d= -f2-)
[ -z "$PW" ] && PW=admin123

tok=$(bash login.sh)
[ -n "$tok" ] || { echo "登录失败"; exit 1; }

echo "== 1) 50 并发 60s 混合读回归 =="
./loadgen/loadgen.bin mixed -c 50 -d 60 -token "$tok"

echo
echo "== 2) 20 并发 60s 混合读回归 =="
./loadgen/loadgen.bin mixed -c 20 -d 60 -token "$tok"

echo
echo "== 3) 登录限流：等 60s 清窗口计数后连发 12 次 =="
sleep 60
./loadgen/loadgen.bin loginburst -n 12 -user admin -pass "$PW"

echo "== 4) 65s 后恢复验证 =="
sleep 65
./loadgen/loadgen.bin loginburst -n 1 -user admin -pass "$PW"
