# 直播录制调度平台 + worker — 架构设计文档

> 版本：V0.1.0 · 初稿：2026-09-21 · 修订：2026-10-14（安全配套对齐） · 状态：设计定稿
>
> 本文是「直播录制调度平台 + worker」的总体架构设计，是所有迭代实现的技术基准。实现过程中如有偏离，需回到本文修订。

## 1. 定位与规模目标

一套 Go 程序，用 `--role=scheduler|worker` 参数区分两种角色，共用一套代码、配置与数据模型。

- **scheduler（调度器）**：提供 Web UI（账号登录 + 任务/节点监控 + 文件管理 + 日志查看 + API 文档入口）、任务调度、对外 REST API。
- **worker（录制执行器）**：认领任务 → 用 ffmpeg 拉取直播流录制（TS 分段）→ 结束后无损合并转 MP4 → 上报日志/状态/产物。

**规模目标**：生产可用，可快速水平扩展，支撑几千～几万路并发直播录制。

**核心约束（已确认）**：
- 录制内核统一用 ffmpeg 拉流，直播地址既可以是外部任意地址，也可以是内部 SRS 流地址。
- 录制产物 TS 分段、结束后 concat 无损合并转 MP4（保证视频完整）。
- 非共享存储优先：每个 worker 挂自己的磁盘，产物归属节点；存储对服务而言就是一个本地路径。

## 2. 架构总览

```
 浏览器 ─HTTP─▶ scheduler(无状态,多副本,内嵌SPA) ─┬─ MySQL(主从,前缀表/元数据/事件日志)
                 │ 调度(zset定时)+文件/日志编排     ├─ Redis(主从+哨兵: 定时队列/任务队列/租约/锁/注册)
                 │                                 └─ Consul(可选,可ACL: 注册/健康)
                 └─HTTP(内部,Redis 按节点路由)─▶ worker-A/B/C（各自 records-dir） ─▶ Prometheus metrics
```

> 服务注册引擎可配置（`registry.engine`）：默认 **redis**（TTL key，零额外中间件依赖），可选 consul。
> scheduler 调用 worker 内部接口的路由始终走 Redis `GetWorkerAddr(node_id)`（按任务绑定的节点），不经 Consul。

组件职责分工：

| 组件 | 职责 |
|------|------|
| **MySQL** | 任务、用户、文件元数据、关键事件日志、节点快照的持久化（真相源） |
| **Redis** | 定时调度队列（zset）、任务分发队列、心跳租约（TTL）、分布式锁 |
| **Consul** | worker 服务注册 + 健康检查（支持 ACL 鉴权） |
| **scheduler** | 多副本无状态；登录鉴权、任务 CRUD 与调度、文件/日志操作编排、监控聚合 |
| **worker** | 注册到 Consul、认领任务、管理 ffmpeg 子进程、本地文件操作、上报进度/日志/指标 |

**真相源约定**：
- 任务状态以 **MySQL** 为准。
- 节点存活以 **Consul 健康检查 + Redis 心跳租约** 综合判断。
- 文件本体在 worker 本地盘，MySQL 只存元数据。

## 3. 技术选型

| 层 | 选型 | 说明 |
|----|------|------|
| 语言 | Go | 单二进制，交叉编译部署 |
| Web 框架 | Gin | 路由/中间件成熟 |
| ORM | GORM | MySQL 持久化 |
| 配置 | Viper | YAML + 多环境 + 环境变量 |
| 迁移 | golang-migrate | flyway 式版本迁移，embed 进二进制 |
| Redis 客户端 | go-redis | 队列/租约/锁 |
| Consul 客户端 | hashicorp/consul/api | 注册发现 + ACL |
| 录制 | ffmpeg（exec 子进程） | 比 CGO 绑定简单稳定 |
| 认证 | JWT（golang-jwt） | 无状态鉴权 |
| 文档 | swaggo/swag + gin-swagger | 从注释生成 OpenAPI |
| 指标 | Prometheus client_golang | 可观测 |
| 前端 | Vue3 + Vite + Element Plus | go:embed 内嵌 SPA |

## 4. 统一存储模型

