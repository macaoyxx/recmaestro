#!/usr/bin/env bash
# 集群扩展场景 第1轮复测 — 分阶段编排（每个 stage 幂等可独立执行，任务 id 持久化到 retest-r1-ids.env）
# 用法: retest-r1-cluster.sh <s1|s2|s3|s4|s5|s6|s7|s8|s9>
#   s1 缩容3→1+建R1-1 | s2 扩1→3+建R1-2/3 | s3 扩3→5+建R1-4..7+绑定快照
#   s4 停机注入(参数: 任务id 容器名) | s5 恢复续录+对账(参数: 任务id 容器名)
#   s6 心跳时延(参数: 容器名 IP) | s7 并发10任务(修复验证) | s8 并发终态+抽查对账 | s9 终态恢复3实例
set -euo pipefail
S="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts"
D="/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/deploy"
IDS="$S/retest-r1-ids.env"
API="http://127.0.0.1:8080"

upd_ids() { touch "$IDS"; if grep -q "^$1=" "$IDS"; then sed -i "s|^$1=.*|$1=$2|" "$IDS"; else echo "$1=$2" >> "$IDS"; fi; }
get_id() { printf '%s' "$1" | sed -n 's/.*"id":"\([0-9]*\)".*/\1/p'; }
snap() { # 一批任务的绑定快照：snap <标签> id...
  local tag=$1; shift
  echo "--- 绑定快照[$tag] $(date '+%T') ---"
  bash "$S/cluster-task.sh" "$@"
}
mk() { # 建任务并登记 id（env 文件 + 当前 shell 全局变量）：mk <序号> <秒数>
  local out id; out=$(bash "$S/cluster-create.sh" "【集群扩展R1-$1】" "$2"); echo "$out"
  id=$(get_id "$out"); upd_ids "R1_$1_ID" "$id"; declare -g "R1_$1_ID=$id"
}

case "${1:-}" in
s1)
  echo "=== [R1-S1] $(date '+%F %T') 缩容前活跃任务检查（关键词=集群扩展）==="
  tok=$(bash "$S/login.sh"); export RM_TOKEN=$tok
  curl -s -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
    -d '{"page":1,"page_size":100,"keyword":"集群扩展"}' "$API/api/tasks/list" \
    | python3 -c 'import json,sys; ts=json.load(sys.stdin)["data"]; a=[t for t in ts if t["status"] in ("pending","queued","running","recovering")]; print("集群扩展系列 active=",len(a)); [print(" ",t["id"],t["name"],t["status"]) for t in a]'
  echo "=== [R1-S1] 缩容 3→1 ==="
  cd "$D" && docker compose up -d --scale worker=1 2>&1 | grep -E 'Stopped|Removed|Running|Started' || true
  bash "$S/cluster-wait-nodes.sh" 1 180 | tail -2
  bash "$S/cluster-ip2worker.sh"
  echo "=== [R1-S1] 单节点建任务【集群扩展R1-1】830s（RM_TOKEN 复用）==="
  mk 1 830
  bash "$S/cluster-poll-task.sh" "$(grep '^R1_1_ID=' "$IDS" | cut -d= -f2)" running 120 | tail -2
  snap "1实例-认领" "$(grep '^R1_1_ID=' "$IDS" | cut -d= -f2)"
  ;;
s2)
  . "$IDS"
  echo "=== [R1-S2] $(date '+%T') 扩容 1→3 ==="
  cd "$D" && docker compose up -d --scale worker=3 2>&1 | grep -E 'Started|Running|Created' || true
  bash "$S/cluster-wait-nodes.sh" 3 60 | tail -2
  bash "$S/cluster-ip2worker.sh"
  tok=$(bash "$S/login.sh"); export RM_TOKEN=$tok
  echo "=== [R1-S2] 3 实例下建 R1-2/R1-3（各 700s，RM_TOKEN 复用）==="
  mk 2 700; mk 3 700
  echo "（认领轮询与绑定快照走 snap2 分支，幂等只读）"
  ;;
snap2)
  . "$IDS"
  bash "$S/cluster-poll-task.sh" "$R1_2_ID" running 120 | tail -1
  bash "$S/cluster-poll-task.sh" "$R1_3_ID" running 120 | tail -1
  snap "3实例" "$R1_1_ID" "$R1_2_ID" "$R1_3_ID"
  ;;
s3)
  . "$IDS"
  echo "=== [R1-S3] $(date '+%T') 扩容 3→5 ==="
  cd "$D" && docker compose up -d --scale worker=5 2>&1 | grep -E 'Started|Running|Created' || true
  bash "$S/cluster-wait-nodes.sh" 5 60 | tail -2
  bash "$S/cluster-ip2worker.sh"
  tok=$(bash "$S/login.sh"); export RM_TOKEN=$tok
  echo "=== [R1-S3] 5 实例下建 R1-4..R1-7（各 360s，RM_TOKEN 复用）==="
  mk 4 360; mk 5 360; mk 6 360; mk 7 360
  sleep 15
  snap "5实例" "$R1_1_ID" "$R1_2_ID" "$R1_3_ID" "$R1_4_ID" "$R1_5_ID" "$R1_6_ID" "$R1_7_ID"
  echo "--- 节点负载（判断认领分布）---"
  curl -s -H "Authorization: Bearer $tok" "$API/api/nodes" | python3 -c '
import json,sys
for n in json.load(sys.stdin)["data"]: print(n["node_id"],n["status"],"current_tasks=",n["current_tasks"])'
  ;;
