# V0.0.5 — 日志与文件

- 状态：🟡 进行中
- 起止：2026-09-21 ~ —
- 相关提交：（暂无，待实现）

## 目标（为什么做）

让「任务从创建到合并完成整个生命周期」的日志都能在页面看到（含合并日志、报什么错），并让用户能对录制产物文件做手动合并/删除/下载操作。这是平台可运维、可排障的关键。

## 范围（做了什么，清单）

- **全生命周期日志落库**：`live_rec_task_logs` 逐条记录 `created→queued→claimed→started→recording(采样)→reconnect→stopped→merging→merged→validated→completed` 及异常；合并阶段 source=`ffmpeg-merge` 同样落库。
- **日志采集**：worker 捕获 ffmpeg stdout/stderr，`-loglevel warning -nostats` 控制量、进度定时采样；**异步批量写库**（内存缓冲 + 批量 insert）。
- **日志查询**：`GET /api/tasks/{id}/logs`（level/stage 筛选、分页、实时）、`GET /api/tasks/{id}/logs/download`（完整原始输出）。
- **文件操作**：`GET /api/tasks/{id}/files`（列出元数据，含所属节点/可达性）、`POST /api/tasks/{id}/merge`（手动合并所选段）、`POST /api/tasks/{id}/files/delete`（删除所选文件）、`GET /api/tasks/{id}/download`（产物下载，scheduler 反向代理到 worker）。
- **TS 段清理**：`cleanup_enabled` 合并后立即删段；`retention_days` 定期兜底清理过期段，均由 worker 本地执行。
- 节点离线时文件「不可达」标记，对应操作禁用。

## 影响面

- 依赖迭代 V0.0.4 的录制/合并/产物上报。
- worker 增加文件操作与日志上报的内部接口。

## 验证标准

- 一个完整任务（含合并失败场景）的日志覆盖全生命周期，页面可查、可筛选、可下载。
- 合并失败任务的 TS 段在页面可见，可勾选部分段手动合并、可删除所选文件。
- 合并成功后按 `cleanup_enabled` 立即删段只留 MP4；关闭清理时按 `retention_days` 到期删除。
- 产物下载正确；节点离线时对应文件标不可达、操作禁用。

## 结果

- 完成：`internal/store/task_log.go`（TaskLog 模型 + TaskLogStore + TaskLogger 异步批量）、recorder stderr 采集（StderrPipe + OnLog）、scheduler/worker 全生命周期日志写入点、日志查询/下载 API、文件管理 API（列出/手动合并/删除/下载）、worker 内部接口（merge/delete/download）、TS 段清理（立即 + 定期兜底）。
- 验证通过（WSL 实测）：
  - 全生命周期日志完整：`created → queued → claimed → started → stopped → merging → merged → validated → completed`（9 条），可查、可下载。
  - 日志下载（DB 导出文本）、产物下载（反向代理到 worker）正确。
  - 文件列出含可达性；手动删除产物后列表更新（剩余 0）。
  - 手动合并下发链路通（scheduler → worker → 响应）。
  - 合并成功后 `cleanup_enabled` 立即删 TS 段只留 MP4。
- 说明：
  - 日志下载当前实现为「从 DB 导出全部日志文本」；完整原始 ffmpeg 输出落文件（ffmpeg-record.log）未单独实现，留待后续。
  - 手动合并的「merge_failed 任务重新合并」端到端场景未专门制造验证（worker 合并逻辑 `rec.Merge` 已在自动合并验证）；下发链路已实测通。
