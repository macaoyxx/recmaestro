# TS 段合并与宕机恢复防线（P-2 修复的设计内核）

## 是什么 / 规则

录制产物合并链路有三层防线，**改录制/合并/恢复相关代码前必须理解为什么是三层**：

| 防线 | 位置 | 挡什么 |
|---|---|---|
| ① 源头：worker 优雅停机 | `cmd/recmaestro/main.go` runWorker | docker stop（SIGTERM）不再裸死：signal.NotifyContext → cancel → Record 内 stopFFmpeg（SIGTERM→5s→Kill）+ 终扫上报，在写段完整收尾。从源头减少损坏段产生 |
| ② 合并前：逐段健康预检 | `internal/worker/worker.go` finishTask | 每段 ffprobe 探测，失败/时长≤0 判损坏：改名 `.corrupt` 隔离保留 + DB 标记 corrupt + error 日志，不进 list.txt。损失从"损坏段之后全部内容"降为"损坏段本身部分数据"，且非静默 |
| ③ 合并后：时长对账 | 同上 finishTask | 产物时长 vs 健康段时长合计，偏差 >10%（durationTolerance）或探测失败 → merge_failed 保留 TS 段人工介入，绝不静默 completed |

配套规则：
- **start_seq = max(DB MaxSegment, 磁盘 glob 最大段号) + 1**（worker.nextStartSeq）：段是"文件一出现就报 DB"（2s 扫描周期），宕机落在窗口内时 DB 落后于磁盘，只看 DB 会算小 → ffmpeg `-segment_start_number` **覆盖重写**磁盘已有段文件。
- **清理段必须在对账通过之后**：合并成功 ≠ 内容完整，先删段=销毁取证素材。
- 损坏段改名 `.corrupt` 的副作用是天然脱离 `seg_*.ts` glob：不进后续合并清单、不被 cleanupSegments 清理、不被续录扫描——保留取证。

## 关键文件

- `internal/worker/worker.go` —— finishTask（预检/对账/清理时机）、nextStartSeq、goExecuteTask/Wait（优雅停机等待组）
- `internal/recorder/recorder.go` —— mergeOutputAnomaly（stderr 段级错误识别）、MaxSeqOnDisk、stopFFmpeg、Record 的 ctx 取消优雅停路径
- `internal/store/task_file.go` —— MarkSegmentCorrupt、ListActiveByTask（活动清单排除 corrupt）
- `cmd/recmaestro/main.go` —— runWorker 信号捕获与 8s 收尾等待（docker stop 默认 10s 宽限内）

## 为什么这么做（隐性知识，读代码看不出来）

1. **ffmpeg concat 对损坏段的行为是陷阱**：读到清单中间的损坏段时报 "Impossible to open" / "Invalid data" 等段级错误，但 CLI 会**把已读部分封盘写 trailer 后以退出码 0 退出**——只看 cmd.Wait() 错误会判"合并成功"，损坏段之后的所有内容静默丢失（2026-09-23 实测：产物 62.8s，实际丢 ~73s）。所以 Merge 必须复查 stderr（mergeOutputAnomaly）。
2. **MPEG-TS 前缀截断通常仍可解码**：TS 是流式封装无 trailer/index，SIGKILL 截断留下"合法流前缀"，ffprobe/concat 照常读——实测强杀场景"隔离损坏 0 段"且近无损。因此预检防的是小概率（头部/同步字节损坏），**时长对账才是兜底一切的最后防线**，两层缺一不可。
3. **段上报时序**：OnSegment 在文件出现瞬间（扫描周期内）就 CreateSegmentIfAbsent，不是写完才报。这保证续录 MaxSegment 正确，但也意味着 DB 里的 size 是瞬时值（结束后才修正）、宕机时 DB 可能落后磁盘一个扫描周期——两个方向的偏差都要兜（start_seq 对账）。
4. **优雅停机的等待上限 8s**：docker stop 默认 10s 宽限后 SIGKILL，等任务收尾超过 8s 就主动放弃，剩余交给宕机恢复机制（任务绑定节点，重启后 recovering → 原节点续录），不能为收尾硬扛到被强杀。

## 相关

[E2E 实测套路](.memory/E2E实测套路.md) —— 如何注入验证这三层防线
