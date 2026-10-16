#!/usr/bin/env bash
# ft-wait-status.sh —— 轮询任务直到出现目标状态之一；输出每次状态变迁，结束打印最终状态行。
# 用法：ft-wait-status.sh <任务id> <超时秒> <状态1> [状态2...]
# 退出码：0=到达目标状态；1=超时；2=任务接口异常
set -u
. "$(dirname "$0")/ft-common.sh"
id=${1:?}; timeout=${2:?}; shift 2
[ $# -ge 1 ] || { echo "至少给一个目标状态" >&2; exit 2; }

tok=$(ft_login) || exit 2
last=""
deadline=$(( $(date +%s) + timeout ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  resp=$(ft_get "$tok" "/api/tasks/$id")
  st=$(printf '%s' "$resp" | ft_jget "d['data']['status']")
  if [ -z "$st" ]; then
    echo "[$(date '+%H:%M:%S')] 查询失败: $(printf '%s' "$resp" | head -c 120)"
    sleep 5; continue
  fi
  if [ "$st" != "$last" ]; then
    echo "[$(date '+%H:%M:%S')] status=$st"
    last=$st
  fi
  for target in "$@"; do
    [ "$st" = "$target" ] && { echo "FINAL=$st"; exit 0; }
  done
  sleep 5
done
echo "FINAL=${last:-unknown}（超时 ${timeout}s）"
exit 1
