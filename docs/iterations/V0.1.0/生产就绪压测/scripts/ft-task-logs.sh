#!/usr/bin/env bash
# ft-task-logs.sh —— 输出任务全量日志（时间/级别/来源/阶段/内容）。
# 用法：ft-task-logs.sh <任务id> [limit，默认500]
set -u
. "$(dirname "$0")/ft-common.sh"
id=${1:?}; limit=${2:-500}
tok=$(ft_login) || exit 2
ft_get "$tok" "/api/tasks/$id/logs?limit=$limit" | ft_jget "'\n'.join('%s|%s|%s|%s|%s' % (i.get('ts','')[:19], i.get('level',''), i.get('source',''), i.get('stage',''), i.get('message','')) for i in d['data'])"
