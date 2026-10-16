#!/usr/bin/env bash
# api-smoke.sh —— 【单机基线】API 全链路冒烟
# 覆盖：登录（错密码 401 / 不存在用户 401 / 连续失败锁号 403 / IP 限流 429 与恢复）、
#       /me、任务创建/列表分页/详情/编辑/取消、统计、节点、审计分页、用户列表、
#       401/403/404/409/400 失败信封形状。
# 登录窗口计数设计（IP 限流每分钟 10 次，login_ratelimit.go）：
#   窗口1: login.sh(1) + admin错(2) + 不存在用户(3) + lock-u1错×5(4..8) + 锁后正确密码(9) = 9 次（≤10）
#   sleep62 → 窗口2: lock-u1 恢复登录(1) + 不存在用户×9(2..10) + 第 11 次 → 429
#   sleep62 → 窗口3: login.sh 恢复 200
set -u
S_DIR=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
API=http://127.0.0.1:8080
PASS=0; FAIL=0
T0=$(date +%s)
ts() { printf '[%5ds]' $(( $(date +%s) - T0 )); }
ok()  { PASS=$((PASS+1)); echo "$(ts) PASS $1"; }
bad() { FAIL=$((FAIL+1)); echo "$(ts) FAIL $1 | $2"; }

# pj '<python 表达式，d=JSON 对象>'：stdin 传 body，输出求值结果（出错输出 PYERR）
pj() { python3 -c '
import sys, json
raw = sys.stdin.read()
try:
    d = json.loads(raw)
    v = eval(sys.argv[1])
    print("None" if v is None else v)
except Exception:
    print("PYERR")
' "$1"; }

# req <method> <path> [curl 参数...] → stdout: body\nhttp状态码
req() {
  local m=$1 p=$2; shift 2
  curl -s --max-time 15 -X "$m" "$API$p" -w '\n%{http_code}' "$@"
}
# split "<req 输出>" → B=body H=http状态 C=信封code M=信封msg（每次调用全部刷新，无残留）
split() {
  B=$(printf '%s' "$1" | sed '$d'); H=$(printf '%s' "$1" | tail -1)
  C=$(printf '%s' "$B" | pj 'd["code"]'); M=$(printf '%s' "$B" | pj 'd["msg"]')
}

ENV_TASK_ID=2110556939766206464   # 【环境-1】已 completed，用作 409/终态素材

echo "===== Phase 1：登录 / me / 错密码 / 锁号（窗口1，登录共 9 次）====="
tok=$(bash "$S_DIR/login.sh") && [ -n "$tok" ] && ok "登录 login.sh 返回 access_token" || bad "登录 login.sh" "无 token"
AUTH=(-H "Authorization: Bearer $tok" -H 'Content-Type: application/json')

split "$(req GET /api/me "${AUTH[@]}")"
u=$(printf '%s' "$B" | pj 'd["data"]["username"]'); role=$(printf '%s' "$B" | pj 'd["data"]["role"]')
{ [ "$H" = 200 ] && [ "$C" = 0 ] && [ "$u" = admin ] && [ "$role" = admin ]; } \
  && ok "/me 200 信封 code=0 data.username=admin role=admin" \
  || bad "/me" "http=$H code=$C user=$u role=$role body=$(printf '%s' "$B" | head -c 200)"

split "$(req POST /api/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"definitely-wrong-pw"}')"
{ [ "$H" = 401 ] && [ "$C" = 401 ] && [ "$M" = "用户名或密码错误" ]; } \
  && ok "错密码 401 信封 code=401 msg=用户名或密码错误" || bad "错密码 401" "http=$H code=$C msg=$M"

split "$(req POST /api/login -H 'Content-Type: application/json' -d '{"username":"no-such-user-xyz","password":"whatever"}')"
{ [ "$H" = 401 ] && [ "$C" = 401 ] && [ "$M" = "用户名或密码错误" ]; } \
  && ok "不存在用户名 401 与错密码同文案（防枚举）" || bad "不存在用户名 401" "http=$H code=$C msg=$M"

split "$(req POST /api/users "${AUTH[@]}" -d '{"username":"baseline-lock-u1","password":"Base#Lock2026","role":"user"}')"
uid=$(printf '%s' "$B" | pj 'd["data"]["id"]')
{ [ "$H" = 201 ] && [ "$C" = 0 ]; } \
  && ok "创建用户 baseline-lock-u1（user 角色）201（id=$uid）" || bad "创建用户" "http=$H code=$C msg=$M body=$(printf '%s' "$B" | head -c 200)"

for i in 1 2 3 4 5; do
  split "$(req POST /api/login -H 'Content-Type: application/json' -d '{"username":"baseline-lock-u1","password":"wrong-pw-'$i'"}')"
  if [ "$i" -lt 5 ]; then
    { [ "$H" = 401 ] && [ "$C" = 401 ]; } && ok "第 $i 次错密码 401（计数 $i/5）" || bad "第 $i 次错密码" "http=$H code=$C msg=$M"
  else
    { [ "$H" = 403 ] && [ "$C" = 403 ]; } && ok "第 5 次错密码触发锁号 403（msg=$M）" || bad "第 5 次锁号" "http=$H code=$C msg=$M"
  fi
done
split "$(req POST /api/login -H 'Content-Type: application/json' -d '{"username":"baseline-lock-u1","password":"Base#Lock2026"}')"
{ [ "$H" = 403 ] && [ "$C" = 403 ] && [ "$M" = "账号已锁定，请联系管理员" ]; } \
  && ok "锁定后正确密码登录被拒 403（msg=账号已锁定，请联系管理员）" || bad "锁定后登录" "http=$H code=$C msg=$M"
split "$(req PATCH "/api/users/$uid/status" "${AUTH[@]}" -d '{"status":"enabled"}')"
{ [ "$H" = 200 ] && [ "$C" = 0 ]; } && ok "管理员 enable 解锁 baseline-lock-u1 200" || bad "enable 解锁" "http=$H code=$C msg=$M uid=$uid body=$(printf '%s' "$B" | head -c 200)"

echo "===== Phase 2：失败信封 / 任务 CRUD / 分页 / 统计 / 审计 / 用户（不消耗登录窗口）====="
split "$(req GET /api/me)"
{ [ "$H" = 401 ] && [ "$C" = 401 ]; } && ok "无 token 401 信封（msg=$M）" || bad "无 token 401" "http=$H code=$C body=$(printf '%s' "$B" | head -c 150)"
split "$(req GET /api/me -H "Authorization: Bearer not-a-jwt")"
{ [ "$H" = 401 ] && [ "$C" = 401 ]; } && ok "坏 token 401 信封（msg=$M）" || bad "坏 token 401" "http=$H code=$C body=$(printf '%s' "$B" | head -c 150)"

split "$(req GET /api/tasks/9999999999999999999 "${AUTH[@]}")"
{ [ "$H" = 404 ] && [ "$C" = 404 ] && [ "$M" = "任务不存在" ]; } \
  && ok "任务详情不存在 404 信封（msg=任务不存在）" || bad "404" "http=$H code=$C msg=$M"

split "$(req POST /api/tasks "${AUTH[@]}" -d '{"name":"no-url"}')"
{ [ "$H" = 400 ] && [ "$C" = 400 ] && [ "$M" = "请求参数错误" ]; } \
  && ok "创建任务缺 stream_url 400 信封（msg=请求参数错误）" || bad "400" "http=$H code=$C msg=$M"

split "$(req POST /api/tasks "${AUTH[@]}" -d "{\"id\":\"$ENV_TASK_ID\",\"stream_url\":\"rtmp://x/live/y\",\"end_time\":\"2027-01-01T00:00:00+08:00\"}")"
{ [ "$H" = 409 ] && [ "$C" = 409 ] && [ "$M" = "任务 ID 已存在" ]; } \
  && ok "重复任务 ID 409 信封（msg=任务 ID 已存在）" || bad "409 重复 ID" "http=$H code=$C msg=$M"
split "$(req POST "/api/tasks/$ENV_TASK_ID/cancel" "${AUTH[@]}")"
{ [ "$H" = 409 ] && [ "$C" = 409 ] && [ "$M" = "任务当前状态不可取消" ]; } \
  && ok "completed 任务取消 409 信封（msg=任务当前状态不可取消）" || bad "409 不可取消" "http=$H code=$C msg=$M"

# 任务创建（定时 → pending）→ 详情 → 编辑 → 取消 → 再取消 409
ST=$(date -d '+2 hours' +%Y-%m-%dT%H:%M:%S%:z)
ET=$(date -d '+3 hours' +%Y-%m-%dT%H:%M:%S%:z)
split "$(req POST /api/tasks "${AUTH[@]}" -d "{\"name\":\"【单机基线-3】\",\"stream_url\":\"rtmp://recmaestro-srs-e2e:1935/live/e2e\",\"start_time\":\"$ST\",\"end_time\":\"$ET\"}")"
T3=$(printf '%s' "$B" | pj 'd["data"]["id"]'); s=$(printf '%s' "$B" | pj 'd["data"]["status"]')
{ [ "$H" = 201 ] && [ "$C" = 0 ] && [ "$s" = "pending" ]; } \
  && ok "创建定时任务【单机基线-3】201 初始 status=pending（id=$T3）" || bad "创建定时任务" "http=$H status=$s body=$(printf '%s' "$B" | head -c 200)"

split "$(req GET "/api/tasks/$T3" "${AUTH[@]}")"
n=$(printf '%s' "$B" | pj 'd["data"]["name"]'); s=$(printf '%s' "$B" | pj 'd["data"]["status"]')
{ [ "$H" = 200 ] && [ "$C" = 0 ] && [ "$n" = "【单机基线-3】" ] && [ "$s" = "pending" ]; } \
  && ok "任务详情回读 name/status 正确" || bad "任务详情" "name=$n status=$s"

split "$(req PUT "/api/tasks/$T3" "${AUTH[@]}" -d "{\"name\":\"【单机基线-3-已改名】\",\"stream_url\":\"rtmp://recmaestro-srs-e2e:1935/live/e2e\",\"start_time\":\"$ST\",\"end_time\":\"$ET\"}")"
{ [ "$H" = 200 ] && [ "$C" = 0 ]; } && ok "编辑任务改名 200" || bad "编辑任务" "http=$H code=$C msg=$M body=$(printf '%s' "$B" | head -c 200)"
split "$(req GET "/api/tasks/$T3" "${AUTH[@]}")"
n=$(printf '%s' "$B" | pj 'd["data"]["name"]')
[ "$n" = "【单机基线-3-已改名】" ] && ok "编辑后详情回读新名（$n）" || bad "编辑回读" "name=$n"

split "$(req POST "/api/tasks/$T3/cancel" "${AUTH[@]}")"
s=$(printf '%s' "$B" | pj 'd["data"]["status"]')
{ [ "$H" = 200 ] && [ "$s" = "cancelled" ]; } && ok "取消任务 200 status=cancelled" || bad "取消任务" "http=$H status=$s msg=$M"
split "$(req POST "/api/tasks/$T3/cancel" "${AUTH[@]}")"
{ [ "$H" = 409 ] && [ "$C" = 409 ]; } && ok "重复取消 409 信封（msg=$M）" || bad "重复取消" "http=$H code=$C msg=$M"

# 任务列表分页：page_size=1 两页 id 不同 + total 一致 + 信封形状
split "$(req POST /api/tasks/list "${AUTH[@]}" -d '{"page":1,"page_size":1}')"
t1=$(printf '%s' "$B" | pj 'd["total"]'); a=$(printf '%s' "$B" | pj 'd["data"][0]["id"]')
msg=$(printf '%s' "$B" | pj 'd["msg"]'); ln=$(printf '%s' "$B" | pj 'len(d["data"])')
r2=$(req POST /api/tasks/list "${AUTH[@]}" -d '{"page":2,"page_size":1}')
r2b=$(printf '%s' "$r2" | sed '$d')
t2=$(printf '%s' "$r2b" | pj 'd["total"]'); b=$(printf '%s' "$r2b" | pj 'd["data"][0]["id"]'); ln2=$(printf '%s' "$r2b" | pj 'len(d["data"])')
{ [ "$C" = 0 ] && [ "$msg" = ok ] && [ "$ln" = 1 ] && [ "$ln2" = 1 ] && [ "$a" != "$b" ] && [ "$t1" = "$t2" ] && [ "$t1" -ge 3 ]; } \
  && ok "任务列表分页：code=0 msg=ok len=1 两页 id 不同（$a vs $b）total=$t1 两页一致" \
  || bad "任务列表分页" "code=$C msg=$msg ln=$ln/$ln2 a=$a b=$b t1=$t1 t2=$t2"

split "$(req GET /api/stats "${AUTH[@]}")"
keys=$(printf '%s' "$B" | pj 'sorted(d["data"].keys())')
{ [ "$H" = 200 ] && [ "$C" = 0 ] && [ "$keys" = "['nodes_online', 'nodes_total', 'queue_size', 'scheduled_size', 'tasks_by_status']" ]; } \
  && ok "/stats 200 data 五键齐全（$keys）" || bad "/stats" "http=$H code=$C keys=$keys"

split "$(req POST /api/audit-logs/list "${AUTH[@]}" -d '{"page":1,"page_size":2}')"
t=$(printf '%s' "$B" | pj 'd["total"]'); ln=$(printf '%s' "$B" | pj 'len(d["data"])')
{ [ "$H" = 200 ] && [ "$C" = 0 ] && [ "$ln" = 2 ] && [ "$t" -ge 2 ]; } \
  && ok "审计分页 200 len=2 total=$t（信封带 total）" || bad "审计分页" "http=$H len=$ln total=$t msg=$M"

split "$(req GET /api/users "${AUTH[@]}")"
names=$(printf '%s' "$B" | pj '[x["username"] for x in d["data"]]')
{ [ "$H" = 200 ] && [ "$C" = 0 ] && printf '%s' "$names" | grep -q admin && printf '%s' "$names" | grep -q baseline-lock-u1; } \
  && ok "用户列表 200 含 admin 与 baseline-lock-u1" || bad "用户列表" "http=$H names=$names"

echo "===== Phase 3：等 IP 限流窗口重置（62s）→ 解锁恢复登录 + 403 + 429 ====="
sleep 62
split "$(req POST /api/login -H 'Content-Type: application/json' -d '{"username":"baseline-lock-u1","password":"Base#Lock2026"}')"
utok=$(printf '%s' "$B" | pj 'd["data"]["access_token"]')
{ [ "$H" = 200 ] && [ -n "$utok" ] && [ "$utok" != "None" ] && [ "$utok" != "PYERR" ]; } \
  && ok "解锁后正确密码登录 200（enable 已清零失败计数）" || bad "解锁后登录" "http=$H body=$(printf '%s' "$B" | head -c 200)"

split "$(req GET /api/users -H "Authorization: Bearer $utok")"
{ [ "$H" = 403 ] && [ "$C" = 403 ] && [ "$M" = "无权限执行该操作" ]; } \
  && ok "user 角色访问 GET /api/users 403 信封（msg=无权限执行该操作）" || bad "403 越权" "http=$H code=$C msg=$M"

# 打满 IP 限流：本窗口已用 1（上面登录），再打 9 次 401（#2~#10），第 11 次（#11）应 429
for i in $(seq 1 9); do
  split "$(req POST /api/login -H 'Content-Type: application/json' -d '{"username":"no-such-user-xyz","password":"x"}')"
  [ "$H" = 401 ] || bad "限流预热第 $i 次" "期望 401 实际 http=$H body=$(printf '%s' "$B" | head -c 120)"
done
ok "限流预热 9 次 401（窗口内共 10 次尝试，均未超限）"
raw=$(curl -s --max-time 15 -X POST "$API/api/login" -H 'Content-Type: application/json' -d '{"username":"no-such-user-xyz","password":"x"}' -D /tmp/hdr429.txt -w '\n%{http_code}')
split "$raw"
ra=$(grep -i '^retry-after:' /tmp/hdr429.txt | tr -d '\r' | awk '{print $2}')
{ [ "$H" = 429 ] && [ "$C" = 429 ] && [ "$ra" = 60 ]; } \
  && ok "第 11 次登录 429 信封（msg=$M，Retry-After=$ra）" || bad "429 限流" "http=$H code=$C retry-after=$ra msg=$M"
split "$(req POST /api/login -H 'Content-Type: application/json' -d '{"username":"no-such-user-xyz","password":"x"}')"
[ "$H" = 429 ] && ok "超限持续：紧接着第 12 次仍 429" || bad "429 持续" "http=$H code=$C"

echo "===== Phase 4：等 62s 验证限流恢复 + 节点断言 ====="
sleep 62
tok2=$(bash "$S_DIR/login.sh") && [ -n "$tok2" ] && ok "62s 后登录恢复 200（IP 限流窗口自动重置）" || bad "限流恢复" "login.sh 失败"
split "$(req GET /api/nodes -H "Authorization: Bearer $tok2")"
cnt=$(printf '%s' "$B" | pj 'len(d["data"])'); onl=$(printf '%s' "$B" | pj 'sum(1 for x in d["data"] if x["status"]=="online")'); ids=$(printf '%s' "$B" | pj '[x["node_id"] for x in d["data"]]')
{ [ "$H" = 200 ] && [ "$C" = 0 ] && [ "$cnt" = 1 ] && [ "$onl" = 1 ]; } \
  && ok "节点列表 200：缩容后仅 1 节点且 online（$ids）" || bad "节点列表" "http=$H cnt=$cnt online=$onl ids=$ids"

echo "===== 汇总：PASS=$PASS FAIL=$FAIL ====="
[ "$FAIL" = 0 ] && echo "API-SMOKE: ALL PASS" || echo "API-SMOKE: HAS FAILURES"
exit 0
