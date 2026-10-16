# V0.1.0 — 安全加固与配套对齐
- 状态：🟢 已上线
- 起止：2026-10-14 ~ 2026-10-16
- 相关提交：暂无（当前工作区非 git 管理，无提交哈希可录）
## 目标（为什么做）

V0.0.8 发版测试后全面体检发现：核心调度/录制链路质量扎实，但外围配套存在 7 项安全/正确性缺陷（账号锁死、内部接口裸奔、吞错误误删节点、重试无上限、ffmpeg 无超时、心跳泄漏、边角健壮性），且架构文档承诺的多项配套（审计日志、日志清理、ffmpeg 留档、scheduler 优雅停机、限流）未落地。本迭代一次性修复并对齐，同时完成前端长期欠账（refresh token 续期、轮询治理、构建瘦身）。

## 范围（做了什么，清单）

**A. 安全与正确性修复**
1. 账号锁定解锁路径：`store.Unlock` 清零失败计数，admin 启用/重置密码时接线（修复锁定后永久无法登录）
2. worker 内部接口 token 鉴权：环境变量 `INTERNAL_API_TOKEN` 共享密钥，`X-Internal-Token` 头（常数时间比较），scheduler 转发/反代携带；两角色启动强校验（fail fast）
3. `syncNodeOffline` 吞 Redis 错误误删在线节点 → 出错跳过并告警
4. `MaxRetry` 生效：worker `failAndRetry` 与 dispatcher 中断自动重试均受上限约束（达到即终态失败，不再排期）；手动重试不受限
5. ffmpeg `Merge`（10min）/`ProbeDuration`（30s）超时：`exec.CommandContext` 强杀挂死进程
6. 心跳 zset 泄漏：`detectExpired` 对终态/已删除任务的残留心跳清扫（default 分支）
7. 边角健壮性：反代 `url.Parse` 错误处理、`GetByID` 错误与 404 区分（taskNode/internal_api）、转发专用 15min 超时 client、claimLoop 出错退避 2s

**B. 配套对齐（架构文档承诺落地）**
1. 审计日志：`live_rec_audit_logs` 落地（store + 写入点：登录成功/失败/锁定、用户增删启停/重置密码、任务编辑/中止/删除、手动合并/删除文件；`GET /api/audit-logs` admin 分页查询）
2. 任务日志定期清理：`log.retention_days`（默认 30，≤0 关闭），scheduler 每小时分批（5000/批）删除
3. ffmpeg 原始 stderr 留档：录制/合并结束落 `ffmpeg-record.log`/`ffmpeg-merge.log`，worker `GET /internal/tasks/:id/ffmpeg-log/:name` + scheduler `GET /api/tasks/:id/ffmpeg-log/:name` 两级下载；顺带接线 `ffmpeg_loglevel` 与 `reconnect.delay_max` 两个已定义未用的配置
4. scheduler 优雅停机：SIGTERM/SIGINT → HTTP Shutdown（5s）→ 后台循环随 ctx 退出、日志 flush
5. 登录 IP 限流：每 IP 每分钟 10 次，超限 429 + Retry-After（内存固定窗口，单机语义）

