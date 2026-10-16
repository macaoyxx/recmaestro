#!/usr/bin/env bash
# 补查 R1-5/6/7 终态与对账行（一次性）
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
bash "$S/cluster-task.sh" 2110620243645698048 2110620244262260736 2110620244727828480
for id in 2110620243645698048 2110620244262260736 2110620244727828480; do
  bash "$S/cluster-task-log.sh" "$id" | grep 对账 | tail -1
done
