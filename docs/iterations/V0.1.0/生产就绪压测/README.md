# 生产就绪压测 — 总报告（V0.1.0 压测战役）

- 状态：🟢 已完成（五场景全过；7 个问题全部修复并复测通过，无未闭环项；三轮终验，最终轮 7/7 判定「生产就绪：通过」）
- 起止：2026-10-15 ~ 2026-10-15（单日战役：首轮 10:17 起 → 第一轮终验 15:21 → 补录轮 1（高并发-1/-2 修复复测）16:16~16:52 → 第二轮终验 17:02 → 补录轮 2（高并发-3 修复 17:21~17:27 + 复测 17:35~17:55）→ 第三轮终验（最终轮）17:56~18:10）
- 环境：WSL Docker Compose 栈（scheduler + worker×3~5 + mysql + redis + consul + SRS 推流源），API/UI `http://127.0.0.1:8080`；终验镜像对齐当前代码（2026-10-15 17:30 构建，其后源码零改动）
- 验收口径：全部录制场景以「时长对账」为准（任务日志『产物时长 Xs（段合计 Ys，对账通过）』+ 下载 ffprobe 复核），不是窗口时长
- 本目录即整场战役的全部归档：00~06 分报告、修复记录×7、scripts/（脚本 + 原始输出/ID 清单/下载样本），无散落他处

## 一、总结论（能否生产使用）

**可以生产使用：五个场景（单机基线/高并发/集群扩展/容错注入/灾备恢复）全部通过，战役累计登记的 7 个问题（3×P1 + 4×P2，其中高并发-3 为补录复测中新发现）全部修复并复测通过、无一遗留；三轮终验的最终轮（第三轮，全部补录修复完成后）7/7 门禁全过，判定「生产就绪：通过」。生产部署按已验证的容量基线执行：5 worker×20 槽 = 100 并发任务、MySQL `max_connections=300`（容量算式：scheduler 100 + 5×worker 30 + 余量 50；每 +1 worker 副本同步 +30）。**

支撑数字（详见各分报告）：

- 稳态吞吐：20 并发 10222.6 RPS、0 错误、p99=3.8ms；录制进行中 300s 耐力 7244.4 RPS、0 错误、无泄漏迹象（02 §1/§4）
- 连接池修复后（高并发-1，补录 R1）：50 并发 6926.7 RPS 0 错误 p99=20.9ms、100 并发 7834.1 RPS 0 错误 p99=34.4ms（修复前 3.629%/10.441% 错误率归零；TIME_WAIT=0、`cannot assign requested address` 0 条、100 并发 RPS 4743→7834 反升 +65%）
- 容量扩展后（高并发-2，补录 R2）：5 worker×20 槽下 100 任务全部获槽、认领 5×20 均衡、0 record_failed、对账 100/100 + ffprobe 一致；溢出语义经 80 任务=60 completed+20 终态失败验证不丢失不重复（补录 R3）
- 停录洪峰治理后（高并发-3，补录轮 2 R3）：100 任务停录洪峰 286s 全收敛、**100/100 completed**、三表一致 100/100/100/100、`Max_used_connections=98`（峰值采样 138 ≪ 300）、连接拒绝 0、Error 1040=0、吞错 [error] 0；历史卡住的 2 个任务经 healLoop 自愈三表补齐（2/2/2/2）并 ffprobe 复核一致
- 容错韧性：worker 强杀/优雅停机/ffmpeg 子进程死亡/scheduler 强杀/MySQL 重启 5/5 注入全过；宕机恢复原节点 2~6s 接管续录，start_seq 正确衔接；scheduler 强杀期间 worker 录制零损失
- 灾备：Redis 重启 1.4s 窗口 AOF 全量恢复、录制零扰动；MySQL 重启 14.2s 窗口六表计数零丢失；全栈 down→up 18.7s 恢复、中断任务原 node_id 续录对账通过（515.8s）
- 终验（第三轮，最终轮）：`go test ./...` 8 包全 ok EXIT=0、重建部署镜像=当前代码（17:30 构建，源码最后改动 17:21~17:27）、API 冒烟 6/6（total=476 与 stats 412+62+2 交叉吻合，无 running 残留）、E2E【终验-1】120.8s 对账通过 + ffprobe 120.842s、SPA 资产全 200