**C. 前端**
1. refresh token 静默续期：401 单飞刷新 + 原请求重放，刷新失败才登出
2. 任务详情轮询治理：终态停止、页面不可见暂停/可见恢复、轮询错误静默
3. 构建瘦身：路由懒加载 + Element Plus 按需引入（主包 1071KB 单体 → 255KB 核心 + 懒加载分块，首屏约减 76%）
4. 去重：`format.js` 统一状态文案/标签色/字节/时间格式；新增 ffmpeg 原始日志下载入口
5. 新增统计仪表盘页（顶栏「统计」菜单，后补范围）：录制中大数字、任务状态分段信号条、队列/节点指标卡，10s 轮询
6. 全站 UI 重设计「播控台暗色」（后补范围）：左侧边栏 + ON AIR 信号面板（录制路数 + 跳动时间码）签名元素、EP 深色变量全量覆写（墨蓝令牌）、登录控制台面板、日志终端化、节点机架卡片、状态呼吸圆点、数字等宽；修复 go:embed 排除下划线文件导致 chunk 404 的问题（`all:dist`）
7. 公共确认弹窗组件 `RmConfirm`（后补范围）：信号灯居中 + 右上角关闭 + tone 三色（danger/warn/signal）+ 悬停确认按钮信号灯「上膛」动画 + ESC/Enter 键盘支持，`rmConfirm()` 全局调用，替代 ElMessageBox
8. 统一 REST 响应规范（后补范围）：`internal/api` 信封 `{code,data,msg[,total]}`（code 0=成功 msg=ok / 失败=HTTP 状态码）、**分页查询一律 POST + JSON 入参**（`/tasks/list`、`/audit-logs/list`，入参内嵌分页基类 `PageQuery{page,page_size}`）、auth 中间件与全部 scheduler JSON handler 接入；流式接口失败路径也走信封；前端 api.js 全局解信封抛 msg
9. 用户管理与审计日志前端页面（后补范围）：侧栏新增「用户」「审计」菜单（仅 admin 可见，角色登录落 localStorage、/api/me 兜底；登录成功即时出现，修复了需刷新的问题）；用户页（列表/创建/启停 rmConfirm/重置密码）、审计页（POST 分页、动作中文名+状态色点、IP/UA）
10. 用户删除与处置护栏（后补范围）：`DELETE /api/users/:id`（物理删除 + 审计）；内置 admin 与当前登录用户不可删、不可禁（后端 409 拦截 + 前端隐藏按钮），其余用户含自建管理员均可处置；登录态同步保存 username
11. 审计对象通用三元组 + 名称快照（后补范围）：迁移 000002 为 audit_logs 增加 `operator_name`/`resource_name` 快照列（含历史回填）；对象统一 `resource(枚举)+resource_id+resource_name` 三元组，名称写入时落库（资源删除后审计仍可读），新增资源类型复用名称列零加列；11 个写入点改 `auditEvent` 结构体，前端对象列显示 类型+ID+名称、用户列 ID+用户名
12. 生产就绪压测（工作流）：五场景（单机/高并发/集群扩展/容错/灾备）+ 修复闭环 + 终验，详见 生产就绪压测/
13. 分页每页条数切换修复（后补范围）：el-pagination 改 `v-model:page-size`（原单向绑定致切换不生效），默认每页条数 20→10（前端两页 + api.PageQuery/store 兜底三处）

**D. 文档**
- 迭代导航状态修正（V0.0.1~V0.0.8 → 🟢）、architecture.md/README 与实现对齐、版本号 0.1.0、swagger 再生成

## 影响面

- **破坏性变更**：scheduler/worker 启动强制要求环境变量 `INTERNAL_API_TOKEN`（未设置启动报错）；部署需在 `.env` 补配（`.env.example` 已更新，compose 默认值兜底开发环境）
- 新增配置项：`log.retention_days`（默认 30）、`recorder.ffmpeg_loglevel` 与 `recorder.reconnect.delay_max` 接线生效（此前定义了但未用）
- worker 内部接口调用方（scheduler 转发/反代）已同步携带 token，无外部调用方
- 前端构建链新增 devDeps（unplugin-vue-components / unplugin-auto-import），vite 配置更名 `vite.config.mjs`
- 压测修复带来的部署参数变化：MySQL `max_connections` 151→300（compose 启动参数）、worker 连接池分化 30/副本（`MYSQL_MAX_OPEN/IDLE_CONNS` 环境变量）、scheduler 连接池 idle=100 + ConnMaxLifetime 1h（容量算式与取舍见 生产就绪压测/修复记录/）

## 结果（上线状态、验证方式、遗留事项）

- 已部署上线（WSL Docker Compose，127.0.0.1:8080，worker×3 基线；2026-10-16 终验通过后含全部修复重建）
- 验证：
  - `go build ./...` / `go vet ./...` 全绿；WSL 全量 `go test ./... -count=1` 全绿（新增用例覆盖解锁闭环、内部鉴权 401、重试上限终态、心跳清扫、审计写入与查询、限流 429、日志清理、留档生成与下载、healLoop 自愈收敛）
  - **生产就绪压测战役**（动态工作流三轮，文档见 [生产就绪压测/](生产就绪压测/README.md)）：五场景（单机基线/高并发压测/集群扩展/容错注入/灾备恢复）全绿；共发现 7 个问题（3 P1 + 4 P2）全部修复并复测闭环；100 任务风暴认领恰好一次/零丢失/时长对账 100/100，5 节点停录洪峰三表一致零吞错；终验 7/7（镜像对齐当前代码 + API 冒烟 + 端到端录制对账 120.8s）
  - 分页修复浏览器实测：默认 10 行，每页条数 10↔20 切换行数与页数即时变化
- 遗留：
  - 多副本 scheduler 下登录限流为单机计数（现状单副本，可接受）
  - 任务日志按 ts 清理暂无单列索引（量大时可补）
  - 压测未覆盖项（多副本 scheduler 高可用、真实公网推流源、>1 小时长稳与 >5 worker 规模、独立负载机）见 生产就绪压测/README.md
