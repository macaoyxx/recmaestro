#!/bin/bash
# retest-hc2-workerlog.sh — 定位 172.21.0.8 对应容器，取 16:28 窗口错误日志与任务收尾报错。
set -u
echo "== 各 worker 容器 IP =="
for c in $(docker ps --format '{{.Names}}' | grep deploy-worker); do
  ip=$(docker inspect "$c" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}')
  echo "$c $ip"
done

echo
echo "== 全 worker 容器 16:27:30~16:33:00 错误/警告日志 =="
for c in $(docker ps --format '{{.Names}}' | grep deploy-worker); do
  out=$(docker logs "$c" --since "2026-10-15T08:27:30Z" --until "2026-10-15T08:33:00Z" 2>&1 | grep -iE 'error|错误|fail|失败|too many|refused' | head -10)
  [ -n "$out" ] && { echo "--- $c ---"; printf '%s\n' "$out"; }
done

echo
echo "== scheduler 同窗口错误日志 =="
docker logs recmaestro-scheduler --since "2026-10-15T08:27:30Z" --until "2026-10-15T08:33:00Z" 2>&1 | grep -iE 'error|错误|fail|失败|too many|refused' | head -20