- 录制目录 `records-dir`（如 `/records`）对 worker 而言**就是一个本地路径**，worker 直接做文件操作：ffmpeg 写 TS 段、concat 合并、删除、写日志文件。
- **scheduler 不访问任何文件系统**，对文件的「列出 / 合并 / 删除 / 下载」全部通过持有该文件的 worker 的 HTTP 接口完成。
- 共享盘还是本地盘，是**部署时给 worker 挂载什么**的事，服务代码零分支、零感知。
- **任务绑定节点**：一个任务由认领它的 worker 全程录制，产物在该 worker 的 `records-dir`；worker 宕机后等待其恢复续录，不转移到其他节点（避免跨节点散落，也无需 worker 间文件传输）。

由此得到两个不变量：
1. 一个任务的所有 TS 段/产物永远在同一个 worker 的本地盘上。
2. 文件操作永远在单节点内完成，不存在跨节点文件传输。

## 5. 多环境配置

用 Viper 加载 YAML，对标 Spring yml：分层 + 注释 + 多环境。

- 文件组织：`config/config.yaml`（公共默认，含注释模板）+ `config/config.dev.yaml` / `config.test.yaml` / `config.local.yaml` / `config.prod.yaml`（环境覆盖）。
- 启动：`--env=dev|test|local|prod`（或 `APP_ENV`），先加载 `config.yaml` 再加载 `config.<env>.yaml` 合并覆盖。
- **敏感信息一律走环境变量**（`MYSQL_PASSWORD`、`REDIS_PASSWORD`、`JWT_SECRET`、`INTERNAL_API_TOKEN`、`CONSUL_TOKEN` 等），不硬编码、不入库。其中 `INTERNAL_API_TOKEN` 是 worker 内部接口共享密钥，scheduler 与 worker 必须一致，两角色启动时强校验。

```yaml
server:
  role: scheduler            # scheduler | worker（--role 可覆盖）
  host: 0.0.0.0
  port: 8080
  env: dev                   # dev | test | local | prod

mysql:                       # 密码走 MYSQL_PASSWORD
  host: 127.0.0.1
  port: 3306
  database: live_recorder
  username: root
  charset: utf8mb4
  max_open_conns: 100
  max_idle_conns: 20
  table_prefix: live_rec_    # 表前缀

redis:                       # 密码走 REDIS_PASSWORD
  addr: 127.0.0.1:6379
  db: 0

consul:                      # ACL 鉴权支持
  addr: 127.0.0.1:8500
  token_env: CONSUL_TOKEN    # ACL token 从环境变量读取；未开启 ACL 留空
  scheme: http

recorder:
  records_dir: /records
  ffmpeg_path: /usr/bin/ffmpeg
  ffmpeg_loglevel: warning   # 控制日志量
  segment_seconds: 300       # TS 分段时长(秒)
  worker_capacity: 20        # 单 worker 并发录制数
  reconnect:                 # 断流重连参数
    delay_max: 10
  segment_cleanup:
    cleanup_enabled: true    # 合并后立即删 TS 段(默认 true)
    retention_days: 3        # 不立即清理时保留天数(默认 3)

jwt:
  secret_env: JWT_SECRET     # 从环境变量读取
  expire: 24h

log:
  level: info
  retention_days: 30         # 任务日志保留天数，scheduler 每小时分批清理（<=0 关闭）

scheduler:
  scan_interval: 30s         # zset 到点扫描间隔
  recover_window: 3m         # worker 心跳超时进入 recovering 的窗口
  interrupt_after: 30m       # 长期离线判中断阈值
```

**Consul ACL**：客户端初始化读取 `token_env` 对应环境变量并设置 token；未开启 ACL 留空即匿名访问。开启 ACL 时需给 token 授予 policy——worker：`service:write`（注册）+ `agent:write`（健康检查）；scheduler：`service:read`、`health:read`（服务发现）。

## 6. 数据库设计

**命名规范**：数据库 `live_recorder`，表名统一前缀 **`live_rec_`**（可配置，默认 `live_rec_`）。

### 表结构

- `live_rec_users`：id、username、password_hash(bcrypt)、role(admin/user)、status(enabled/disabled)、failed_login_count、last_login_at、created_at、updated_at
- `live_rec_tasks`：id、name、created_by、stream_url、start_time、end_time、actual_start_time、actual_end_time、status、worker_id、priority、segment_seconds、max_retry、retry_count、output_path、error_code、error_msg、last_heartbeat_at、created_at、updated_at（索引 `status+start_time`）
- `live_rec_task_files`：id、task_id、node_id、filename、type(segment|output)、path、seq、size_bytes、duration_ms、file_hash、status(ok|partial|corrupt)、created_at、deleted_at（索引 `task_id+seq`、`node_id`）
- `live_rec_task_logs`：id、task_id、ts、level(info|warn|error)、source(scheduler|worker|ffmpeg-record|ffmpeg-merge)、stage、message、exit_code、node_id（索引 `task_id+ts`）
- `live_rec_nodes`：id、node_id、host、ip、status(online|offline)、disk_total、disk_free、cpu、mem、ffmpeg_version、capacity、current_tasks、last_heartbeat_at、registered_at
- `live_rec_audit_logs`：id、user_id、action、resource、resource_id、detail、ip、user_agent、created_at

