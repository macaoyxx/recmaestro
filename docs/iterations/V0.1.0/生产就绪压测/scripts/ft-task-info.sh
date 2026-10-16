#!/usr/bin/env bash
# ft-task-info.sh —— 输出任务关键字段单行摘要（状态/worker/重试/错误码/时间）。
# 用法：ft-task-info.sh <任务id>
set -u
. "$(dirname "$0")/ft-common.sh"
id=${1:?}
tok=$(ft_login) || exit 2
ft_get "$tok" "/api/tasks/$id" | ft_jget "'id=%s status=%s worker=%s retry=%s/%s err=%s start=%s end=%s' % (d['data']['id'], d['data']['status'], d['data'].get('worker_id') or '-', d['data'].get('retry_count'), d['data'].get('max_retry'), ((d['data'].get('error_code') or '-') + ':' + (d['data'].get('error_msg') or '-')[:60]), (d['data'].get('actual_start_time') or '-'), (d['data'].get('actual_end_time') or '-'))"
