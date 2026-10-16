#!/usr/bin/env bash
# ft-find-worker.sh —— 按任务 worker_id（recmaestro-worker-<IP>-8080）定位容器名。
# 用法：ft-find-worker.sh <任务id>；stdout 输出容器名，找不到 exit 1。
set -u
. "$(dirname "$0")/ft-common.sh"
id=${1:?}
tok=$(ft_login) || exit 2
wid=$(ft_get "$tok" "/api/tasks/$id" | ft_jget "d['data'].get('worker_id') or ''")
[ -n "$wid" ] || { echo "任务无 worker_id" >&2; exit 1; }
ip=${wid#recmaestro-worker-}; ip=${ip%-8080}
echo "worker_id=$wid ip=$ip" >&2
for c in deploy-worker-1 deploy-worker-2 deploy-worker-3; do
  cip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$c" 2>/dev/null)
  if [ "$cip" = "$ip" ]; then echo "$c"; exit 0; fi
done
echo "未匹配到容器（IP 快照可能已变）" >&2; exit 1
