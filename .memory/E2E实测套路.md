# E2E 实测套路（录制链路 / 部署形态 / 宕机注入）

## 是什么 / 规则

在 WSL Docker 全栈上对录制链路做端到端实测的固定套路（发版测试与 P-2 修复复验均用此法）。**所有网络操作（curl/ffprobe）一律在 WSL 内执行**——Windows→WSL 的 localhost 转发不可靠，容器网内地址 Windows 侧也不可达。

### 测试环境陷阱（跑单测/E2E 前必读，2026-10-14 实测）

- **WSL VM 空闲约 60s 自动关机**（默认 vmIdleTimeout）：一条 wsl 命令结束后约 1 分钟 VM 关闭 → systemd 停 docker → 容器全体优雅退出（现象=容器"神秘停止"、下次连接拒绝，mysqld 日志为优雅 SHUTDOWN，不是谁在杀容器）。**起容器 + 跑测试/验证必须放进同一条 wsl 命令**；两条命令间隔 >60s，一切运行状态（含 /tmp）都不复存在。
- **Git Bash（MSYS2）的 `/dev/tcp` 端口探测是假阳性**：连接失败也不返回非零。Windows 侧要验端口用 PowerShell `Test-NetConnection`；网络操作干脆全在 WSL 内做。
- **`docker compose down` 会删除容器**（不只是停）：redis/consul 等下次要 `docker compose up -d <svc>` 重建（named volume 数据不丢）。另有偶发病：容器 Up 且 healthy 但端口不通、inspect 的 IPAddress 显示空或字面量 "invalid IP"——网络端点损坏，`compose down` + 删网络 + `up -d` 重建可修复。
- 跑全量单测的固定命令（同一条 wsl 命令内完成起容器+测试）：

```bash
wsl -e bash -c 'docker start recmaestro-mysql >/dev/null; sleep 10; cd /mnt/c/<repo> && GOPROXY=https://goproxy.cn,direct MYSQL_PASSWORD=root123456 go test ./...'
```

### 环境准备（一次搭好全程复用）

```bash
# 中间件（consul/mysql/redis 一次部署不再动；重置用 down -v 后重启）
wsl -e sh -c "cd /mnt/c/<repo> && docker compose -f deploy/docker-compose.yml up -d mysql redis consul"

# 流媒体 SRS：挂 compose 网络（worker 用服务名访问），主机端口选未占用的（如 21935）
wsl -e sh -c "docker run -d --name recmaestro-srs-e2e --network deploy_default -p 21935:1935 ossrs/srs:5"

# 推流常驻（WSL 内后台循环推视频源）
wsl -e sh -c "nohup ffmpeg -re -stream_loop -1 -i /mnt/c/<视频>.mp4 -c copy -f flv rtmp://127.0.0.1:21935/live/e2e >/tmp/e2e_push.log 2>&1 &"
```

worker 容器内拉流地址用容器名：`rtmp://recmaestro-srs-e2e:1935/live/e2e`；主机侧探测用 `rtmp://127.0.0.1:21935/live/e2e`。

### API 全链路（凭据为 compose 缺省 admin/admin123，down -v 重置后生效）

```bash
tok=$(curl -s -X POST http://127.0.0.1:8080/api/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
end=$(date -d '+N seconds' '+%Y-%m-%dT%H:%M:%S%:z')   # 注意 %:z 带时区偏移
curl -s -X POST http://127.0.0.1:8080/api/tasks -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
  -d '{"stream_url":"rtmp://recmaestro-srs-e2e:1935/live/e2e","name":"e2e-xx","segment_seconds":30,"end_time":"'$end'"}'
# 轮询 GET /api/tasks/<id> 至 completed；GET /:id/files 看分段；/:id/download -o 后 ffprobe 时长；/:id/logs 看链路日志
```

坑：`start_time` 传空串会 400 是历史行为（已修复兼容），测试立即任务**省略该字段**最稳。

### 宕机注入（两种，覆盖不同路径）

| 方式 | 命令 | 覆盖路径 | 预期 |
|---|---|---|---|
| 优雅停机 | `docker stop deploy-worker-N` | SIGTERM 优雅收尾，无损坏段 | 产物≈全程已录内容，日志"对账通过、隔离 0 段" |
| 强杀 | `docker stop -t 0 deploy-worker-N` | SIGKILL，模拟旧 bug 条件 | TS 前缀多数仍可解码（近无损）；真损坏段被隔离；对账兜底 |

**注意：compose stop 只能按服务名停全部实例**，单实例必须用容器名 docker stop/start（容器名 deploy-worker-1/2/3）。按任务定位容器：任务详情的 worker_id（`recmaestro-worker-<IP>-8080`）与 `docker inspect <容器名>` 的 IP 比对。

### 验收断言口径

- **时长对账**：任务日志「产物时长 Xs（段合计 Ys，对账通过）」，X≈Y；产物下载后 ffprobe 复核与 API 上报一致。
- **停机空窗期无录制属设计内**（任务绑定节点不迁移）：期望时长 = 宕机前已录 + 恢复后续录，不是整个 end_time 窗口。
- 转 recovering 约 recover_window（默认 3 分钟）+ tick 对齐；原节点 docker start 后 15s 内 resumeLoop 接管续录。

## 关键文件

- `deploy/docker-compose.yml` —— 全栈编排与 worker scale 设计
- `docs/iterations/V0.0.8/发版测试及修复报告.md` —— 六项门禁矩阵与 P-2 修复复验附录（实测数据与证据的完整存档）

## 为什么这么做

- 推流常驻 + 容器名寻址：一次搭好后各场景复用，worker 只依赖容器网内服务名，与生产形态一致；主机端口避让（曾遇 11935 被残留服务占用，换 21935 解决，不与无关服务纠缠）。
- 两种注入都要测：优雅路径验证收尾行为，强杀路径验证防线兜底——只测一种会漏掉退出码 0 陷阱类问题。
- 断言用"段合计 vs 产物"而非"窗口时长"：停机空窗是设计内损失，拿窗口时长断言必误判。

## 相关

[TS 段合并与宕机恢复防线](.memory/TS段合并与宕机恢复防线.md)
