#!/usr/bin/env bash
# 集群扩展场景：worker 容器名 <-> 容器 IP 映射（node_id 按 IP 定位容器）
set -euo pipefail
for c in $(docker ps --format '{{.Names}}' | grep '^deploy-worker-' | sort); do
  ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$c")
  st=$(docker inspect -f '{{.State.Status}}' "$c")
  echo "$c | $ip | $st"
done