## 二、场景 × 轮次 × 结果矩阵

| # | 场景 | 首轮（2026-10-15） | 发现问题 | 复测 / 补录轮 | 终态 |
|---|---|---|---|---|---|
| 00 | 环境预制 | ✅ 7/7 全过（10:17 起：保活/镜像对齐/worker×3/栈健康/SRS 推流/三脚本/冒烟【环境-1】74.0s 对账 + ffprobe 74.002656s） | 无 | — | ✅ 通过 |
| 01 | 单机基线 | ✅ 核心功能全过（API 冒烟 33 PASS / 2 FAIL——两处 FAIL 均为断言预设错误非产品问题、E2E 195.8s 对账、坏源重试上限 3/3、SPA）；2 个 P2 | 单机基线-1、单机基线-2 | ✅ 第 1 轮复测：3 个修复栈上转绿（锁定/禁用文案链路 14 PASS；Redis NOAUTH/回环绑定/带认证 healthcheck）+ 核心断言回归全过（E2E 194.2s 对账、坏源终态稳定） | ✅ 复测通过 |
| 02 | 高并发压测 | ❌ **不通过**（11:13~11:35）：稳态吞吐/调度正确性/限流/耐力优秀，但 50/100 并发 3.629%/10.441% 错误率（500）、风暴仅 60/100 completed | 高并发-1（P1）、高并发-2（P1） | ⚠️→✅ **两轮补录复测**：补录轮 1（16:16~16:52）高并发-1 修复生效（50/100 并发 0 错误）、高并发-2 容量修复生效（100 任务全部获槽、0 溢出失败、对账 100/100），但暴露**新 P1 高并发-3**（2/100 卡 running）；补录轮 2（17:35~17:55）高并发-3 修复复测通过（100/100 completed、三表一致、洪峰零连接拒绝、历史任务自愈、溢出语义 80=60+20 与回归全过） | ✅ **复测通过**（以补录轮 2 为终态） |
| 03 | 集群扩展 | ✅ 功能断言 11/11（11:42~12:07：扩缩容注册/绑定铁律/recovering 不抢/续录/对账/并发/恢复）；1 个疑似 P2 | 集群扩展-1（P2 疑似） | ✅ 第 1 轮复测（14:21~14:49）：根因实锤并关闭（批量 10/10 无 401、批量窗口 login 仅 2 次、fail fast 定向注入补验）+ 11 项断言全量回归 11/11 | ✅ 复测通过（关闭） |
| 04 | 容错注入 | ✅ 5/5 场景全过（12:15~12:55：优雅停机/强杀/ffmpeg 死亡/scheduler 强杀/MySQL 重启），6 任务对账全过；1 个 P2 观察项 | 容错-1（P2） | ✅ 第 1 轮复测（14:50~15:12）：修复转绿（worker×3 healthz/metrics 匿名 200，/internal 鉴权语义不变）+ 核心回归 2 项全过（强杀续录 326.2s、ffmpeg 死亡 233.6s，采样 104/104 正常）；场景 1/4/5 未重跑（与修复无关联代码路径，分报告已如实说明） | ✅ 复测通过 |
| 05 | 灾备与重启恢复 | ✅ 15/15 全过（MySQL 重启快照 13:47、全栈重建 13:49 起、中断任务终态 14:01:20：Redis/MySQL 重启、全栈重建、原节点续录、终态核验），无 Issue | 无（1 条部署形态观察点见「遗留与建议」） | — | ✅ 通过 |
| 06 | 终验与部署 | ✅ 第一轮（15:21）：五项门禁全过（全量单测 EXIT=0 / 重建部署+栈健康 / API 冒烟（total=194）/ E2E / SPA），判定通过 | 无 | ✅ 第二轮（17:02，补录轮 1 后覆盖式重验）：通过（total=375）；✅ **第三轮（17:56~18:10，最终轮，全部补录修复完成后覆盖式重验）**：重建部署（镜像=当前代码，17:30 构建）、全量单测 8 包 EXIT=0、冒烟 6/6（total=476=412+62+2，无 running 残留）、E2E【终验-1】120.8s 对账+ffprobe 120.842s、SPA 全 200、第二轮遗留 2 条 running 确认已 completed（actual_end=17:30:46），**7/7 通过，判定「生产就绪：通过」，无新增 Issue** | ✅ 通过（以第三轮为准） |

