#!/usr/bin/env bash
# 终验诊断：列出 .env 键名与 compose 引用的变量名（仅名字，绝不输出值）
cd /mnt/c/Users/macao/.zcode/workspace/live/recmaestro || exit 1
echo '--- deploy/.env 键名列表 ---'
cut -d= -f1 deploy/.env
echo '--- compose 引用的 env 变量名 ---'
grep -o '\${[A-Z_]*' deploy/docker-compose.yml | tr -d '${' | sort -u
