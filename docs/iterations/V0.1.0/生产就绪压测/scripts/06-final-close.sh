#!/usr/bin/env bash
# 终验收场核验：SPA NoRoute 兜底 + 栈终态 + 推流在
set -u
echo "=== [1] SPA NoRoute 兜底（/tasks 返回 index.html）==="
curl -s -o /tmp/fv-spa-route.html -w 'HTTP:%{http_code} type:%{content_type} size:%{size_download}\n' http://127.0.0.1:8080/tasks
grep -o '<title>[^<]*</title>' /tmp/fv-spa-route.html

echo "=== [2] healthz ==="
curl -s -o /dev/null -w 'healthz=%{http_code}\n' http://127.0.0.1:8080/healthz

echo "=== [3] compose ps 终态 ==="
cd /mnt/c/Users/macao/.zcode/workspace/live/recmaestro/deploy && docker compose ps --format 'table {{.Name}}\t{{.Status}}'

echo "=== [4] worker 实例数 ==="
docker compose ps --status running --quiet worker | wc -l

echo "=== [5] 推流源 ==="
docker ps --filter name=recmaestro-srs-e2e --format '{{.Names}} {{.Status}}'
pgrep -f 'ffmpeg.*recmaestro-e2e-src' >/dev/null && echo "常驻推流进程在跑" || echo "[WARN] 推流进程未找到"
ffprobe -v error -show_entries stream=codec_type -of csv=p=0 rtmp://127.0.0.1:21935/live/e2e 2>/dev/null | sort | tr '\n' ' '; echo
