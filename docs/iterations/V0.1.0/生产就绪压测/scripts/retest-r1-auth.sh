#!/usr/bin/env bash
# 复测 R1 — 单机基线-1：连续错密码锁定后登录文案成因区分（修复前返回『账号已被禁用』）
# 链路：创建用户 → 5 次错密码锁定 → 锁定后登录(修复点) → enable 解锁 → 正确密码 200
#      → disable(管理员禁用成因) → 登录仍『账号已被禁用』 → enable 收场恢复
# 登录次数合计 8 次(<10/IP/分钟)，不触 IP 限流。
set -u
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API="http://127.0.0.1:8080"
U="r1-lock-u1"
PW='R1#Lock2026'
PASS=0; FAIL=0
say() { printf '[%s] %s %s\n' "$(date +%M:%S)" "$1" "$2"; }
ok()  { PASS=$((PASS+1)); say PASS "$1"; }
bad() { FAIL=$((FAIL+1)); say FAIL "$1 | $2"; }

tok=$(bash "$SCRIPT_DIR/login.sh")
[ -n "$tok" ] && ok "admin login.sh 取 token" || { bad "admin 登录" "无 token"; exit 1; }
AUTH=(-H "Authorization: Bearer $tok" -H 'Content-Type: application/json')

# msg 提取：优先 python3，退回 grep
getmsg() { printf '%s' "$1" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("msg",""))' 2>/dev/null || printf '%s' "$1" | grep -o '"msg":"[^"]*"' | cut -d'"' -f4; }

# 1. 创建测试用户（若已存在则复用）
B=$(curl -s --max-time 15 -X POST "$API/api/users" "${AUTH[@]}" -d "{\"username\":\"$U\",\"password\":\"$PW\",\"role\":\"user\"}")
C=$(printf '%s' "$B" | grep -o '"code":[0-9]*' | cut -d: -f2)
if [ "$C" = "0" ]; then ok "创建用户 $U"; else
  M=$(getmsg "$B"); case "$M" in *已存在*) ok "用户 $U 已存在（复用）";; *) bad "创建用户" "$B";; esac
fi
uid=$(curl -s --max-time 15 "$API/api/users?page=1&page_size=100" "${AUTH[@]}" | python3 -c "
import sys,json
d=json.load(sys.stdin)
items=d['data'] if isinstance(d['data'],list) else d['data'].get('list',d['data'].get('items',[]))
print(next((u['id'] for u in items if u['username']=='$U'),''))" 2>/dev/null)
[ -n "$uid" ] && ok "取到 uid=$uid" || { bad "取 uid" "空"; exit 1; }

# 2. 5 次错密码：前 4 次 401『用户名或密码错误』，第 5 次 403『密码错误次数过多，账号已锁定』
for i in 1 2 3 4; do
  B=$(curl -s --max-time 15 -X POST "$API/api/login" -H 'Content-Type: application/json' -d "{\"username\":\"$U\",\"password\":\"wrong-$i\"}")
  H=$(printf '%s' "$B" | grep -o '"code":401' | head -1); M=$(getmsg "$B")
  [ "$H" = '"code":401' ] && [ "$M" = "用户名或密码错误" ] && ok "第 $i 次错密码 401『用户名或密码错误』" || bad "第 $i 次错密码" "msg=$M"
done
B=$(curl -s --max-time 15 -X POST "$API/api/login" -H 'Content-Type: application/json' -d "{\"username\":\"$U\",\"password\":\"wrong-5\"}")
M=$(getmsg "$B")
[ "$M" = "密码错误次数过多，账号已锁定" ] && ok "第 5 次错密码 403『密码错误次数过多，账号已锁定』" || bad "第 5 次错密码" "msg=$M"

# 3. 修复点：锁定后正确密码登录 → 预期 403『账号已锁定，请联系管理员』（修复前『账号已被禁用』）
B=$(curl -s --max-time 15 -X POST "$API/api/login" -H 'Content-Type: application/json' -d "{\"username\":\"$U\",\"password\":\"$PW\"}")
M=$(getmsg "$B")
[ "$M" = "账号已锁定，请联系管理员" ] && ok "锁定后登录 403『账号已锁定，请联系管理员』（单机基线-1 修复点）" || bad "锁定后登录（修复点）" "msg=$M（预期 账号已锁定，请联系管理员）"

# 4. enable 解锁 → 正确密码 200
B=$(curl -s --max-time 15 -X PATCH "$API/api/users/$uid/status" "${AUTH[@]}" -d '{"status":"enabled"}')
[ "$(printf '%s' "$B" | grep -o '"code":0' | head -1)" = '"code":0' ] && ok "enable 解锁 200" || bad "enable 解锁" "$B"
B=$(curl -s --max-time 15 -X POST "$API/api/login" -H 'Content-Type: application/json' -d "{\"username\":\"$U\",\"password\":\"$PW\"}")
[ "$(printf '%s' "$B" | grep -o '"code":0' | head -1)" = '"code":0' ] && ok "解锁后正确密码登录 200" || bad "解锁后登录" "msg=$(getmsg "$B")"

# 5. 禁用成因（计数已清零）→ 403『账号已被禁用』（两成因可区分）
B=$(curl -s --max-time 15 -X PATCH "$API/api/users/$uid/status" "${AUTH[@]}" -d '{"status":"disabled"}')
[ "$(printf '%s' "$B" | grep -o '"code":0' | head -1)" = '"code":0' ] && ok "disable 200" || bad "disable" "$B"
B=$(curl -s --max-time 15 -X POST "$API/api/login" -H 'Content-Type: application/json' -d "{\"username\":\"$U\",\"password\":\"$PW\"}")
M=$(getmsg "$B")
[ "$M" = "账号已被禁用" ] && ok "管理员禁用成因登录 403『账号已被禁用』（与锁定成因可区分）" || bad "禁用成因登录" "msg=$M（预期 账号已被禁用）"

# 6. 收场恢复 enabled（测试数据保留，状态恢复）
B=$(curl -s --max-time 15 -X PATCH "$API/api/users/$uid/status" "${AUTH[@]}" -d '{"status":"enabled"}')
[ "$(printf '%s' "$B" | grep -o '"code":0' | head -1)" = '"code":0' ] && ok "收场 enable 恢复 enabled" || bad "收场恢复" "$B"

echo "---- 汇总: PASS=$PASS FAIL=$FAIL ----"
