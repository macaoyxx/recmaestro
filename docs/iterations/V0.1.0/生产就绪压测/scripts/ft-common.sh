#!/usr/bin/env bash
# ft-common.sh —— 容错注入场景公共函数（被其余 ft-*.sh source）。
# 只在 WSL 内使用；API 一律走 http://127.0.0.1:8080。
set -u
FT_REPO=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro
FT_API=http://127.0.0.1:8080
FT_SCRIPTS="$FT_REPO/docs/iterations/V0.1.0/生产就绪压测/scripts"

# ft_login：stdout 输出 access_token（复用 login.sh，密码不回显）
ft_login() { bash "$FT_SCRIPTS/login.sh"; }

# ft_get <token> <path> [curl额外参数...]：GET 并输出响应体
ft_get() { local tok=$1 path=$2; shift 2; curl -s --max-time 15 "$FT_API$path" -H "Authorization: Bearer $tok" "$@"; }

# ft_post <token> <path> <json>：POST 并输出响应体
ft_post() { local tok=$1 path=$2 json=$3; curl -s --max-time 15 -X POST "$FT_API$path" -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d "$json"; }

# ft_jget <json> <python表达式>：从 stdin JSON 提取字段（依赖 python3）
ft_jget() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null; }
