# RecMaestro — 直播录制调度平台

单 Go 二进制、双角色（`--role=scheduler|worker`）的直播录制调度平台：scheduler 提供 Web UI 与 REST API，负责任务调度与编排；worker 用 ffmpeg 拉流录制（TS 分段），结束后无损合并转 MP4。面向几千～几万路并发录制设计，可水平扩展 worker。

## 功能特性

- **任务调度**：定时/立即开始、到点自动触发（Redis zset）、BLPOP 原子认领、心跳租约
- **录制内核**：ffmpeg 拉流、TS 分段（崩溃只丢最后一段）、断流重连、concat 无损合并转 MP4、校验
- **容错续录**：worker 宕机任务置 `recovering`，原节点恢复后续录；产物不跨节点迁移
- **全生命周期日志**：逐条落库（stage 覆盖创建→合并完成，按 `log.retention_days` 定期清理），ffmpeg 原始输出留档可下载（录制/合并）
- **文件管理**：TS 段/产物元数据、手动合并、删除、下载，全部经持有文件的 worker 执行
- **可观测**：Prometheus 指标（任务数/成功率/worker 负载/磁盘）+ Grafana
- **认证**：JWT 登录（refresh token 静默续期）、admin/user 角色、登录失败 5 次锁定（admin 可解锁）、登录 IP 限流
- **安全**：worker 内部接口共享密钥鉴权（`INTERNAL_API_TOKEN`）、审计日志（登录/用户管理/任务干预/文件操作）

## 架构总览

```
 浏览器 ─HTTP─▶ scheduler(无状态,多副本,内嵌SPA) ─┬─ MySQL(任务/用户/文件元数据/日志)
                 │ 调度(zset定时)+文件/日志编排     ├─ Redis(定时队列/任务队列/租约/锁/注册)
                 │                                └─ Consul(可选,可ACL: 注册/健康)
                 └─HTTP(内部,Redis 按节点路由)─▶ worker-A/B/C（各自 records-dir） ─▶ Prometheus
```

- 任务状态以 **MySQL** 为真相源；节点存活由 **Redis 心跳租约** 判断。
- **scheduler 不访问文件系统**，文件操作一律经持有该文件的 worker 的内部 HTTP 接口完成。
- 服务注册引擎可配置（`registry.engine`）：默认 redis，可选 consul。

技术栈：Go 1.26（Gin + GORM + Viper + golang-migrate + go-redis）、Vue3 + Vite + Element Plus、ffmpeg、Prometheus。

## 快速开始（Docker Compose）

```bash
cd deploy
cp .env.example .env        # 修改密码/密钥（生产必须改）
docker compose up -d --build
```

| 服务 | 端口 | 说明 |
|------|------|------|
| scheduler | 8080 | 调度器 + 内嵌前端 + Swagger |
| worker | 8081 | 录制执行器（内置 ffmpeg） |
| mysql | 3306 | 持久化（任务/用户/日志/节点） |
| redis | 6379 | 队列 / 心跳 / 锁 |
| consul | 8500 | 服务注册（可选 ACL） |

启动后：

- 前端 UI：<http://127.0.0.1:8080>（首次启动用 `ADMIN_INITIAL_PASSWORD` 设置的 admin 登录）
- API 文档（Swagger）：<http://127.0.0.1:8080/swagger/index.html>
- Prometheus 指标：`:8080/metrics`（scheduler）、`:8081/metrics`（worker）

worker 水平扩展：`docker compose up -d --scale worker=3`。NODE_ID 默认取容器 IP 天然唯一，无需手工配置；单实例启停等运维见 `deploy/README.md` 的「多 worker 运维」。

## 本地开发

```bash
# 后端（需本地 MySQL/Redis，或复用 deploy 的 compose 基础设施）
go run ./cmd/recmaestro --role=scheduler --env=local
go run ./cmd/recmaestro --role=worker --env=local

# 前端
cd web && npm install && npm run dev   # /api 自动代理到 http://127.0.0.1:8080

# 测试与文档
go test ./...
swag init -g cmd/recmaestro/main.go    # 改了 handler 注释后重新生成 Swagger
```

