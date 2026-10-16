#!/usr/bin/env bash
# ft-files.sh —— 输出任务文件清单（类型/文件名/seq/状态/大小）。
# 用法：ft-files.sh <任务id>
set -u
. "$(dirname "$0")/ft-common.sh"
id=${1:?}
tok=$(ft_login) || exit 2
ft_get "$tok" "/api/tasks/$id/files" | ft_jget "'\n'.join('%s|%s|seq=%s|%s|%sB' % (i.get('type',''), i.get('filename',''), i.get('seq','-'), i.get('status',''), i.get('size_bytes',0)) for i in d['data'])"