### 版本控制

- 用 **golang-migrate**，迁移文件 `NNNN_name.up.sql` / `NNNN_name.down.sql`，`go:embed` 进二进制。
- **服务启动时自动执行 `migrate.Up()`** 建库建表，版本记录在 `schema_migrations`。
- `live_rec_task_logs` 定期清理已落地：`log.retention_days`（默认 30，≤0 关闭），scheduler 每小时分批删除（每批 5000 条，避免大事务锁表）。

## 7. 任务状态机

```
pending → queued → running ──▶ merging ──▶ completed
              │        │            │
              │        ├─▶ recovering ──▶ running (节点恢复续录)
              │        │        └─▶ interrupted (长期离线，待人工)
              │        ├─▶ record_failed (源异常/拉流失败，可重录)
              │        └─▶ cancelled
              └─▶ cancelled
merging 失败 ──▶ merge_failed (段仍在，可手动重合并/删除)
```

状态说明：

| 状态 | 含义 |
|------|------|
| pending | 已创建，等待到点（定时任务）或等待调度 |
| queued | 已投递队列，等待 worker 认领 |
| running | 录制中 |
| recovering | worker 心跳超时，等待原节点恢复续录（录制暂停） |
| merging | 录制结束，合并转 MP4 中 |
| completed | 完成，产物已生成 |
| record_failed | 拉流/录制失败（源异常） |
| merge_failed | 合并失败，TS 段仍在，可手动重合并/删除 |
| interrupted | 节点长期离线，中断待人工处理 |
| cancelled | 已取消 |

- 支持「定时开始」（start_time 未来，到点自动触发）与「立即开始」（start_time 空/已过）。
- end_time 到点自动停；也可手动取消。
- 每次状态变更写一条日志；失败必写 error 日志（stage + 退出码 + 摘要）。

## 8. 全生命周期日志

统一落库 `live_rec_task_logs`，逐条记录任务从创建到合并完成/失败的每一步。

- **stage 覆盖全生命周期**：`created → queued → claimed → started → recording(采样) → reconnect(断流重连) → stopped → merging → merged → validated → completed`；异常 `failed / cancelled / recovering / interrupted`。
- **合并阶段日志同样落库**：合并是独立 ffmpeg 进程（concat `-c copy` → remux MP4），其输出以 source=`ffmpeg-merge` 逐条写入。
- **原始输出留档**：ffmpeg stderr（级别随 `ffmpeg_loglevel`，默认 warning）在录制/合并结束时全量落任务目录 `ffmpeg-record.log` / `ffmpeg-merge.log`，详情页可下载（record/merge 两个入口）。
- **控制落库量**：ffmpeg 用 `-nostats` 且日志级别可配（`ffmpeg_loglevel`，默认 warning）；录制进度定时采样（如每 30s 一条）为规划项，当前由阶段/段级日志覆盖。
- **写入异步批量**：日志经内存缓冲批量 insert，不打爆单库；预留独立日志存储（ClickHouse/ES）扩展位。

## 9. TS 段清理策略

- `segment_cleanup.cleanup_enabled`（默认 `true`）：合并完成并校验通过后，worker 本地**立即删除该任务 TS 段**，只留 MP4。
- `segment_cleanup.retention_days`（默认 `3`）：关闭立即清理、或合并后清理失败产生遗留段时，TS 段保留天数。
- **后台兜底清理**：worker 定期（如每小时）扫描自己 `records-dir` 中超过 `retention_days` 的 TS 段删除，并同步更新 `live_rec_task_files`。
- 清理统一由 **worker 本地执行**，符合统一存储模型。

## 10. 容错机制（保证视频完整）

