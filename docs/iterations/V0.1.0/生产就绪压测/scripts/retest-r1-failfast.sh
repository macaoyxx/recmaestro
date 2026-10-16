#!/usr/bin/env bash
# 集群扩展-1 修复复测补充：fail fast 分支定向验证（修复记录中标注的未验证项）
# 步骤：① 正确密码连打 12 次 /api/login（同一 IP 桶），记录状态码序列（预期前 10 放行后 429，
#       429 不计失败尝试、正确密码不锁号）；② 无 RM_TOKEN 调 cluster-create.sh，
#       预期 exit=2、stderr 透传 429、不发起 POST /api/tasks；③ 等 60s 窗口重置。
set -uo pipefail
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
pw=$(cd /mnt/c/Users/macao/.zcode/workspace/live/recmaestro/deploy && { [ -f .env ] && . ./.env 2>/dev/null; printf '%s' "${ADMIN_INITIAL_PASSWORD:-}"; })
[ -z "$pw" ] && pw=admin123

echo "=== [failfast-1] $(date '+%T') 同 IP 连打 12 次 /api/login（只记状态码）==="
for i in $(seq 1 12); do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST http://127.0.0.1:8080/api/login \
    -H 'Content-Type: application/json' -d "{\"username\":\"admin\",\"password\":\"$pw\"}")
  echo "第${i}次 http=$code"
done

echo "=== [failfast-2] 无 RM_TOKEN 调 cluster-create.sh（预期 fail fast，不发创建请求）==="
unset RM_TOKEN
set +e
out=$(bash "$S/cluster-create.sh" "【集群扩展R1-failfast-不应存在】" 60 2>/tmp/r1-failfast-stderr.txt)
rc=$?
set -e
echo "exit=$rc stdout=[$out]"
echo "stderr（透传内容，前 300 字符）："; head -c 300 /tmp/r1-failfast-stderr.txt; echo
echo "=== [failfast-3] 核验：该时刻前后无新增 POST /api/tasks ==="
docker logs --since 30s recmaestro-scheduler 2>&1 | grep -E 'POST.*/api/tasks' | tail -5 || echo "（无，符合预期：创建请求未发出）"
