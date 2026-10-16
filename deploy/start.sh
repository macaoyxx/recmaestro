#!/bin/bash
# 一键启动 RecMaestro（构建镜像并后台启动）
set -e
cd "$(dirname "$0")/.."
if [ ! -f deploy/.env ]; then
  cp deploy/.env.example deploy/.env
  echo "已生成 deploy/.env，请修改密码后重新运行"
fi
docker compose -f deploy/docker-compose.yml up -d --build
echo "RecMaestro 已启动: http://127.0.0.1:8080  (Swagger: /swagger/index.html)"