1. **断流重连**：ffmpeg `-reconnect 1 -reconnect_streamed 1 -reconnect_at_eof 1 -reconnect_delay_max N`，同节点续录，写 `reconnect` warn 日志。
2. **TS 分段**：每 5–10 分钟一段独立 `.ts`，崩溃/断流只丢最后一段，其余完好。
3. **节点宕机**：任务置 `recovering` 等原节点恢复续录（段在本地完整保留）。宕机窗口（宕机→恢复）内容必然缺失——那段时间没有进程在录，任何方案都一样；本方案保证的是「已录段完整 + 恢复后继续录」。
4. **结束合并**：concat demuxer `-c copy` 无损合并 → remux MP4 → 校验（段数 / 时长 / 非空 / hash）。
5. **幂等重试**：合并/录制失败可重试；任务可手动重跑。

## 11. 规模化设计

面向几千～几万路并发的关键设计：

1. **定时调度（Redis zset）**：任务创建即以 `start_time` 为 score 写 zset；调度器每 `scan_interval` 用 `ZRANGEBYSCORE` 取到点任务投递，避免全表扫描。
2. **任务分发（原子）**：待执行任务进 Redis 队列，worker 用 **BLPOP 原子 pop** 认领（天然防重复）。现状为拉模式：worker 本地按 `worker_capacity` 自检容量后认领，scheduler 不做按负载路由（**按负载均衡分配为规划项**）；worker 上报容量与负载（任务数/磁盘/CPU/内存）供监控展示。
3. **日志分级 + 异步批量**：见第 8 节。
4. **worker 容量**：单 worker 并发录制数可配（默认 20），按负载横向扩展；几万路 = 数百～数千 worker 分摊。
5. **文件海量管理**：目录按 `records/<task>/` 分层；合并后按配置清理 TS 段；文件元数据建索引 + 定期归档历史。
6. **可观测性**：Prometheus metrics（任务数、成功率、录制时长、ffmpeg 错误率、worker 负载、磁盘）+ Grafana。
7. **高可用（规划，现状单点）**：MySQL 主从、Redis 主从+哨兵、Consul 集群；scheduler 无状态多副本 + Redis 锁保证「到点调度」只有一个副本执行（已实现）。scheduler/worker 均支持 SIGTERM 优雅停机（HTTP Shutdown + 后台循环退出 + 在录任务收尾）。
8. **安全**：登录失败 5 次锁定（admin 启用/重置密码即解锁）、登录 IP 限流（每 IP 每分钟 10 次，超限 429）、worker 内部接口共享密钥鉴权（`X-Internal-Token`，启动强校验）、审计日志（登录/用户管理/任务人工干预/文件操作，`GET /api/audit-logs` 查询）。规划：全局 API 限流、Consul ACL、Swagger 生产可关闭。

## 12. API 设计

### 统一响应约定（V0.1.0 起）

- 全部 JSON 接口走统一信封：普通 `{"code":0,"data":<任意>,"msg":"ok"}`；分页额外顶层 `"total"`；
  失败时 HTTP 状态码照常，body 为 `{"code":<HTTP状态码>,"data":null,"msg":"<说明>"}`。
- `code` 只有两类：0 成功 / 失败=HTTP 状态码，不设业务码表；实现见 `internal/api/response.go`（`OK/OKMsg/Created/OKPage/Fail`，成功默认 msg="ok"，动作类接口经 OKMsg 附文案）。
- **分页查询一律 POST + JSON 入参**（map 一把传，不逐个拼 query）：入参结构体内嵌分页基类 `api.PageQuery{page, page_size}`（归一化 page≤0→1、page_size≤0→20），如任务列表 `POST /api/tasks/list`（+status/keyword）、审计日志 `POST /api/audit-logs/list`。
- 动作类接口结果放 `data`、友好提示放 `msg`（如删除：data={id}、msg="已删除"）。
- 流式接口（产物/日志/ffmpeg 留档下载）仅成功出流，**失败路径同样返回信封 JSON**；前端在 `api.js` 全局解信封（code!=0 抛 msg）。
- 任务日志列表保留游标分页（before/limit）语义，套信封不带 total。
- worker 内部接口为 scheduler 内部契约（按状态码判断），不走信封。

### scheduler 对外（浏览器调用）

