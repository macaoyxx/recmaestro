# RecMaestro 部署说明

直播录制调度平台 + worker 的 Docker Compose 部署。

## 前提

- Docker 20+ 与 Docker Compose v2
- 构建机器能访问 Docker Hub（拉取基础镜像）

## 一键启动

```bash
cd deploy
cp .env.example .env        # 按需修改密码/密钥
docker compose up -d --build
```

启动后：

- 前端 UI：<http://127.0.0.1:8080>（首次登录用 `ADMIN_INITIAL_PASSWORD` 的 admin）
- API 文档（Swagger）：<http://127.0.0.1:8080/swagger/index.html>
- Prometheus 指标：scheduler `:8080/metrics`、worker `:8081/metrics`

## 服务构成

| 服务 | 端口 | 说明 |
|------|------|------|
| scheduler | 8080 | 调度器 + 内嵌前端 + Swagger |
| worker | 8081 | 录制执行器（内置 ffmpeg） |
| mysql | 3306 | 持久化（任务/用户/日志/节点） |
| redis | 6379 | 队列 / 心跳 / 锁 |
| consul | 8500 | 服务注册（可选 ACL） |

## 关键环境变量

见 `.env.example`。MySQL/Redis/Consul 的地址通过 `MYSQL_HOST`/`REDIS_ADDR`/`CONSUL_ADDR` 覆盖（容器内默认指向服务名）。

- `INTERNAL_API_TOKEN`（必填）：worker 内部接口共享密钥，scheduler 与 worker 必须一致；未设置时两角色启动直接报错（内部接口绝不裸奔）。

## 生产化建议

- MySQL / Redis / Consul 建议使用生产级高可用（主从 / 哨兵 / 集群），本 compose 为单机演示。
- worker 水平扩展：`docker compose up -d --scale worker=3`。NODE_ID 默认取 `recmaestro-worker-<容器IP>-<端口>`，天然唯一，无需手工配置（多机部署时才需显式指定全局唯一的 `NODE_ID` 与可路由的 `WORKER_ADDR`）。
- 录制产物默认在 worker 容器的 `/records`（挂载 `worker_records` 卷）。
- 生产务必修改 `JWT_SECRET`、`INTERNAL_API_TOKEN`、`ADMIN_INITIAL_PASSWORD`、MySQL/Redis 密码。

## 多 worker 运维（单实例启停与宕机恢复）

scale 出来的多个 worker 同属一个 compose 服务，**`docker compose stop worker` 会停掉全部实例**；对单个实例操作要用容器级命令（容器名形如 `deploy-worker-1/2/3`）：

```bash
docker ps --format '{{.Names}}' | grep worker    # 列出各 worker 容器
docker stop deploy-worker-2                      # 停单个实例（SIGTERM 优雅停机，等待在录任务收尾段文件）
docker start deploy-worker-2                     # 恢复该实例
```

任务与节点绑定（不跨节点迁移）：实例停止后其任务约 `recover_window`（默认 3 分钟）转 `recovering`，原实例 `docker start` 回来后续录；长期不恢复则超时 `interrupted` 并自动排重试。

按任务定位所在容器：`GET /api/nodes`（或任务详情的 `worker_id`，形如 `recmaestro-worker-<IP>-8080`）拿到 IP，再与 `docker inspect <容器名> -f '{{.NetworkSettings.Networks}}'` 的 IP 比对。

## 与 SRS 对接

本平台与 `single-node/` 的 SRS 解耦。录制内部 SRS 流时，直播地址直接填 SRS 的 RTMP 拉流地址（如 `rtmp://<srs-host>:11935/live/<stream>?vhost=live`）。
