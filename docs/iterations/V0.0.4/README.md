# V0.0.4 — worker 录制内核

- 状态：🟡 进行中
- 起止：2026-09-21 ~ —
- 相关提交：（暂无，待实现）

## 目标（为什么做）

实现真正把直播流录下来的核心能力：ffmpeg 拉流 → TS 分段 → 断流重连 → 结束后无损合并转 MP4 → 校验，以及节点宕机恢复后的续录。这是「保证视频完整」的根基。

## 范围（做了什么，清单）

- ffmpeg 子进程管理：exec 启停、信号处理、超时与退出码捕获。
- **拉流录制**：按 `recorder` 配置拉取 stream_url（RTMP/HTTP-FLV/HLS/SRT/RTSP，含内部 SRS 流），`-c copy` 写 TS 分段（`segment_seconds`）。
- **断流重连**：`-reconnect 1 -reconnect_streamed 1 -reconnect_at_eof 1 -reconnect_delay_max N`，同节点续录。
- **合并转 MP4**：concat demuxer `-c copy` 合并 TS → remux MP4 → 校验（段数/时长/非空/hash）。
- **续录**：节点恢复后从 `live_rec_task_files` 最后一段时间点重新拉流续录（新段 seq 递增，旧段在本地保留）。
- 产物元数据上报：每段 TS 写 `live_rec_task_files`（含 size/duration/hash）。
- 失败重试：拉流失败按 `max_retry` 重试，最终置 `record_failed`；合并失败置 `merge_failed`。

## 影响面

- 依赖迭代 V0.0.3 的调度/认领/状态机，把 running 的占位替换为真实录制。
- 引入 ffmpeg 依赖（worker 镜像内置 ffmpeg）。

## 验证标准

- 用本地 ffmpeg 推流（或内部 SRS 流）真实录制一段，产物 MP4 时长/内容正确。
- 中途断开推流源再恢复，能自动重连续录，最终 MP4 无断裂（除重连窗口）。
- 结束时间到点自动停录并完成合并。
- kill worker 后重启，任务从断点续录，旧段保留、最终合并完整。
- 拉流地址无效时按配置重试后置 `record_failed`，并记录错误。

## 结果

- 完成：`internal/recorder`（ffmpeg 拉流 TS 分段、断流续录、concat 合并 MP4、ffprobe 时长校验）、`internal/store/task_file.go`（TaskFile 模型 + 元数据上报）、worker 真实录制替换占位、续录 + 拉流失败重试循环。
- 过程中修复的真实问题：
  - `-reconnect` 是 HTTP 协议专属选项，对 RTMP 会报 `Option reconnect not found` 退出 → 按协议条件添加。
  - concat 的 `file` 路径相对 list.txt 解析，写相对路径导致路径重复 → 改写文件名。
  - ffmpeg 退出后二次读 done 误判「仍在运行」，导致 `stopFFmpeg` 永久阻塞死锁 → 改用 `ffmpegExited` 标志。
  - 段元数据改录制结束统一上报后，kill worker 时无记录、续录从 seq 1 覆盖旧段 → 恢复实时幂等上报（`CreateSegmentIfAbsent`）+ 结束后修正 size。
- 验证通过（WSL 实测，ffmpeg testsrc 推流到 SRS live 作为直播源）：
  - 真实录制 → 合并 → MP4，产物时长正确（19.7s / 33s / 28.8s）。
  - 中途 kill 推流源再恢复，自动续录，最终 MP4 完整（断流窗口缺失符合预期）。
  - `end_time` 到点自动停录并完成合并。
  - kill worker 后重启，从断点续录（seq 递增、旧段保留），最终合并完整。
  - 无效地址（协议不存在）连续失败后置 `record_failed`，错误信息完整记录。
- 已知边界：录制停止瞬间最后一段 TS 的 size 可能为 0（正在写时被停止），不影响产物完整性。
