#!/usr/bin/env bash
# ft-create-task.sh —— 创建立即开始的录制任务，stdout 只输出任务 id。
# 用法：ft-create-task.sh <任务名(可中文)> <end_offset秒，距 now>
# 说明：省略 start_time（立即任务最稳，见 E2E memory）；segment_seconds=30。
set -u
. "$(dirname "$0")/ft-common.sh"
name=${1:?用法: ft-create-task.sh <任务名> <end_offset秒>}
offset=${2:?用法: ft-create-task.sh <任务名> <end_offset秒>}

tok=$(ft_login) || { echo "登录失败" >&2; exit 1; }
end=$(date -d "+${offset} seconds" '+%Y-%m-%dT%H:%M:%S%:z')
resp=$(ft_post "$tok" /api/tasks "{\"stream_url\":\"rtmp://recmaestro-srs-e2e:1935/live/e2e\",\"name\":\"${name}\",\"segment_seconds\":30,\"end_time\":\"${end}\"}")
id=$(printf '%s' "$resp" | ft_jget "d['data']['id']")
if [ -z "$id" ]; then
  echo "建任务失败，响应前 300 字符：" >&2; printf '%s' "$resp" | head -c 300 >&2; echo >&2
  exit 1
fi
printf '%s\n' "$id"