启动时自动执行数据库迁移建表。首次启动 scheduler 需设置环境变量 `ADMIN_INITIAL_PASSWORD`（创建初始管理员）和 `JWT_SECRET`。

## 目录结构

```
recmaestro/
├── cmd/recmaestro/          # 统一入口，--role 分发
├── internal/
│   ├── scheduler/           # 调度器（API/调度/编排）
│   ├── worker/              # worker（录制/合并/清理/上报）
│   ├── recorder/            # ffmpeg 录制内核
│   ├── storage/             # 本地文件/日志操作
│   ├── store/               # GORM 数据访问
│   ├── migrate/             # SQL 迁移（embed，启动自动执行）
│   ├── auth/                # JWT 认证
│   ├── config/              # Viper 多环境配置
│   └── metrics/             # Prometheus 指标
├── web/                     # 前端 SPA 源码（构建产物 go:embed 内嵌）
├── config/                  # config.yaml + config.<env>.yaml 多环境配置
├── docs/                    # 架构设计 + 迭代文档 + 生成的 Swagger
└── deploy/                  # Dockerfile + docker-compose + Grafana
```

## 配置

- 多环境：`--env=dev|test|local|prod`（或 `APP_ENV`），先加载 `config/config.yaml` 再用 `config.<env>.yaml` 覆盖。
- 敏感信息一律走环境变量，不写入配置文件：`MYSQL_PASSWORD`、`REDIS_PASSWORD`、`JWT_SECRET`、`CONSUL_TOKEN`、`ADMIN_INITIAL_PASSWORD`。
- 中间件地址可用 `MYSQL_HOST`、`REDIS_ADDR`、`CONSUL_ADDR` 等环境变量覆盖（容器内默认指向服务名）。

## 更多文档

- [架构设计](docs/architecture.md) — 总体设计与技术基准
- [部署说明](deploy/README.md) — Compose 编排细节、生产化建议、与 SRS 对接
- [迭代记录](docs/iterations/README.md) — 各迭代范围与状态

## 记忆目录 (.memory/)

存放跨会话复用的项目级知识，新会话进入本模块前优先阅读。

| 文档 | 内容 |
|---|---|
| [TS 段合并与宕机恢复防线](.memory/TS段合并与宕机恢复防线.md) | 三层防线（优雅停机/预检隔离/时长对账）的设计内核与 ffmpeg concat 退出码 0 陷阱等隐性知识 |
| [E2E 实测套路](.memory/E2E实测套路.md) | 录制链路端到端实测的固定套路：推流源准备、API 全链路、宕机两种注入、验收断言口径、WSL 测试环境陷阱 |
| [安全鉴权与审计约定](.memory/安全鉴权与审计约定.md) | INTERNAL_API_TOKEN 共享密钥机制、登录锁定/解锁、IP 限流、审计写入点清单与登记制 |
| [自动重试与心跳生命周期](.memory/自动重试与心跳生命周期.md) | max_retry 只约束自动重试的两个守卫位置、"不排期即终态"机制、心跳 zset 清扫规则 |
| [ffmpeg留档与超时链](.memory/ffmpeg留档与超时链.md) | stderr 留档机制、三级超时匹配关系（合并10min/探测30s/转发15min）、配置接线 |
| [后台循环与优雅停机](.memory/后台循环与优雅停机.md) | 新增后台循环必须接 ctx 的铁律、双角色停机编排、日志清理分批策略 |

其他关键约定：前端 vite 配置名为 `vite.config.mjs`（unplugin 新版为 ESM-only，CJS 方式加载会报错，勿改回 .js）；`internal/web/embed.go` 的嵌入指令必须带 `all:` 前缀（`//go:embed all:dist`）——go:embed 默认排除下划线/点开头文件，而按需引入构建会产出 `_plugin-vue_*.js` 这类 chunk，漏嵌会在页面加载时 404。
