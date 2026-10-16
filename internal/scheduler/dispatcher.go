package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"recmaestro/internal/store"
)

// RunDispatcher 启动调度后台循环：到点调度 + 宕机检测 + 队列对账。
func (s *Server) RunDispatcher(ctx context.Context) {
	go s.scheduleLoop(ctx)
	go s.recoverLoop(ctx)
	go s.rebuildLoop(ctx)
}

// rebuildLoop 低频对账：周期性按 DB 重建队列。Redis 单独重启且数据意外丢失时
// （scheduler 不重启，启动时的 RebuildQueues 不会触发），最多一个周期自愈。
// 重建幂等：zset ZAdd 幂等，执行队列重复消息由 worker Claim 条件更新去重。
func (s *Server) rebuildLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.RebuildQueues(ctx)
		}
	}
}

// scheduleLoop 周期性扫描到点任务并入队。
func (s *Server) scheduleLoop(ctx context.Context) {
	interval := s.cfg.Scheduler.ScanInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanAndDispatch(ctx)
		}
	}
}

// scanAndDispatch 抢分布式锁后，把到点任务投递到执行队列。
// 锁 TTL 取扫描间隔的 6 倍且不低于 30s（defer 主动释放，TTL 仅进程崩溃兜底），
// 防止单轮投递慢于 TTL 时多副本重叠。
func (s *Server) scanAndDispatch(ctx context.Context) {
	lockTTL := 6 * s.cfg.Scheduler.ScanInterval
	if lockTTL < 30*time.Second {
		lockTTL = 30 * time.Second
	}
	locked, err := s.queue.AcquireScheduleLock(ctx, lockTTL)
	if err != nil {
		log.Printf("[warn] 获取调度锁失败: %v", err)
		return
	}
	if !locked {
		return
	}
	defer func() {
		if err := s.queue.ReleaseScheduleLock(ctx); err != nil {
			log.Printf("[warn] 释放调度锁失败: %v", err)
		}
	}()

	dueIDs, err := s.queue.PopDue(ctx, time.Now())
	if err != nil {
		log.Printf("[warn] 取出到点任务失败: %v", err)
		return
	}
	for _, id := range dueIDs {
		if err := s.dispatch(ctx, id); err != nil {
			log.Printf("[warn] 调度任务 %s 失败: %v", id, err)
		}
	}
}

func (s *Server) dispatch(ctx context.Context, id string) error {
	task, err := s.tasks.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return nil // 已删除
	}
	// 终态任务不投递（取消/删除会清定时队列，此处兜底 PopDue 并发窗口）。
	// 注意 record_failed / interrupted 不在此列：它们出现在定时队列即代表已排自动重试。
	switch task.Status {
	case store.TaskCancelled, store.TaskAborted, store.TaskCompleted, store.TaskMergeFailed:
		return nil
	}
	// 已过截止时间：不再入队空跑，直接终态失败（已录段由人工通过"合并所选"处理）
	if task.EndTime != nil && !time.Now().Before(*task.EndTime) {
		if err := s.tasks.Fail(ctx, id, store.TaskRecordFailed, "record_failed", "已过截止时间，未再投递"); err != nil {
			return err
		}
		s.logger.Log(id, store.LogLevelWarn, store.LogSourceScheduler, store.LogStageFailed, "已过截止时间，不再投递", nil, "")
		return nil
	}
	// 条件置 queued：仅 pending/失败/中断态可投递。多副本重叠或重复消息时
	// running/queued/recovering 任务在此被拦下，不会重复入队造成双录
	ok, err := s.tasks.MarkQueued(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if err := s.queue.Enqueue(ctx, id); err != nil {
		return err
	}
	s.logger.Log(id, store.LogLevelInfo, store.LogSourceScheduler, store.LogStageQueued, "到点调度，任务入队", nil, "")
	log.Printf("任务 %s 到点调度，已入队", id)
	return nil
}

// RebuildQueues 启动兜底：按 DB 重建定时/执行队列，Redis 数据丢失后任务不再卡死。
// 无条件重建：zset ZAdd 幂等；与调度循环并发产生的重复消息由 worker Claim 条件更新去重。
// 投递顺序必须先补投 queued 快照、再处理 pending：若相反，到点 pending 在第一轮被置
// queued 后，又会被第二轮的 queued 快照捞到再补投一次，同一任务确定性双投。
func (s *Server) RebuildQueues(ctx context.Context) {
	now := time.Now()
	queueds, err := s.tasks.ListByStatus(ctx, store.TaskQueued)
	if err != nil {
		log.Printf("[warn] 重建队列：查询 queued 失败: %v", err)
		return
	}
	for _, t := range queueds {
		_ = s.queue.Enqueue(ctx, t.ID)
	}
	pendings, err := s.tasks.ListByStatus(ctx, store.TaskPending)
	if err != nil {
		log.Printf("[warn] 重建队列：查询 pending 失败: %v", err)
		return
	}
	for _, t := range pendings {
		if t.StartTime != nil && t.StartTime.After(now) {
			_ = s.queue.Schedule(ctx, t.ID, *t.StartTime)
		} else if ok, _ := s.tasks.MarkQueued(ctx, t.ID); ok {
			_ = s.queue.Enqueue(ctx, t.ID)
		}
	}
	log.Printf("队列重建完成: pending=%d queued=%d", len(pendings), len(queueds))
}

