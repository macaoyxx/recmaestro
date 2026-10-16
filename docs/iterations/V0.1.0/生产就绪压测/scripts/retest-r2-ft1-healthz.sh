#!/usr/bin/env bash
# 复测容错-1：worker /healthz、/metrics 匿名应 200；/internal 匿名/错token 应 401
set -u
cd /mnt/c/Users/macao/.zcode/workspace/live/recmaestro/deploy

# 等 scheduler healthz 就绪（最多 60s）
code=000
for i in $(seq 30); do
  code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/healthz)
  [ "$code" = "200" ] && break
  sleep 2
done
echo "[scheduler] /healthz => $code"

fail=0
docker ps --format '{{.Names}}\t{{.Ports}}' | grep deploy-worker | sort | while IFS=$'\t' read -r name ports; do
  port=$(echo "$ports" | grep -oE '[0-9]+->8080/tcp' | head -1 | cut -d- -f1)
  [ -z "$port" ] && { echo "[$name] 端口解析失败: $ports"; continue; }
  hz=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/healthz")
  body_hz=$(curl -s "http://127.0.0.1:${port}/healthz")
  mt=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/metrics")
  ia=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/internal/tasks/0/download")
  ib=$(curl -s -o /dev/null -w '%{http_code}' -H 'X-Internal-Token: wrong-token' "http://127.0.0.1:${port}/internal/tasks/0/download")
  ok=no; [ "$hz" = "200" ] && [ "$mt" = "200" ] && [ "$ia" = "401" ] && [ "$ib" = "401" ] && ok=yes
  echo "[$name :$port] healthz=$hz body=$body_hz | metrics=$mt | internal匿名=$ia internal错token=$ib | => $ok"
done
