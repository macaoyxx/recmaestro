# V0.0.1 — 项目骨架

- 状态：🟡 进行中
- 起止：2026-09-21 ~ —
- 相关提交：（暂无，待实现）

## 目标（为什么做）

搭建一个可编译、可启动、可多环境配置、可自动建表的基础骨架，为后续所有迭代打地基。没有这一步，认证、调度、录制都无法落地。

## 范围（做了什么，清单）

- 初始化 Go 项目：`go.mod`、`cmd/recmaestro/main.go` 统一入口。
- 目录分层：`internal/` 下划分 config / scheduler / worker / recorder / storage / store / migrate / auth / metrics 等包骨架。
- **Viper 多环境配置**：`config/config.yaml`（公共默认 + 注释）+ `config/config.dev.yaml` / `config.test.yaml` / `config.local.yaml` / `config.prod.yaml`，`--env` 选择环境，敏感信息走环境变量。
- **golang-migrate 迁移**：`internal/migrate` 内嵌 SQL，建 `live_rec_` 前缀全量表（users/tasks/task_files/task_logs/nodes/audit_logs），启动自动 `migrate.Up()`。
- **`--role=scheduler|worker` 启动分发**：两种角色当前为占位实现，能启动、打印角色、挂 `/healthz` 健康检查。
- **Docker Compose 基础设施**：mysql、redis、consul（含可选 ACL 配置）编排，供本地开发/联调。
- 配置读取 MySQL / Redis / Consul 连接信息并建立连接（连通性自检）。

## 影响面

全新项目，无上游影响。与 `single-node/` 的 SRS 完全解耦。

## 验证标准

- `go build ./...`、`go vet ./...` 通过。
- `--role=scheduler` 与 `--role=worker` 均能启动，`/healthz` 返回 200。
- 启动后自动执行迁移，`live_rec_*` 全量表已建立（`schema_migrations` 有版本记录）。
- 切换 `--env=dev|test|local|prod` 配置正确加载并合并覆盖。
- Docker Compose 能拉起 mysql/redis/consul，服务能连通三者。

## 结果

- 项目骨架完成：`go.mod`（module `recmaestro`）、`cmd/recmaestro` 统一入口（`--role` 分发）、`internal` 分层（config/scheduler/worker/recorder/storage/store/migrate/auth/metrics）、多环境配置（Viper）、golang-migrate 迁移（live_rec_ 6 张表）、Docker Compose 基础设施。
- 验证通过：
  - `go build ./...`、`go vet ./...` 通过。
  - 配置单元测试 4 项通过（多环境加载、环境变量覆盖敏感项）。
  - `--role=scheduler` 与 `--role=worker` 均能启动，`/healthz` 返回 200。
  - 启动自动迁移建表：`live_rec_users/tasks/task_files/task_logs/nodes/audit_logs` + `schema_migrations`。
  - Redis / Consul 连通性自检正常。
  - Docker Compose 拉起 mysql / redis / consul 正常。
