# V0.0.3 — 任务调度

- 状态：🟡 进行中
- 起止：2026-09-21 ~ —
- 相关提交：（暂无，待实现）

## 目标（为什么做）

实现「创建任务 → 到点调度 → worker 认领 → 录制（状态占位）→ 结束」的核心调度闭环与状态机。这是调度平台的中枢，也是几万路并发能否撑住的关键。

## 范围（做了什么，清单）

- 任务 CRUD：`GET/POST /api/tasks`（状态筛选）、`GET /api/tasks/{id}`，`live_rec_tasks` 数据访问。
- **Redis zset 定时调度**：任务创建即以 `start_time` 为 score 写 zset；调度器按 `scan_interval` `ZRANGEBYSCORE` 取到点任务投递；scheduler 多副本用 Redis 锁保证只有一个副本执行。
- **Redis 队列 + BLPOP 认领**：待执行任务入队，worker 原子 pop 认领，防重复。
- **worker 注册/心跳/租约**：worker 启动注册到 Consul，周期心跳写 Redis 租约（TTL）；上报容量与当前负载。
- **任务状态机**：`pending → queued → running → recovering/record_failed/cancelled` 流转（本迭代录制为占位，到点后短 sleep 模拟 running → completed 以打通链路）。
- 宕机恢复：心跳超时置 `recovering`，节点恢复续租并回到 `running`；长期离线置 `interrupted`。
- 取消/重试接口：`POST /api/tasks/{id}/cancel`、`POST /api/tasks/{id}/retry`。

## 影响面

- 依赖迭代 V0.0.1（配置/迁移）与 V0.0.2（鉴权）。
- worker 角色首次产生实际职责（认领/心跳/上报）。

## 验证标准

- 创建定时任务与立即任务，均能「创建 → 到点调度 → 认领 → running → completed」全流程走通。
- 多 worker 并发认领同一任务时仅一个成功（无重复认领）。
- 手动 kill worker 后，其任务进入 `recovering`；重启 worker 后恢复续租。
- 取消 pending/queued/running 任务生效。
- 多 scheduler 副本下「到点调度」不重复投递。

## 结果

- 完成：Task 模型 + TaskStore（原子认领）、Redis 封装（zset 定时队列 / list 任务队列 / 心跳 / 分布式锁）、任务 CRUD、到点调度循环 + 宕机检测循环、worker 认领/心跳/占位录制、取消/重试。
- 说明：节点心跳本迭代用 **Redis 租约**实现（Consul 服务注册留到 V0.0.6 可观测迭代），避免过度设计；「多 scheduler 不重复调度」由 Redis `SET NX EX` 分布式锁保证。
- 验证通过（WSL 实测）：
  - 立即任务全流程：`queued → running → completed`。
  - 定时任务：`pending`（未到点）→ 到点调度 → `completed`。
  - 取消 pending 任务 → `cancelled`。
  - worker 宕机恢复：`running` → kill worker → `recovering` → 重启 → `running` → `completed`。
  - 多 worker 并发：5 任务分散 2 worker（3+2），全部 completed，无重复认领、无丢失。
  - `go build ./...`、`go vet ./...` 通过。
