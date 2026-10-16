# V0.0.6 — 可观测

- 状态：🟡 进行中
- 起止：2026-09-21 ~ —
- 相关提交：（暂无，待实现）

## 目标（为什么做）

几万路并发下，靠眼睛看是看不过来的。需要标准化的指标与节点状态，接入 Prometheus + Grafana，才能定位问题、评估容量、快速扩展。

## 范围（做了什么，清单）

- **Prometheus metrics**：`GET /metrics` 输出，覆盖——在跑任务数、任务成功率、录制时长、ffmpeg 错误率、worker 负载（任务数/CPU/内存/磁盘）、队列积压等。
- **节点监控数据**：worker 周期上报磁盘总量/可用、CPU、内存、ffmpeg 版本、capacity、current_tasks，落 `live_rec_nodes` 并同步 Consul 健康状态。
- 节点查询接口：`GET /api/nodes`、`GET /api/stats`、`GET /api/tasks/{id}/progress`。
- Grafana 看板（dashboard JSON，进 repo 留档）。

## 影响面

- 依赖迭代 V0.0.3（worker 心跳/上报）与 V0.0.4（录制）。
- 纯观测增强，不改业务主流程。

## 验证标准

- `/metrics` 返回合法 Prometheus 指标，核心指标数值随任务运行变化。
- 节点磁盘/CPU/内存/容量/当前任务数上报准确，节点离线后 `live_rec_nodes` 状态正确置 offline。
- Grafana 导入看板后能展示任务与节点关键曲线。

## 结果

- 完成：`internal/metrics`（Prometheus 指标）、`internal/store/node.go`（Node 模型 + NodeStore）、worker 节点状态采集上报（gopsutil 磁盘/CPU/内存 + current_tasks）、scheduler `/metrics` 端点 + 指标采集循环 + 节点离线同步、节点查询 API（nodes/stats/progress）、Grafana 看板 JSON。
- 过程中修复的真实问题：
  - `ffmpeg_version` 列 VARCHAR(64) 装不下完整版本串（约 80 字符）→ 截断到 60 字符。
  - `registered_at` 零值产生 `'0000-00-00'` 被 MySQL 8 拒绝 → 显式赋当前时间。
  - 任务状态 Gauge 残留旧 label 组合 → 采集前 Reset。
  - `syscall.Statfs` 是 Linux 专属（Windows 交叉编译失败）→ 改用 gopsutil `disk.Usage`。
- 验证通过（WSL 实测）：
  - `/metrics` 返回合法指标：scheduler 端（任务状态/队列）、worker 端（完成计数=1、录制时长计数=1、worker 当前任务数/磁盘 ~982GB）均正常。
  - 节点状态上报准确：`/api/nodes` 返回 worker-1 online，含 CPU 0.3%、内存 7%、容量 20。
  - `/api/stats`、`/api/tasks/{id}/progress` 正确。
- 说明：
  - scheduler 与 worker 各自暴露独立 `/metrics`（Prometheus 分别抓取，属分布式抓取标准模型）。
  - 节点离线同步（`nodeSyncLoop` 30s 周期）逻辑已实现，端到端离线场景未专门等待验证。