### 补录修复轮（三次治理，均已闭环）

| 补录轮 | 修复 | 内容 | 复测验证 | 结果 |
|---|---|---|---|---|
| 补录轮 1（修复 ~15:47 落地，复测 16:16~16:52） | **高并发-1 连接池修复** | 连接复用三件套：`max_idle_conns 20→100`（峰值连接全量复用，消除收缩扩容 churn）+ `conn_max_lifetime: 1h` + `conn_max_idle_time: 10m`（`internal/store/db.go`、`internal/config/config.go`、`config/config.yaml`） | 02 补录 R1：50/100 并发 60s 错误 0（原 3.629%/10.441%）、p99 20.9/34.4ms、TIME_WAIT=0、cannot assign 0 条（原 43577）、100 并发 RPS +65% | ✅ 复测通过 |
| 补录轮 1（同期） | **高并发-2 容量扩展** | 修复口径=水平扩展 `--scale worker=5`（5×20=100 槽），无代码改动（行为经审定为设计内「错过开播即终态失败」，纯部署容量缺口） | 02 补录 R2：100 任务全部获槽、认领 5×20 均衡、0 record_failed、对账 100/100 + ffprobe 一致；溢出语义另经 80=60+20 批验证（恰好认领一次、不丢失不重复，补录 R3） | ✅ 复测通过（容量口径；同轮暴露的高并发-3 见下行） |
| 补录轮 2（修复 17:21~17:27 落地，定向验证 17:27~17:31，复测 17:35~17:55） | **高并发-3 停录洪峰终态写库治理** | 代码侧：收尾链 `finalizeDBOp` 有界重试（4 次，退避 1s/2s/4s）绝不吞错 + 终态先于清段 + `healLoop` 幂等自愈（铁证=validated 日志+磁盘产物）+ in-flight 防竞态 + `CompleteRunning`/`CreateOutputIfAbsent` 幂等（`worker.go`、`store/task{,log,file}.go`）；部署侧：MySQL `--max_connections=300` + worker 连接池按角色分化 30/副本（环境变量注入，compose 容量算式） | 02 补录轮 2 R3：部署核验（max_connections=300、worker 池 30/30）；历史卡住任务自愈三表 2/2/2/2 + ffprobe 179.645856/179.605856s；100 任务停录洪峰 100/100 completed、三表一致 100/100/100/100、Max_used=98（峰值 138 ≪ 300）、零拒绝零 [error]；回归 50/20 并发错误 0；06 第三轮终验另确认两条遗留 completed | ✅ 复测通过 |

## 三、问题闭环总表（7/7 全闭环，无未闭环项）

