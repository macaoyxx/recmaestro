# V0.0.8 — 部署联调

- 状态：🟡 进行中
- 起止：2026-09-21 ~ 2026-09-22
- 相关提交：（暂无，待实现）

## 目标（为什么做）

把整套系统真正跑起来并端到端验证，产出可交付的部署物与联调结论，达到「生产可用」的门槛。

## 范围（做了什么，清单）

- **Dockerfile**：scheduler 镜像、worker 镜像（内置 ffmpeg）。
- **docker-compose 编排**：mysql、redis、consul + scheduler（多副本）+ worker（N 副本，各自 records-dir）。
- **多环境完善**：`config.local.yaml`（WSL 单机联调）、`config.prod.yaml`（生产骨架），敏感信息环境变量注入。
- **端到端联调（E2E）**：建任务 → 到点调度 → worker 录制 → 合并 MP4 → 产物下载，全链路跑通。
- 运维补充：启动脚本、`.env.example`、README 部署说明。
- 与 `single-node/` SRS 的对接验证：用内部 SRS 流地址作为直播地址录制一条。

## 影响面

- 汇总迭代 V0.0.1 ~ V0.0.7 全部成果。
- 产出正式部署物，进入可交付状态。

## 验证标准

- `docker compose up -d` 一键拉起全套，服务自检通过、自动迁移建表。
- 全链路 E2E：建任务 → 录制 → 合并 → 下载产物，全程日志与状态正确。
- 用内部 SRS 流地址 + 外部地址各录制成功一条。
- 多 worker 并发录制、worker 宕机恢复、TS 段清理等关键场景复验通过。

## 结果

- 完成：Dockerfile（多阶段：前端构建 + Go 编译 + 运行时含 ffmpeg）、docker-compose 全栈编排（mysql/redis/consul + scheduler + worker）、`.env.example`、启动脚本 `start.sh`、README 部署说明、多环境配置完善（`MYSQL_HOST`/`REDIS_ADDR`/`CONSUL_ADDR`/`RECORDS_DIR`/`WORKER_ADDR` 环境变量覆盖）。
- 过程中修复的真实问题：
  - Docker 构建阶段 `go mod download` 访问 proxy.golang.org 超时 → 设 `GOPROXY=goproxy.cn`。
  - compose 的 config 挂载路径 `./config` 相对 deploy/ 导致挂空目录覆盖镜像配置 → 改 `../config`。
  - worker 地址上报 `0.0.0.0:8080`，Docker 下 scheduler 反向代理到自己导致产物下载失败 → 加 `WORKER_ADDR` 环境变量。
- 验证通过（WSL Docker 实测）：
  - `docker compose up -d` 一键拉起全套，scheduler/worker 启动、自动迁移建表。
  - 全链路 E2E：建任务 → worker 录制 → 合并 MP4 → 产物下载（508KB / 18.86s），日志全生命周期正确。
  - 用内部 SRS 流地址录制成功。
- 说明：多 worker 并发、宕机恢复、TS 段清理等场景在 V0.0.3~V0.0.5 裸进程下已验证，Docker 环境验证了核心全链路。
