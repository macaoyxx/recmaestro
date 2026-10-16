# RecMaestro — 工作区说明

直播录制调度平台：单 Go 二进制以 `--role=scheduler|worker` 区分角色，共用一套代码与数据模型。scheduler 提供 Web UI（内嵌 Vue3 SPA）+ REST API + 任务调度；worker 认领任务后用 ffmpeg 拉流录制（TS 分段），结束后无损合并转 MP4。总设计基准是 `docs/architecture.md`，实现若偏离需先修订该文档。

## 常用命令

```bash
go build ./...                  # 编译（module 名 recmaestro，Go 1.26）
go test ./...                   # 全部测试（含集成用例：需 MYSQL_PASSWORD 环境变量指向测试库 MySQL）
go vet ./...
go run ./cmd/recmaestro --role=scheduler --env=local   # env: dev|test|local|prod

# 竞争检测只能在 WSL 跑（Windows 侧无 gcc，路径按本机 WSL 挂载换算）：
wsl -e sh -c "cd /mnt/c/Users/macao/.zcode/workspace/live/recmaestro && GOPROXY=https://goproxy.cn,direct MYSQL_PASSWORD=<密码> go test ./... -race"

# 前端（Vue3 + Vite + Element Plus，在 web/）
cd web && npm run dev           # dev server，/api 代理到 127.0.0.1:8080
cd web && npm run build         # 产物输出到 ../internal/web/dist（go:embed 内嵌）

# Swagger 文档再生成（改了 handler 注释后执行，从仓库根目录）
swag init -g cmd/recmaestro/main.go

# 部署（单机 Docker Compose：mysql/redis/consul/scheduler/worker；docker 在 WSL 内，Windows 侧无 docker）
cd deploy && cp .env.example .env && docker compose up -d --build
```

## 目录结构

- `cmd/recmaestro/` — 统一入口，--role / --env 分发
- `internal/scheduler/` — 调度器：`handler_*.go`（API）、dispatcher（Redis zset 定时 + 队列分发）、metrics_collector
- `internal/worker/` — worker：认领循环、心跳、`internal_api.go`（供 scheduler 回调的内部接口）
- `internal/recorder/` — ffmpeg 录制内核；`internal/storage/` — worker 本地文件/日志操作
- `internal/store/` — GORM 数据访问；`internal/migrate/migrations/` — SQL 迁移（embed，启动自动执行）
- `internal/auth/` — JWT；`internal/config/` — Viper 多环境配置；`internal/metrics/` — Prometheus 指标
- `internal/web/` — go:embed 前端产物；`web/` — 前端源码
- `config/` — `config.yaml`（公共默认）+ `config.<env>.yaml` 环境覆盖
- `docs/` — architecture.md、iterations/（迭代归档）、生成的 swagger；`deploy/` — Dockerfile + compose + grafana
- `.memory/` — 跨会话项目知识（导航见 README.md），改录制/合并/恢复等敏感区域前先读

## 架构铁律（改动前先读 docs/architecture.md）

1. **scheduler 不碰文件系统**：列出/合并/删除/下载文件一律经持有该文件的 worker 的 HTTP 内部接口完成（路由走 Redis `GetWorkerAddr(node_id)`，不经 Consul）。
2. **任务绑定节点**：任务由认领它的 worker 全程录制，宕机后等原节点恢复续录，不转移到其他节点；不存在跨节点文件传输。
3. **真相源**：任务状态以 MySQL 为准；节点存活由 Redis 心跳租约判断（registry.engine=consul 时再加 Consul 健康检查）。
4. 启动即自动 `migrate.Up()` 建表；新迁移放 `internal/migrate/migrations/NNNN_name.{up,down}.sql`；表名统一前缀 `live_rec_`。
5. 服务注册引擎可配置（`registry.engine`：redis 默认 / consul 可选），代码不得写死单一引擎。

## 约定与坑

- 注释、日志、错误信息全部用中文；每个 internal 包配 doc.go 包注释。
- 敏感信息只走环境变量（`MYSQL_PASSWORD`、`REDIS_PASSWORD`、`JWT_SECRET`、`CONSUL_TOKEN`、`ADMIN_INITIAL_PASSWORD`），严禁写进 YAML 或代码。scheduler 启动必须设 `JWT_SECRET`；首次启动必须设 `ADMIN_INITIAL_PASSWORD`（创建初始 admin）。
- 改前端后必须重新 `npm run build`（产物进 `internal/web/dist`）再编 Go 二进制，SPA 变更才会生效；SPA 由 scheduler 的 NoRoute 兜底托管，勿改回 StaticFS（catch-all 会与 /api 冲突）。
- swag CLI 与 swaggo/swag 库版本不匹配会生成不兼容字段（LeftDelim/RightDelim），保持 v1.16.6。
- 测试纪律：Redis 相关测试一律用 miniredis（go.mod 已备），严禁连真实 6379（上面跑着待部署栈）；worker 包测试用独立库 `live_recorder_worker_test`（与 scheduler 包共库会被其 RebuildQueues 捞走任务）。
- 改录制/合并/宕机恢复代码前先读 `.memory/TS段合并与宕机恢复防线.md`：合并链路有三层防线（优雅停机/预检隔离/时长对账），ffmpeg concat 遇损坏段会以退出码 0 静默丢内容，不能只看退出码。
- 端到端实测（推流源搭建、API 全链路、宕机注入、验收口径）套路见 `.memory/E2E实测套路.md`。
- 迭代文档在 `docs/iterations/<V0.0.x>/`，导航与版本规则见 `docs/iterations/README.md`；当前进行到 V0.0.8（部署联调，发版测试已完成：六项门禁全过、P-2/P-1/P-3 已修复复验，详见 V0.0.8/发版测试及修复报告.md）。每个迭代的实现须用户下令才开始。