| 编号 | 严重度 | 根因（一句话） | 状态 |
|---|---|---|---|
| 高并发-1 | P1 | 连接池无生命周期治理（idle=20 远小于峰值 62），高并发下反复收缩扩容 churn，scheduler 容器 TIME_WAIT 耗尽 ephemeral port，新建 MySQL 连接失败 → API 500（`internal/store/db.go:23-24`、`config/config.yaml:18-19`） | ✅ 复测通过（补录轮 1 R1：50/100 并发 0 错误、TIME_WAIT=0、cannot assign 0 条、RPS 反升 +65%，见 `修复记录/高并发-1.md`） |
| 高并发-2 | P1 | 100 任务风暴超出 3×20=60 容量槽，溢出任务槽位释放后认领时 end_time 已过，过期即终态失败、不排队不顺延（经审定设计内行为，纯部署容量缺口；`config/config.yaml:40` + `worker.go:385,465,645`） | ✅ 复测通过（补录轮 1 R2：scale=5 后 100 任务全部获槽、0 record_failed、对账 100/100；溢出语义 80=60+20 另验证，见 `修复记录/高并发-2.md`） |
| 高并发-3 | P1 | 5 节点 100 路停录洪峰打满 MySQL `max_connections=151`（连接池治理后总账 600≫151，部署侧未同步上调），2/100 任务收尾 `Complete`/`files.Create` 撞 Error 1040 且被 `_ =` 吞错不重试 → 三表不一致永久卡 running、无自愈（`worker.go:612,623` + `store/task.go:159`） | ✅ 复测通过（补录轮 2 修复=部署侧 151→300 + worker 池 30/副本、代码侧 finalizeDBOp 重试不吞错 + healLoop 自愈；R3 复测 100/100 completed、三表一致、零连接拒绝、历史任务自愈收敛，06 第三轮终验复核，见 `修复记录/高并发-3.md`） |
| 单机基线-1 | P2 | 登录处理禁用检查先于锁定计数检查，锁定用户恒提示「账号已被禁用」（`handler_auth.go` 检查顺序，修复=调换顺序） | ✅ 复测通过（锁定/禁用两成因文案可区分，链路 14 PASS，见 `修复记录/单机基线-1.md` + 01 复测） |
| 单机基线-2 | P2 | compose 从未把 REDIS_PASSWORD 传给 redis-server（无 requirepass）且 6379 全接口映射，无认证即可执行命令（部署编排缺陷） | ✅ 复测通过（requirepass + 回环绑定 + 带认证 healthcheck：无认证 NOAUTH、栈功能正常，见 `修复记录/单机基线-2.md` + 01 复测） |
| 容错-1 | P2 | worker InternalRouter 把内部鉴权挂 engine 级，`/healthz`、`/metrics` 一并被拦匿名 401（修复=鉴权收敛到 `/internal` 分组） | ✅ 复测通过（3 worker 匿名 200、/internal 匿名/错 token 仍 401、内部调用链回归正常，见 `修复记录/容错-1.md` + 04 复测） |
| 集群扩展-1 | P2（原「疑似」） | 批量脚本逐任务登录打满每 IP 每分钟 10 次限流，429 被 login.sh 转成空 token 后无鉴权头创建 → 误导性 401（非服务端缺陷，修复=脚本整批复用 token + fail fast 透传） | ✅ 复测通过（根因实锤关闭：10/10 全 201、批量窗口 login 仅 2 次、fail fast exit=2 不发创建请求，见 `修复记录/集群扩展-1.md` + 03 复测） |

## 四、环境与复跑说明

### 4.1 环境事实速查

- 全部脚本在 **WSL 内 bash 执行**（Windows 侧无 docker）；复杂流程一律走脚本文件，勿在 `wsl -e bash -c` 里硬嵌 JSON/轮询
- 容器名：`deploy-worker-{1..N}`、`recmaestro-scheduler`、`recmaestro-mysql`、`recmaestro-redis`、`recmaestro-consul`、`recmaestro-srs-e2e`（推流源，独立容器不在 compose 内）
- worker 内拉流地址 `rtmp://recmaestro-srs-e2e:1935/live/e2e`；主机侧探测 `rtmp://127.0.0.1:21935/live/e2e`
- 扩缩 worker：`wsl -e bash -c "cd .../deploy && docker compose up -d --scale worker=N"`（N=1~5；**收场必须回 3**）
- 纪律：严禁 `docker compose down -v`（测试数据保留）；Redis 直查须带密码（`deploy/.env`，单机基线-2 修复后无认证返回 NOAUTH）；登录连续失败 5 次锁号、每 IP 每分钟 10 次限流（脚本批量用法须 `RM_TOKEN` 复用，见集群扩展-1）；密码一律经 login.sh
- 容量基线（高并发-2/-3 修复后）：生产 5 worker×20 槽；MySQL `max_connections=300`，每 +1 worker 副本连接 +30 需同步上调

### 4.2 scripts/ 用法与命令

脚本根目录（WSL 内绝对路径）：`/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts`（下记 `$S`）。**每个场景开场第一步**：

