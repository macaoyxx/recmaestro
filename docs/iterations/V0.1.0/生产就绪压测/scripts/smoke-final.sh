#!/usr/bin/env bash
# 终验 API 冒烟：登录 / 任务列表(分页) / 统计 / 审计 / 节点
# 用法：bash smoke-final.sh
set -u
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BASE=http://127.0.0.1:8080

echo "=== [1] 登录 ==="
tok=$(bash "$SCRIPT_DIR/login.sh")
if [ -z "$tok" ]; then echo "[1] FAIL: 未取到 token"; exit 1; fi
echo "[1] OK: 登录成功，token 长度 ${#tok}"

echo "=== [2] 任务列表（分页 total 核验，信封顶层 total）==="
r1=$(curl -s -X POST "$BASE/api/tasks/list" -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d '{"page":1,"page_size":10}')
echo "page1 原始输出（前 300 字符）: ${r1:0:300} ... 尾部: ${r1: -120}"
total=$(echo "$r1" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("total","MISSING"))')
n1=$(echo "$r1" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(len(d.get("data",[])))')
echo "page1: total=$total, 本页条数=$n1"

r2=$(curl -s -X POST "$BASE/api/tasks/list" -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d '{"page":2,"page_size":10}')
total2=$(echo "$r2" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("total","MISSING"))')
n2=$(echo "$r2" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(len(d.get("data",[])))')
echo "page2: total=$total2, 本页条数=$n2"

if [ "$total" = "$total2" ] && [ "$total" != "MISSING" ] && [ "$n1" = "10" ]; then
  echo "[2] OK: 分页 total 一致（$total），page_size=10 返回 $n1 条"
else
  echo "[2] WARN: total(page1)=$total total(page2)=$total2 n1=$n1 n2=$n2 —— 请人工核对"
fi

echo "=== [3] 统计 ==="
r3=$(curl -s "$BASE/api/stats" -H "Authorization: Bearer $tok")
echo "stats 原始输出: ${r3:0:500}"

echo "=== [4] 审计（admin，分页）==="
r4=$(curl -s -X POST "$BASE/api/audit-logs/list" -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d '{"page":1,"page_size":5}')
echo "audit 原始输出（前 300 字符）: ${r4:0:300}"
atotal=$(echo "$r4" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("total","MISSING"))')
an=$(echo "$r4" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(len(d.get("data",[])))')
echo "audit total=$atotal, 本页条数=$an"

echo "=== [5] 节点 ==="
r5=$(curl -s "$BASE/api/nodes" -H "Authorization: Bearer $tok")
echo "nodes 原始输出: ${r5:0:800}"
nodes=$(echo "$r5" | python3 -c 'import sys,json;d=json.load(sys.stdin);lst=d.get("data",[]);print(len(lst) if isinstance(lst,list) else "NOT_LIST")' 2>/dev/null)
echo "节点数=$nodes"
echo "=== 冒烟脚本执行完毕 ==="
