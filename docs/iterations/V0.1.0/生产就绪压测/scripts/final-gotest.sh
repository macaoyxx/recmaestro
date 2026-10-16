#!/usr/bin/env bash
# 终验：全量单测门禁 go test ./...
# 密码来源：docker compose config 解析出的 MYSQL_ROOT_PASSWORD（shell 内传递，绝不回显）
cd /mnt/c/Users/macao/.zcode/workspace/live/recmaestro || exit 1

pw=$(cd deploy && docker compose config 2>/dev/null | grep -oP 'MYSQL_ROOT_PASSWORD[=:]\s*\K\S+' | head -1 | tr -d '"')
if [ -z "$pw" ]; then
  echo "[diag] compose 未解析出 MYSQL_ROOT_PASSWORD，改用测试约定缺省 root123456"
  pw=root123456
fi

# 验证密码可用性（只输出结论，不输出值）
if docker exec recmaestro-mysql mysql -uroot -p"$pw" -e 'select 1' >/dev/null 2>&1; then
  echo "[diag] root 密码验证通过"
else
  echo "[diag] root 密码验证失败（Access denied?）"
  exit 2
fi

export MYSQL_PASSWORD="$pw" GOPROXY=https://goproxy.cn,direct
go test ./... > /tmp/final-gotest.log 2>&1
ec=$?
echo "=== 各包结果（ok/FAIL 行）==="
grep -E '^(ok|FAIL|\?)' /tmp/final-gotest.log
echo "=== 失败详情（若有）==="
grep -A2 '^--- FAIL' /tmp/final-gotest.log | head -40
echo "=== EXIT=$ec ==="
