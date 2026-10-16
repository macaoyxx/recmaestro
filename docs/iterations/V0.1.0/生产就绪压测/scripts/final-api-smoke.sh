#!/usr/bin/env bash
# 终验 API 冒烟：登录 / 任务列表分页 / 统计 / 审计 / 节点
set -u
BASE=http://127.0.0.1:8080
S='/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts'

tok=$(bash "$S/login.sh")
if [ -z "$tok" ]; then echo "[FAIL] 登录失败（login.sh 无 token）"; exit 1; fi
echo "[1] 登录 OK（access_token 长度 ${#tok}）"

AUTH="Authorization: Bearer $tok"

echo "[2] 任务列表 POST /api/tasks/list {page:1,page_size:10}"
r1=$(curl -s -X POST $BASE/api/tasks/list -H "$AUTH" -H 'Content-Type: application/json' -d '{"page":1,"page_size":10}')
t1=$(echo "$r1" | grep -oP '"total":\s*\K[0-9]+' | head -1)
c1=$(echo "$r1" | grep -oP '"id":\s*"?[0-9]+' | grep -c '')
ids1=$(echo "$r1" | grep -oP '"id":\s*"?\K[0-9]+' | sort | tr '\n' ',' )
echo "    total=$t1 本页任务id数=$c1"
echo "    page1 ids: $ids1"

echo "[3] 任务列表 page=2（分页一致性）"
r2=$(curl -s -X POST $BASE/api/tasks/list -H "$AUTH" -H 'Content-Type: application/json' -d '{"page":2,"page_size":10}')
t2=$(echo "$r2" | grep -oP '"total":\s*\K[0-9]+' | head -1)
ids2=$(echo "$r2" | grep -oP '"id":\s*"?\K[0-9]+' | sort | tr '\n' ',')
echo "    total=$t2 page2 ids: $ids2"
ov=$(comm -12 <(echo "$ids1" | tr ',' '\n') <(echo "$ids2" | tr ',' '\n') | grep -c '' || true)
echo "    page1/page2 id 重叠数=$ov（应为 0）"

echo "[4] 统计 GET /api/stats"
s=$(curl -s -w '\nHTTP:%{http_code}' $BASE/api/stats -H "$AUTH")
echo "$s" | head -5

echo "[5] 审计 POST /api/audit-logs/list {page:1,page_size:5}"
a=$(curl -s -X POST $BASE/api/audit-logs/list -H "$AUTH" -H 'Content-Type: application/json' -d '{"page":1,"page_size":5}')
ta=$(echo "$a" | grep -oP '"total":\s*\K[0-9]+' | head -1)
ca=$(echo "$a" | grep -oP '"id":\s*"?\K[0-9]+' | grep -c '' || true)
echo "    audit total=$ta 本页条数=$ca"
echo "    摘要: $(echo "$a" | head -c 300)"

echo "[6] 节点 GET /api/nodes"
n=$(curl -s $BASE/api/nodes -H "$AUTH")
echo "$n" | head -c 600
echo
nn=$(echo "$n" | grep -oP '"node_id":\s*"\K[^"]+' | grep -c '' || true)
echo "    node 数=$nn（应为 3）"
