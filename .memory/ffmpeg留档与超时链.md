# ffmpeg 留档与超时链

## 是什么 / 规则

### stderr 留档（原始日志）
- 文件名常量 `recorder.FFmpegRecordLog` / `recorder.FFmpegMergeLog`（即 `ffmpeg-record.log` / `ffmpeg-merge.log`），固定落任务录制目录
- Record / Merge **结束时一次性写入**已捕获的 stderr，不做流式追加、不加常驻 goroutine；**失败路径同样留档**（排障价值最高）
- 留档内容级别随 `recorder.ffmpeg_loglevel`（默认 warning）——要更全的 ffmpeg 日志调配置，不改代码
- 下载链路两级：worker `GET /internal/tasks/:id/ffmpeg-log/:name`（白名单 map 映射文件名）→ scheduler `GET /api/tasks/:id/ffmpeg-log/:name` 反代（走 token 鉴权）；前端详情页日志 Tab 有两个入口

### 超时链（三级匹配，改一处必须核对另两处）

| 层 | 超时 | 位置 |
|---|---|---|
| ffmpeg 合并 | 10min（`mergeTimeout`） | `recorder.Merge` 的 CommandContext |
| 单文件 ffprobe | 30s（`probeFileTimeout`） | `recorder.ProbeDuration` |
| scheduler → worker 转发 | 15min（`workerHTTPClient`） | `scheduler/handler_file.go` |

- **`workerHTTPClient.Timeout` 必须 > `mergeTimeout` + 余量**：否则 scheduler 先超时返回 502 而 worker 仍在合并，用户看到失败但任务实际会成功，状态与认知错乱

### 配置接线（V0.1.0 起生效，此前定义了但没用）
- `recorder.ffmpeg_loglevel` → ffmpeg `-loglevel`（默认回退 warning）
- `recorder.reconnect.delay_max` → `-reconnect_delay_max`（默认回退 10）
- Recorder 构造注入：`recorder.New(ffmpegPath, logLevel, reconnectDelayMax)`；测试传 `("", 0)` 即默认行为

## 关键文件
- `internal/recorder/recorder.go` —— 留档写入、超时常量、参数注入与默认回退
- `internal/worker/internal_api.go` —— `handleInternalFFmpegLog`（白名单 + 先 GetByID）
- `internal/scheduler/handler_file.go` —— `handleDownloadFFmpegLog`、`workerHTTPClient`

## 为什么这么做（设计权衡 / 踩坑）
- 下载端点**先 GetByID 再拼路径**：任务 id 来自 URL，直接 `filepath.Join` 有目录穿越风险；经 DB 查到的任务 ID 都是合法字符串，顺带得到 404 语义
- 一次性写入而非流式：stderr 本来就在内存缓冲（错误摘要、合并异常复查都要用），收尾落盘零额外复杂度
- Merge 的 `-loglevel error` **不能**跟着 `ffmpeg_loglevel` 调高：`mergeOutputAnomaly` 靠 stderr 子串识别「退出码 0 但静默丢内容」，级别调高会冲淡信号（这是三防线的一环，见 TS 段合并防线）
- 超时都是 `exec.CommandContext`：损坏流/IO 挂起会让 ffmpeg/ffprobe 无限卡死，没有 ctx 的 exec 是挂在 worker 执行槽上的隐形泄漏

## 相关
[TS 段合并与宕机恢复防线](.memory/TS段合并与宕机恢复防线.md)、[安全鉴权与审计约定](.memory/安全鉴权与审计约定.md)