| 分类 | 接口 |
|------|------|
| 认证 | `POST /api/login`（IP 限流）、`POST /api/refresh` |
| 任务 | `POST /api/tasks/list`（分页查询，JSON 入参）、`POST /api/tasks`（创建）、`GET /api/tasks/{id}`、`PUT /api/tasks/{id}`（编辑）、`POST /api/tasks/{id}/cancel`、`POST /api/tasks/{id}/retry`、`POST /api/tasks/{id}/abort`、`DELETE /api/tasks/{id}`（干预均 admin） |
| 日志 | `GET /api/tasks/{id}/logs?level=&stage=&before=&limit=`（分页/筛选/实时）、`GET /api/tasks/{id}/logs/download`、`GET /api/tasks/{id}/ffmpeg-log/{name}`（ffmpeg 原始留档，name=record/merge） |
| 文件 | `GET /api/tasks/{id}/files`、`POST /api/tasks/{id}/merge`、`POST /api/tasks/{id}/files/delete`、`GET /api/tasks/{id}/download` |
| 监控 | `GET /api/nodes`、`GET /api/stats`、`GET /api/tasks/{id}/progress` |
| 用户 | `GET/POST /api/users`、`PATCH /api/users/{id}/status`、`PATCH /api/users/{id}/password`（均 admin） |
| 审计 | `POST /api/audit-logs/list`（admin，分页查询） |
| 文档 | `/swagger/*` |

### worker 对内（scheduler 经 Redis 按节点路由调用）

全部内部接口经 `X-Internal-Token` 共享密钥鉴权（环境变量 `INTERNAL_API_TOKEN`，双方一致，常数时间比较）。

| 接口 | 说明 |
|------|------|
| `POST /internal/tasks/{id}/merge` | 手动合并 |
| `POST /internal/tasks/{id}/files/delete` | 删除所选文件 |
| `GET /internal/tasks/{id}/download` | 下载产物 |
| `GET /internal/tasks/{id}/ffmpeg-log/{name}` | 下载 ffmpeg 原始留档（name=record/merge） |

> 心跳、任务认领、进度与结果上报不走 HTTP：心跳走 Redis（节点租约 + 任务心跳 zset），状态与元数据以 MySQL 为真相源。

### 可观测

- `GET /metrics`（Prometheus）。

## 13. 前端页面

- **登录页**。
- **顶栏/侧栏**：「API 文档」入口（跳转 `/swagger/index.html`）。
- **任务列表**：状态筛选；`merge_failed` / `record_failed` / `interrupted` 红色角标「待人工处理」。
- **任务详情**：
  - 「日志」Tab：全生命周期时间线，按 level 彩色，可按 stage/level 筛选、实时刷新（任务终态自动停止、页面不可见暂停）；error 红色卡片展示「阶段 + 退出码 + 错误信息」；下载完整日志与 ffmpeg 原始留档（录制/合并）。
  - 「文件」Tab：文件表格（勾选、文件名、类型、大小、时长、状态、所属节点、可达性）；全选 / 手动合并所选 / 删除所选（二次确认）；`merge_failed` 显示「重新合并」（默认全选 TS 段）；节点离线标「不可达」、操作禁用。
- **节点监控**：worker 在线/离线、磁盘/CPU/内存、当前任务、容量。
- **产物列表与下载**。

## 14. 部署拓扑

- 基础设施：MySQL（主从为规划，现状单点）、Redis（主从+哨兵为规划，现状单点 + AOF）、Consul（集群，可选 ACL）。
- scheduler 多副本（无状态）；worker N 副本（含 ffmpeg，各自 `records-dir`）；Prometheus + Grafana。部署需配置环境变量 `INTERNAL_API_TOKEN`（见 `.env.example`）。
- WSL 单机先 Docker Compose 起一套（`--env=local`）；生产按角色拆分多机、水平扩展 worker（`--env=prod`）。
- 与现有 `single-node/` 的 SRS 解耦：SRS 继续作为「自己推流进来被动录制」的底座，调度平台不依赖它。

## 15. 目录结构规划

```
recmaestro/
├── cmd/
│   └── recmaestro/main.go         # 统一入口，--role 分发
├── internal/
│   ├── config/                    # Viper 配置加载
│   ├── scheduler/                 # 调度器（API/调度/编排）
│   ├── worker/                    # worker（录制/合并/清理/上报）
│   ├── recorder/                  # ffmpeg 录制内核
│   ├── storage/                   # 文件/日志本地操作
│   ├── store/                     # GORM 数据访问
│   ├── migrate/                   # golang-migrate 迁移文件(embed)
│   ├── auth/                      # JWT 认证
│   └── metrics/                   # Prometheus 指标
├── web/                           # 前端 SPA（构建产物 go:embed）
├── config/                        # 多环境配置文件
├── docs/                          # 本文档 + 迭代文档
├── deploy/                        # Dockerfile + docker-compose
└── go.mod
```
