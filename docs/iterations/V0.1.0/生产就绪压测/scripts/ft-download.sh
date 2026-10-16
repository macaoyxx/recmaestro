#!/usr/bin/env bash
# ft-download.sh —— 下载任务产物到指定路径并 ffprobe 时长。
# 用法：ft-download.sh <任务id> <输出文件路径>
set -u
. "$(dirname "$0")/ft-common.sh"
id=${1:?}; out=${2:?}
tok=$(ft_login) || exit 2
code=$(curl -s --max-time 120 -w '%{http_code}' -o "$out" "$FT_API/api/tasks/$id/download" -H "Authorization: Bearer $tok")
echo "http=$code size=$(stat -c%s "$out" 2>/dev/null || echo 0)B"
if [ "$code" = "200" ]; then
  dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$out" 2>/dev/null)
  echo "ffprobe_duration=${dur}s"
fi