```bash
S=/mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts
bash $S/ensure-stack.sh      # WSL 保活确认 + 栈健康（不在则 up --scale worker=3，等 healthz 上限 120s）
bash $S/ensure-stream.sh     # SRS 容器/源视频/常驻推流缺哪个补哪个 + ffprobe 验证
tok=$(bash $S/login.sh)      # 登录，stdout 只输出 access_token（密码 source deploy/.env，绝不回显）
```

| 分组 | 脚本 | 用途 |
|---|---|---|
| 地基复用 | `ensure-stack.sh` / `ensure-stream.sh` / `login.sh` | 栈保活与健康、推流源、登录取 token（三脚本幂等，全部场景复用） |
| 01 单机基线 | `api-smoke.sh`（登录窗口计数设计见脚本头注释）、`e2e-record.sh`、`bad-source.sh` | API 全链路冒烟；SRS 源端到端对账；坏源重试上限/终态不排期 |
| 01 复测 | `retest-r1-auth.sh`、`retest-r1-core.sh` | 锁定/禁用文案链路；核心断言回归（API 抽测+SPA+E2E+坏源） |
| 02 高并发 | `loadgen/`（`mixed`/`loginburst`/`storm` 三子命令，go1.22 纯标准库，已编译 `loadgen.bin`）、`tcpstat.sh`、`endurance.sh`、`storm_assert.sh` | 混合读压测/登录限流/任务风暴；TCP 状态与 MySQL 指标采样；30s 轮 docker stats；风暴断言 SQL（mysql 客户端需 `--default-character-set=utf8mb4`） |
| 02 补录轮 1 复测 | `retest-hc1.sh`（混合读两档+日志断言，输出存 `retest-hc1.log`）、`retest-hc2-launch.sh`/`retest-hc2-poll.sh`/`retest-hc2-peek.sh`/`retest-hc2-mysql.sh`/`retest-hc2-workerlog.sh`/`retest-hc2-ffprobe.sh`（风暴：扩 5 节点→100 任务→轮询→断言→抽检）、`retest-ro-launch.sh`（溢出批：缩回 3 节点 80 任务）、`retest-regress.sh`（回归抽查，输出存 `retest-regress.log`）、`storm_assert2.sh`（参数化前缀断言，用法 `storm_assert2.sh <前缀>`） | 高并发-1/-2 修复复测全套；高并发-3 现场取证亦出自 `retest-hc2-mysql.sh`/`retest-hc2-workerlog.sh` |
| 02 补录轮 2 复测 | `retest-hc3-launch.sh`（扩 5 节点→记基线→起 900s 连接采样→100 任务【压测S】）、`retest-hc3-assert.sh`（三表一致性核心断言，输出存 `retest-hc3-assert.log`）、`retest-hc3-verify-stuck.sh`（历史卡住任务自愈三表核验）、`retest-hc3-sample.sh`（MySQL 连接 3s 粒度采样→`retest-hc3-conn.tsv`）、`retest-hc3-ffprobe.sh`（产物下载复核，输出存 `retest-hc3-ffprobe.log`）、`retest-hc3-regress.sh`（回归，输出存 `retest-hc3-regress.log`） | 高并发-3 修复复测全套（部署核验/自愈核验/洪峰复测/回归） |
| 03 集群扩展 | `cluster-peek.sh`、`cluster-wait-nodes.sh`、`cluster-create.sh`（支持 `RM_TOKEN` 复用+登录失败 fail fast）、`cluster-task.sh`、`cluster-ip2worker.sh`、`cluster-poll-task.sh`、`cluster-poll-node.sh`、`cluster-task-log.sh`、`cluster-concurrency.sh`（整批复用 token）、`cluster-wait-all.sh`、`cluster-final.sh` | 节点/任务快照、注册收敛轮询、建任务、容器↔IP 映射、状态/节点轮询、关键日志过滤、并发批量+终态等待、终态恢复 3 实例（缩容前 active=0 检查） |
| 03 复测 | `retest-r1-cluster.sh`（s1~s9 分阶段编排，任务 id 登记 `retest-r1-ids.env`）、`retest-r1-failfast.sh`、`retest-r1-peek567.sh` | 缩扩容/停机注入/恢复/心跳时延/并发各阶段幂等重跑；fail fast 定向注入；补查 |
| 04 容错 | `ft-common.sh` + `ft-create-task.sh`、`ft-wait-status.sh`、`ft-task-info.sh`、`ft-task-logs.sh`、`ft-files.sh`、`ft-download.sh`、`ft-sample.sh`、`ft-find-worker.sh` | 容错场景任务操作全家桶（建/轮询/详情/日志/文件清单/下载+ffprobe/注入期采样/worker_id 定位容器） |
| 04 复测 | `retest-r2-ft1-healthz.sh`、`retest-r2-ft-pkill.sh`、`retest-r2-ft-kill.sh` | worker healthz/metrics 探测；ffmpeg 子进程死亡回归；强杀续录回归（注入期 API 采样） |
| 05 灾备 | `05-redis-restart.sh`、`05-redis-verify.sh`、`05-mysql-restart.sh`、`05-rebuild-a1.sh`、`05-rebuild-a2.sh`、`05-rebuild-a3a.sh`、`05-rebuild-a3b.sh`、`05-final.sh`、`05-check-env1.sh`、`05-check-env1b.sh`、状态 `05-ids.env` | Redis 重启+验证、MySQL 重启零丢失、全栈重建三阶段（down/up 一致性/中断任务观测）、终态核验、环境-1 复核 |
| 06 终验 | `final-gotest.sh`（全量单测门禁，密码经 `docker compose config` 提取不回显）、`final-api-smoke.sh`/`final-e2e-record.sh`（第一轮冒烟/E2E）、`smoke-final.sh`/`e2e-final.sh`（第二轮，信封顶层 total 口径）、`06-final-logcheck.sh`/`06-final-spa.sh`/`06-final-close.sh`/`06-final-leftover.sh`（第三轮：链路日志对账/SPA 抽查/收场核验/遗留核对）、`diag-db-varname.sh`（密码来源诊断） | 三轮终验门禁脚本（第二轮沿用 final-gotest.sh，第三轮全组沿用） |
| 原始产物 | `retest-hc1.log`、`retest-regress.log`、`retest-hc3-{assert,ffprobe,regress}.log`、`retest-hc3-conn.tsv`/`baseline.txt`/`window.txt`/`timing.txt`、`retest-{hc2,hc3,ro}-ids.txt`（100/100/80 条风暴任务 ID）、`retest-{hc2,hc3}-dl-sample.mp4`（下载样本）、`loadgen/loadgen.bin` | 复测原始输出与证据样本，供追溯 |

