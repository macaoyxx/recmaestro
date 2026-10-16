#!/usr/bin/env bash
# ft-sample.sh —— 注入期间 API 可用性采样：healthz + 任务列表（读接口）+ 建任务探针（可选）。
# 每次采样一行：<时间戳> healthz=<code> list=<code> [create=<code/信封msg>]
# 用法：ft-sample.sh <持续秒> <间隔秒> [probe_create=1 表示附带建任务探针]
# 说明：探针建的任务名带【容错-探针】前缀，end 60s，同样保留不删。
set -u
. "$(dirname "$0")/ft-common.sh"
dur=${1:?}; iv=${2:-5}; probe=${3:-0}
tok=$(ft_login) || exit 2
end=$(( $(date +%s) + dur ))
n=0; fail=0
while [ "$(date +%s)" -lt "$end" ]; do
  hz=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$FT_API/healthz" 2>/dev/null || echo ERR)
  if ft_post "$tok" /api/tasks/list '{"page":1,"page_size":5}' | grep -q '"code":0'; then lst=200; else lst=FAIL; fi
  line="$(date '+%H:%M:%S') healthz=$hz list=$lst"
  if [ "$hz" = "200" ] && [ "$lst" = "200" ]; then
    n=$((n+1)); [ "$n" -eq 1 ] && echo "$line（首条正常采样）"
  else
    fail=$((fail+1)); echo "$line <<<异常"
  fi
  if [ "$probe" = "1" ] && [ $(( $(date +%s) % 30 )) -lt "$iv" ]; then
    endt=$(date -d '+60 seconds' '+%Y-%m-%dT%H:%M:%S%:z')
    cresp=$(ft_post "$tok" /api/tasks "{\"stream_url\":\"rtmp://recmaestro-srs-e2e:1935/live/e2e\",\"name\":\"【容错-探针】$(date +%H%M%S)\",\"segment_seconds\":30,\"end_time\":\"$endt\"}")
    ccode=$(printf '%s' "$cresp" | ft_jget "d.get('code')")
    cmsg=$(printf '%s' "$cresp" | ft_jget "(d.get('msg') or '')[:40]")
    echo "$(date '+%H:%M:%S') create探针 code=$ccode msg=$cmsg"
  fi
  sleep "$iv"
done
echo "SUMMARY: 正常=$n 异常=$fail（间隔${iv}s，共约$((dur/iv))次）"