// recoverLoop 周期性检测心跳超时任务，置 recovering / interrupted。
func (s *Server) recoverLoop(ctx context.Context) {
	window := s.cfg.Scheduler.RecoverWindow
	if window <= 0 {
		window = 3 * time.Minute
	}
	// 检测频率取窗口的一半，尽早发现心跳超时
	ticker := time.NewTicker(window / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.detectExpired(ctx)
		}
	}
}

func (s *Server) detectExpired(ctx context.Context) {
	now := time.Now()
	// 与 recoverLoop 一致的缺省兜底：recover_window/interrupt_after 缺省或误配为 0 时，
	// cutoff 会等于 now，把刚上报的心跳全部判超时、recovering 任务立刻被误中断
	window := s.cfg.Scheduler.RecoverWindow
	if window <= 0 {
		window = 3 * time.Minute
	}
	interruptAfter := s.cfg.Scheduler.InterruptAfter
	if interruptAfter <= 0 {
		interruptAfter = 30 * time.Minute
	}
	recoverCutoff := now.Add(-window)
	expired, err := s.queue.ExpiredHeartbeats(ctx, recoverCutoff)
	if err != nil {
		log.Printf("[warn] 查询超时心跳失败: %v", err)
		return
	}
	interruptCutoff := now.Add(-interruptAfter)

	for _, hb := range expired {
		task, err := s.tasks.GetByID(ctx, hb.TaskID)
		if err != nil {
			continue
		}
		if task == nil {
			// 任务已删除：清残留心跳，防 zset 泄漏（否则该条目每轮都被空查一次）
			_ = s.queue.RemoveHeartbeat(ctx, hb.TaskID)
			continue
		}
		switch task.Status {
		case store.TaskRunning:
			if err := s.tasks.MarkRecovering(ctx, hb.TaskID); err == nil {
				s.logger.Log(hb.TaskID, store.LogLevelWarn, store.LogSourceScheduler, store.LogStageRecovering, "心跳超时，等待节点恢复", nil, "")
				log.Printf("任务 %s 心跳超时，置 recovering", hb.TaskID)
			}
		case store.TaskRecovering:
			if hb.LastAt.Before(interruptCutoff) {
				if err := s.tasks.MarkInterrupted(ctx, hb.TaskID); err == nil {
					s.logger.Log(hb.TaskID, store.LogLevelError, store.LogSourceScheduler, store.LogStageInterrupted, "长期未恢复，任务中断", nil, "")
					log.Printf("任务 %s 长期未恢复，置 interrupted", hb.TaskID)
					// 未过截止时间则自动排重试（等价断流重试，流恢复后续录）。
					// max_retry 只约束自动重试（人工重试是显式操作不受限），用尽后保持 interrupted 终态
					if task.EndTime != nil && time.Now().Before(*task.EndTime) {
						if task.RetryCount < task.MaxRetry {
							backoff := s.cfg.Recorder.RetryBackoffFor(task.RetryCount + 1)
							if err := s.queue.Schedule(ctx, hb.TaskID, time.Now().Add(backoff)); err == nil {
								s.logger.Log(hb.TaskID, store.LogLevelInfo, store.LogSourceScheduler, store.LogStageRetry,
									fmt.Sprintf("节点长期离线，%s 后自动重试", backoff), nil, "")
							}
						} else {
							s.logger.Log(hb.TaskID, store.LogLevelWarn, store.LogSourceScheduler, store.LogStageInterrupted,
								fmt.Sprintf("自动重试次数已用尽（%d/%d），保持中断终态，可人工重试", task.RetryCount, task.MaxRetry), nil, "")
						}
					}
				}
			}
		default:
			// 终态/待执行任务的残留心跳：任务不在录，心跳无意义，清掉防 zset 泄漏；
			// 自动重试到点重新 running 后心跳会重写，删除无副作用
			_ = s.queue.RemoveHeartbeat(ctx, hb.TaskID)
		}
	}
}