高并发压测工具示例（02 报告口径）：

```bash
cd $S/loadgen && go build -o loadgen.bin .          # 或直接用已编译的 loadgen.bin
./loadgen.bin mixed -c 20 -d 60 -token "$tok"        # 混合读压测（tasks/list:stats:audit-logs = 5:3:2）
./loadgen.bin loginburst -n 12 -user admin -pass "$PW"   # 登录限流验证（PW 取自 deploy/.env，不回显）
./loadgen.bin storm -n 100 -c 10 -prefix 压测 -start <RFC3339> -end <RFC3339> -token "$tok" -out /tmp/storm_ids.txt
bash $S/storm_assert.sh                              # 风暴断言 SQL（认领唯一/对账/分布）
bash $S/storm_assert2.sh 压测R                       # 参数化版断言（按前缀查任意一批风暴）
```

## 五、遗留与建议

**未闭环问题：无。** 7 个问题全部修复复测通过；第二轮终验时曾遗留的 2 条 running 任务（【压测R-78】【压测R-42】，高并发-3 现场残留）已经 healLoop 自愈补写一致，并在第三轮终验确认均 completed（actual_end=17:30:46），遗留清零（06 §十）。

**生产建议（非缺陷，各分报告/修复记录已记录）**

1. scheduler 单点：强杀期间 8080 拒连（healthz=000）为设计内行为，worker 录制零损失、恢复 4s 内 200；如需 API 高可用需引入多副本（注意 V0.1.0 已知遗留：登录限流为单机计数）。
2. node_id 基于 IP（`registry.go:34`）：单机 compose 形态无故障（本战役全栈重建后 IP 集合稳定、自愈与续录均闭环）；未来多机部署（卷不共享、IP 池大）需固定 `NODE_ID`（compose 已预留环境变量）或保证 IP 稳定，否则重建后 recovering 任务可能无人认领（等 interrupt_after 30m 转 interrupted）。
3. MySQL `3306:3306` 仍全接口映射（单机基线-2 修复时仅收紧了 Redis）：有 root 密码保护、性质不同，建议生产收尾时一并评估收紧为回环绑定。
4. 认领非 least-loaded（BLPop 竞争出队，扩容后分布可能不均）：设计内表现，capacity=20 内无风险；需均匀需产品层引入指派策略。
5. `record_failed` 同时表示「重试等待中」与「终态失败」两种处境：仅凭 status 不可区分，需结合 retry_count/任务日志；前端详情页已可见 retry_count。
6. 构建卫生：`internal/web/dist/assets/` 残留旧构建产物（如 index-9hsc34B1.js）仍会被 go:embed 打进二进制，建议构建前清空 dist。
7. 手动合并路径 `internal_api.go` 的 `_ =` 吞错（files/Complete）未在高并发-3 范围处理（同步 HTTP 失败用户可感知重试），建议后续统一收口到 finalizeDBOp（修复记录/高并发-3.md 遗留说明）。
8. 容量运维：100 槽对 100 任务无冗余，任务量增长优先加副本而非上调单节点 capacity；每 +1 worker 副本连接 +30，同步上调 MySQL max_connections（compose 注释已写明算式）。