s4) # 停机注入：s4 <任务id> <容器名>
  tid=${2:?}; c=${3:?}
  . "$IDS"
  echo "=== [R1-S4] $(date '+%T') 停机注入：docker stop $c（受试任务 $tid）==="
  snap "停机前" "$R1_1_ID" "$R1_2_ID" "$R1_3_ID"
  date '+%T.%3N stop 开始'; docker stop "$c"; date '+%T.%3N stop 完成'
  bash "$S/cluster-poll-task.sh" "$tid" recovering 300
  echo "--- recovering 后快照（断言 worker_id 仍指原节点）---"
  snap "recovering" "$tid"
  echo "--- 其他在线节点未抢（current_tasks 应不含受试任务迁移）---"
  tok=$(bash "$S/login.sh")
  curl -s -H "Authorization: Bearer $tok" "$API/api/nodes" | python3 -c '
import json,sys
for n in json.load(sys.stdin)["data"]: print(n["node_id"],n["status"],"current_tasks=",n["current_tasks"])'
  ;;
s5) # 恢复续录+对账：s5 <任务id> <容器名>
  tid=${2:?}; c=${3:?}
  echo "=== [R1-S5] $(date '+%T') docker start $c，断言 15s 内转 running ==="
  date '+%T.%3N start'; docker start "$c"; date '+%T.%3N start 完成'
  bash "$S/cluster-poll-task.sh" "$tid" running 60
  snap "恢复续录后" "$tid"
  echo "--- 等待受试任务 completed ---"
  bash "$S/cluster-poll-task.sh" "$tid" completed 420 | tail -2
  bash "$S/cluster-task-log.sh" "$tid"
  echo "--- 下载产物 ffprobe 复核 ---"
  tok=$(bash "$S/login.sh")
  curl -s -o /tmp/r1-cluster-受试.mp4 -w 'download http=%{http_code} size=%{size_download}\n' \
    -H "Authorization: Bearer $tok" "$API/api/tasks/$tid/download"
  ffprobe -v error -show_entries format=duration -of csv=p=0 /tmp/r1-cluster-受试.mp4
  ;;
s6) # 心跳时延：s6 <容器名> <IP>（要求该节点空闲）
  c=${2:?}; ip=${3:?}
  echo "=== [R1-S6] $(date '+%T') 心跳下线时延：docker stop $c（空闲节点）==="
  date '+%T.%3N stop'; docker stop "$c" >/dev/null; date '+%T.%3N stop 完成'
  bash "$S/cluster-poll-node.sh" "$ip" vanish 180 2
  echo "=== 重新上线时延：docker start $c ==="
  date '+%T.%3N start'; docker start "$c" >/dev/null; date '+%T.%3N start 完成'
  bash "$S/cluster-poll-node.sh" "$ip" appear 60 2
  ;;
s7) # 并发 10 任务（Issue 集群扩展-1 修复验证原路径）
  echo "=== [R1-S7] $(date '+%T') 5 实例并发 10 任务（KEYWORD=集群扩展R1-并发）==="
  t0=$(date '+%F %T')
  echo "window_start=$t0" | tee /tmp/r1-login-window.txt
  KEYWORD=集群扩展R1-并发 bash "$S/cluster-concurrency.sh" 10 180
  t1=$(date '+%F %T')
  echo "window_end=$t1" | tee -a /tmp/r1-login-window.txt
  echo "--- 批量期间 scheduler /api/login 次数（应≤2：整批 1 次+统计段 1 次）---"
  docker logs recmaestro-scheduler 2>&1 | awk -v s="$t0" -v e="$t1" '$0>=s && $0<=e' | grep -c '/api/login' || true
  docker logs recmaestro-scheduler 2>&1 | awk -v s="$t0" -v e="$t1" '$0>=s && $0<=e' | grep '/api/login' | tail -5 || true
  ;;
s8) # 并发终态 + 抽查对账 + 长任务终态
  . "$IDS"
  echo "=== [R1-S8] $(date '+%T') 等并发任务全部终态 ==="
  bash "$S/cluster-wait-all.sh" 集群扩展R1-并发 420
  echo "--- 抽查对账（并发-1/5/10 跨节点）---"
  tok=$(bash "$S/login.sh")
  ids=$(curl -s -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
    -d '{"page":1,"page_size":100,"keyword":"集群扩展R1-并发"}' "$API/api/tasks/list" \
    | python3 -c '
import json,sys
ts=json.load(sys.stdin)["data"]
for t in ts:
    if t["name"].endswith(("-1】","-5】","-10】")): print(t["id"])')
  for id in $ids; do bash "$S/cluster-task-log.sh" "$id" | grep -E '对账|completed' | tail -2; done
  echo "--- 并发-1 下载 ffprobe 复核 ---"
  first=$(printf '%s\n' $ids | head -1)
  curl -s -o /tmp/r1-conc-1.mp4 -w 'download http=%{http_code} size=%{size_download}\n' \
    -H "Authorization: Bearer $tok" "$API/api/tasks/$first/download"
  ffprobe -v error -show_entries format=duration -of csv=p=0 /tmp/r1-conc-1.mp4
  echo "--- 长任务 R1-1/2/3 终态与对账 ---"
  snap "终态" "$R1_1_ID" "$R1_2_ID" "$R1_3_ID"
  for id in "$R1_1_ID" "$R1_2_ID" "$R1_3_ID"; do bash "$S/cluster-poll-task.sh" "$id" completed 600 | tail -1; bash "$S/cluster-task-log.sh" "$id" | grep 对账 | tail -1; done
  ;;
s9)
  echo "=== [R1-S9] $(date '+%T') 终态恢复 3 实例 + 收场核验 ==="
  bash "$S/cluster-final.sh"
  bash "$S/ensure-stream.sh" | tail -2
  ;;
*) echo "用法: $0 <s1..s9>"; exit 2;;
esac
