#!/usr/bin/env bash
# login.sh —— 登录 RecMaestro，stdout 只输出 access_token。
# 用法（WSL 内绝对路径）：tok=$(bash /mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts/login.sh)
# 密码来源：deploy/.env 的 ADMIN_INITIAL_PASSWORD（shell 内 source，绝不回显）；缺省 admin123。
# 注意：连续 5 次失败会锁号（handler_auth.go maxFailedLoginAttempts=5），别拿错密码反复试。
set -u
REPO=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro
API=http://127.0.0.1:8080

# 密码在 subshell 内取，不落终端（.env 存在才 source；变量为空回退 admin123）
pw=$(cd "$REPO/deploy" && { [ -f .env ] && . ./.env 2>/dev/null; printf '%s' "${ADMIN_INITIAL_PASSWORD:-}"; })
[ -z "$pw" ] && pw=admin123

resp=$(curl -s --max-time 10 -X POST "$API/api/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$pw\"}")
tok=$(printf '%s' "$resp" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')

if [ -z "$tok" ]; then
  echo "登录失败：响应无 access_token（前 300 字符如下）" >&2
  printf '%s' "$resp" | head -c 300 >&2; echo >&2
  exit 1
fi
printf '%s\n' "$tok"
