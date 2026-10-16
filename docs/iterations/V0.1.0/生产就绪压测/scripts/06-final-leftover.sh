#!/usr/bin/env bash
# 终验：核对第二轮遗留 running 任务（压测R-42 / 压测R-78）当前状态
set -u
BASE=http://127.0.0.1:8080
S='/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts'
tok=$(bash "$S/login.sh")
[ -z "$tok" ] && { echo "[FAIL] 登录失败"; exit 1; }
for id in 2110647685064167424 2110647684950921223; do
  echo "--- 任务 $id ---"
  curl -s "$BASE/api/tasks/$id" -H "Authorization: Bearer $tok" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; print("name=%s status=%s worker=%s actual_end=%s" % (d.get("name"),d.get("status"),d.get("worker_id"),d.get("actual_end_time")))'
done