**测试数据**：全部保留未删（第三轮终验冒烟口径 476 任务：cancelled 2 / completed 412 / record_failed 62、无 running；另本轮新建【终验-1】），含审计与产物文件，可在 http://127.0.0.1:8080 页面回看全部场景过程（任务前缀覆盖：环境/单机基线/压测（首轮 100）/压测R（补录 1 100）/压测RO（溢出 80）/压测S（补录 2 100）/集群扩展/容错/灾备/终验）。

## 六、文档导航

| 文档 | 内容 |
|---|---|
| [00-环境预制.md](00-环境预制.md) | 栈对齐/worker×3/SRS 推流/三脚本/冒烟对账 + 首轮踩坑（POST 分页、雪花 ID、引号转义等） |
| [01-单机基线.md](01-单机基线.md) | 单 worker 全功能基线（API 冒烟/E2E/坏源/SPA）+ 第 1 轮复测（三修复转绿） |
| [02-高并发压测.md](02-高并发压测.md) | 混合读三档/登录限流/百任务风暴/耐力采样 + 2 个 P1 根因定位 + **补录复测轮 1**（高并发-1/-2 修复验证、新发现高并发-3、溢出语义、回归）+ **补录复测轮 2 R3**（高并发-3 修复复测：部署核验/历史任务自愈/100 任务洪峰 100/100/回归，关闭） |
| [03-集群与动态扩展.md](03-集群与动态扩展.md) | 扩缩容/绑定铁律/recovering/续录/心跳时延 + 复测（集群扩展-1 关闭） |
| [04-容错与异常恢复.md](04-容错与异常恢复.md) | 5 类故障注入（三层防线验证）+ 复测（容错-1 转绿） |
| [05-灾备与重启恢复.md](05-灾备与重启恢复.md) | Redis/MySQL 重启、全栈重建、原节点续录（无 Issue） |
| [06-终验与部署.md](06-终验与部署.md) | 三轮终验（第三轮为全部补录修复后的最终轮，覆盖原文）：全量单测门禁/重建部署/API 冒烟/E2E/SPA/遗留闭环，生产就绪判定：通过 |
| `修复记录/`（单机基线-1、单机基线-2、容错-1、集群扩展-1、高并发-1、高并发-2、高并发-3 共 7 份） | 各闭环问题的根因分析、最小改动与验证证据 |
| `scripts/`（73 个脚本 + loadgen 源码 + 原始输出/ID 清单/下载样本） | 全场景可复跑脚本与证据产物（用法见 §4.2） |
