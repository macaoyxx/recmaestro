package scheduler

// dispatcher_test.go — 调度核心测试：
// 1. 定时扫描：到点任务 pending→queued→入执行队列，未到点不动；
// 2. 去重：MarkQueued 条件更新拦下重复投递（queued/running/终态不入队）；
// 3. 调度锁互斥（miniredis）：持锁副本独占投递，多副本并发恰投一次；
// 4. 恢复窗口/中断判定（store 走 MySQL 测试库）：running→recovering→interrupted
//    及自动重试排期、恢复窗口内不误断；
// 5. 队列重建兜底。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"recmaestro/internal/store"
)

// drainQueue 弹空执行队列并返回全部任务 ID（空队列阻塞 1s 超时即认为已弹完）。
func drainQueue(t *testing.T, s *Server) []string {
	t.Helper()
	var got []string
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		id, err := s.queue.Dequeue(ctx, time.Second) // go-redis BLPop 最小阻塞粒度 1s
		cancel()
		if errors.Is(err, redis.Nil) {
			return got
		}
		if err != nil {
			t.Fatalf("弹出执行队列失败: %v", err)
		}
		got = append(got, id)
	}
}

// assertTaskStatus 断言任务当前状态。
func assertTaskStatus(t *testing.T, s *Server, id string, want store.TaskStatus) {
	t.Helper()
	if task := mustGetTask(t, s, id); task.Status != want {
		t.Errorf("任务 %s 状态期望 %s, 实际 %s", id, want, task.Status)
	}
}

// TestScanAndDispatchDueTasks 到点任务被投递：pending→queued→入执行队列；
// 未到点任务原样留在定时队列，状态不变。
func TestScanAndDispatchDueTasks(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()

	dues := []string{"test-scan-1", "test-scan-2", "test-scan-3"}
	for _, id := range dues {
		start := now.Add(-2 * time.Minute)
		end := now.Add(time.Hour)
		mustCreateTask(t, s, id, store.TaskPending, &start, &end)
		if err := s.queue.Schedule(ctx, id, start); err != nil {
			t.Fatalf("Schedule(%s) 失败: %v", id, err)
		}
	}
	// 未到点任务：30 分钟后才开始
	futureStart := now.Add(30 * time.Minute)
	futureEnd := now.Add(time.Hour)
	mustCreateTask(t, s, "test-scan-future", store.TaskPending, &futureStart, &futureEnd)
	if err := s.queue.Schedule(ctx, "test-scan-future", futureStart); err != nil {
		t.Fatalf("Schedule(test-scan-future) 失败: %v", err)
	}

	s.scanAndDispatch(ctx)

	for _, id := range dues {
		assertTaskStatus(t, s, id, store.TaskQueued)
	}
	assertTaskStatus(t, s, "test-scan-future", store.TaskPending)

	if n, _ := s.queue.QueueLen(ctx); n != 3 {
		t.Errorf("执行队列长度期望 3, 实际 %d", n)
	}
	if n, _ := s.queue.ScheduledLen(ctx); n != 1 {
		t.Errorf("定时队列应只剩未到点任务 1 个, 实际 %d", n)
	}

	// 每个到点任务恰投递一次，无重复
	got := drainQueue(t, s)
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Errorf("任务 %s 被重复投递: %v", id, got)
		}
		seen[id] = true
	}
	for _, id := range dues {
		if !seen[id] {
			t.Errorf("到点任务 %s 未进入执行队列: %v", id, got)
		}
	}
}

// TestScanAndDispatchSkipsWhenLocked 调度锁互斥：其他副本持锁时本轮扫描直接跳过，
// 不投递也不弹出定时队列；锁释放后恢复投递。
func TestScanAndDispatchSkipsWhenLocked(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()

	start := now.Add(-time.Minute)
	end := now.Add(time.Hour)
	mustCreateTask(t, s, "test-lock-1", store.TaskPending, &start, &end)
	if err := s.queue.Schedule(ctx, "test-lock-1", start); err != nil {
		t.Fatalf("Schedule 失败: %v", err)
	}

	// 模拟另一 scheduler 副本持锁
	locked, err := s.queue.AcquireScheduleLock(ctx, 30*time.Second)
	if err != nil || !locked {
		t.Fatalf("模拟副本抢锁失败: locked=%v err=%v", locked, err)
	}
	s.scanAndDispatch(ctx)

	// 持锁期间：任务不动、定时队列未弹出、执行队列为空
	assertTaskStatus(t, s, "test-lock-1", store.TaskPending)
	if n, _ := s.queue.QueueLen(ctx); n != 0 {
		t.Errorf("持锁期间不应投递, 执行队列长度实际 %d", n)
	}
	if n, _ := s.queue.ScheduledLen(ctx); n != 1 {
		t.Errorf("持锁期间定时队列不应被弹出, 实际剩 %d", n)
	}

	// 锁释放后本轮扫描正常投递
	if err := s.queue.ReleaseScheduleLock(ctx); err != nil {
		t.Fatalf("释放锁失败: %v", err)
	}
	s.scanAndDispatch(ctx)
	assertTaskStatus(t, s, "test-lock-1", store.TaskQueued)
	if n, _ := s.queue.QueueLen(ctx); n != 1 {
		t.Errorf("释放锁后应投递 1 条, 实际 %d", n)
	}
}

// TestScanAndDispatchConcurrentSingleDelivery 两副本并发扫描同一批到点任务：
// 调度锁串行化 + 条件置 queued，每个任务在执行队列中恰出现一次（无重复投递）。
func TestScanAndDispatchConcurrentSingleDelivery(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()

	const n = 10
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("test-conc-%02d", i)
		start := now.Add(-time.Minute)
		end := now.Add(time.Hour)
		mustCreateTask(t, s, id, store.TaskPending, &start, &end)
		if err := s.queue.Schedule(ctx, id, start); err != nil {
			t.Fatalf("Schedule(%s) 失败: %v", id, err)
		}
		ids = append(ids, id)
	}

	// 副本二：同一 DB 与 Redis，独立 Server 实例（模拟 scheduler 多副本）
	s2 := &Server{cfg: s.cfg, tasks: s.tasks, queue: s.queue, logger: s.logger}

	var wg sync.WaitGroup
	for _, replica := range []*Server{s, s2} {
		for round := 0; round < 5; round++ { // 每副本多轮扫描，制造重叠窗口
			wg.Add(1)
			go func(rep *Server) {
				defer wg.Done()
				rep.scanAndDispatch(ctx)
			}(replica)
		}
	}
	wg.Wait()

	got := drainQueue(t, s)
	if len(got) != n {
		t.Fatalf("并发扫描后执行队列总数期望 %d, 实际 %d（重复或丢失）: %v", n, len(got), got)
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Errorf("任务 %s 被重复投递: %v", id, got)
		}
		seen[id] = true
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("任务 %s 未被投递: %v", id, got)
		}
		assertTaskStatus(t, s, id, store.TaskQueued)
	}
}

// TestDispatchDedupRepeatedSchedule 去重：已 queued 的任务再次出现在定时队列
// （重复排期消息/残留），MarkQueued 条件更新返回 false，不会二次入队。
func TestDispatchDedupRepeatedSchedule(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()

	start := now.Add(-time.Minute)
	end := now.Add(time.Hour)
	mustCreateTask(t, s, "test-dedup-1", store.TaskPending, &start, &end)
	if err := s.queue.Schedule(ctx, "test-dedup-1", start); err != nil {
		t.Fatalf("Schedule 失败: %v", err)
	}

	s.scanAndDispatch(ctx)
	assertTaskStatus(t, s, "test-dedup-1", store.TaskQueued)
	if n, _ := s.queue.QueueLen(ctx); n != 1 {
		t.Fatalf("首轮扫描后执行队列长度期望 1, 实际 %d", n)
	}

	// 模拟重复排期消息：同一任务再次进入定时队列并到点
	if err := s.queue.Schedule(ctx, "test-dedup-1", now.Add(-time.Second)); err != nil {
		t.Fatalf("二次 Schedule 失败: %v", err)
	}
	s.scanAndDispatch(ctx)

	assertTaskStatus(t, s, "test-dedup-1", store.TaskQueued)
	if n, _ := s.queue.QueueLen(ctx); n != 1 {
		t.Errorf("重复排期不应二次入队, 执行队列长度实际 %d", n)
	}
}

// TestDispatchSkipsRunningTask running 任务出现在定时队列（竞态窗口兜底）：
// 不投递、不打回 queued，避免双录。
func TestDispatchSkipsRunningTask(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()

	start := now.Add(-time.Minute)
	end := now.Add(time.Hour)
	mustCreateTask(t, s, "test-run-1", store.TaskRunning, &start, &end)
	if err := s.queue.Schedule(ctx, "test-run-1", start); err != nil {
		t.Fatalf("Schedule 失败: %v", err)
	}

	s.scanAndDispatch(ctx)

	assertTaskStatus(t, s, "test-run-1", store.TaskRunning)
	if n, _ := s.queue.QueueLen(ctx); n != 0 {
		t.Errorf("running 任务不应入执行队列, 实际 %d", n)
	}
}

// TestDispatchSkipsTerminalTask 终态任务（已取消）残留排期：到点也不投递。
func TestDispatchSkipsTerminalTask(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()

	start := now.Add(-time.Minute)
	end := now.Add(time.Hour)
	mustCreateTask(t, s, "test-term-1", store.TaskCancelled, &start, &end)
	if err := s.queue.Schedule(ctx, "test-term-1", start); err != nil {
		t.Fatalf("Schedule 失败: %v", err)
	}

	s.scanAndDispatch(ctx)

	assertTaskStatus(t, s, "test-term-1", store.TaskCancelled)
	if n, _ := s.queue.QueueLen(ctx); n != 0 {
		t.Errorf("终态任务不应入执行队列, 实际 %d", n)
	}
}

// TestDispatchPastDeadlineFailsDirectly 已过截止时间的到点任务不再入队空跑，
// 直接终态失败 record_failed。
func TestDispatchPastDeadlineFailsDirectly(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()

	start := now.Add(-2 * time.Hour)
	end := now.Add(-time.Hour) // 截止时间已过
	mustCreateTask(t, s, "test-expired-1", store.TaskPending, &start, &end)
	if err := s.queue.Schedule(ctx, "test-expired-1", start); err != nil {
		t.Fatalf("Schedule 失败: %v", err)
	}

	s.scanAndDispatch(ctx)

	task := mustGetTask(t, s, "test-expired-1")
	if task.Status != store.TaskRecordFailed {
		t.Errorf("过期任务期望 record_failed, 实际 %s", task.Status)
	}
	if task.ErrorCode != "record_failed" {
		t.Errorf("error_code 期望 record_failed, 实际 %q", task.ErrorCode)
	}
	if n, _ := s.queue.QueueLen(ctx); n != 0 {
		t.Errorf("过期任务不应入执行队列, 实际 %d", n)
	}
}

// TestDispatchDeletedTaskNoop 定时队列中的任务已在 DB 删除：
// dispatch 静默跳过，不报错、不入队。
func TestDispatchDeletedTaskNoop(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()

	// 只写定时队列，不造 DB 记录（模拟任务已被删除但排期残留）
	if err := s.queue.Schedule(ctx, "test-dispatch-gone", now.Add(-time.Minute)); err != nil {
		t.Fatalf("Schedule 失败: %v", err)
	}

	s.scanAndDispatch(ctx) // 内部对已删除任务应直接返回，不应 Fatal

	if n, _ := s.queue.QueueLen(ctx); n != 0 {
		t.Errorf("已删除任务不应入执行队列, 实际 %d", n)
	}
}

// TestDetectExpiredRunningToRecovering running 任务心跳超时（超出恢复窗口）→ recovering。
func TestDetectExpiredRunningToRecovering(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()
	// 窗口 1m：最后心跳 10m 前，超时
	end := now.Add(time.Hour)
	mustCreateTask(t, s, "test-recov-1", store.TaskRunning, &now, &end)
	if err := s.queue.Heartbeat(ctx, "test-recov-1", now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("写入心跳失败: %v", err)
	}

	s.detectExpired(ctx)

	assertTaskStatus(t, s, "test-recov-1", store.TaskRecovering)
}

// TestDetectExpiredFreshHeartbeatKeptRunning 新鲜心跳（窗口内）不判超时，任务保持 running。
func TestDetectExpiredFreshHeartbeatKeptRunning(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()
	// 窗口 1m：心跳 30s 前，未超时
	end := now.Add(time.Hour)
	mustCreateTask(t, s, "test-recov-fresh", store.TaskRunning, &now, &end)
	if err := s.queue.Heartbeat(ctx, "test-recov-fresh", now.Add(-30*time.Second)); err != nil {
		t.Fatalf("写入心跳失败: %v", err)
	}

	s.detectExpired(ctx)

	assertTaskStatus(t, s, "test-recov-fresh", store.TaskRunning)
}

// TestDetectExpiredRecoveringWithinInterruptWindow recovering 任务心跳超时但
// 未超中断阈值（3m < 5m）：不中断、不排自动重试，继续等待节点恢复。
func TestDetectExpiredRecoveringWithinInterruptWindow(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()
	// RecoverWindow=1m、InterruptAfter=5m：心跳 3m 前，已超窗口但未超中断阈值
	end := now.Add(time.Hour)
	mustCreateTask(t, s, "test-interrupt-keep", store.TaskRecovering, &now, &end)
	if err := s.queue.Heartbeat(ctx, "test-interrupt-keep", now.Add(-3*time.Minute)); err != nil {
		t.Fatalf("写入心跳失败: %v", err)
	}

	s.detectExpired(ctx)

	assertTaskStatus(t, s, "test-interrupt-keep", store.TaskRecovering)
	if n, _ := s.queue.ScheduledLen(ctx); n != 0 {
		t.Errorf("窗口内不应排自动重试, 定时队列实际 %d", n)
	}
}

// TestDetectExpiredInterruptedSchedulesRetry recovering 任务长期未恢复（心跳 10m 前
// 超过 InterruptAfter=5m）→ interrupted，且未过截止时间时按退避排自动重试。
func TestDetectExpiredInterruptedSchedulesRetry(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()
	// retry_count=0 → RetryBackoffFor(1)=10s（封顶 30s），重试排期约在 10s 后
	end := now.Add(time.Hour)
	mustCreateTask(t, s, "test-interrupt-go", store.TaskRecovering, &now, &end)
	if err := s.queue.Heartbeat(ctx, "test-interrupt-go", now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("写入心跳失败: %v", err)
	}

	s.detectExpired(ctx)

	assertTaskStatus(t, s, "test-interrupt-go", store.TaskInterrupted)

	// 自动重试已排期：10s 退避内不可见，推到 12s 后可见
	if n, _ := s.queue.ScheduledLen(ctx); n != 1 {
		t.Fatalf("中断后期望排 1 条自动重试, 定时队列实际 %d", n)
	}
	if due, _ := s.queue.PopDue(ctx, now); len(due) != 0 {
		t.Errorf("退避期内不应到点, 实际弹出 %v", due)
	}
	due, err := s.queue.PopDue(ctx, now.Add(12*time.Second))
	if err != nil {
		t.Fatalf("PopDue 失败: %v", err)
	}
	if len(due) != 1 || due[0] != "test-interrupt-go" {
		t.Errorf("退避到点后应弹出重试排期 [%s], 实际 %v", "test-interrupt-go", due)
	}
}

// TestDetectExpiredInterruptedPastEndNoRetry 长期未恢复且截止时间已过：
// 置 interrupted 但不再排自动重试（避免空跑）。
func TestDetectExpiredInterruptedPastEndNoRetry(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()
	end := now.Add(-time.Minute) // 截止时间已过
	mustCreateTask(t, s, "test-interrupt-end", store.TaskRecovering, &now, &end)
	if err := s.queue.Heartbeat(ctx, "test-interrupt-end", now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("写入心跳失败: %v", err)
	}

	s.detectExpired(ctx)

	assertTaskStatus(t, s, "test-interrupt-end", store.TaskInterrupted)
	if n, _ := s.queue.ScheduledLen(ctx); n != 0 {
		t.Errorf("截止时间已过不应排自动重试, 定时队列实际 %d", n)
	}
}

// TestDetectExpiredIgnoresIrrelevantTasks 心跳超时但任务不在恢复路径上：
// 非 running/recovering 状态不动状态、不排期，但残留心跳被清扫（防 zset 泄漏）；
// DB 已无此任务则清残留心跳后跳过。
func TestDetectExpiredIgnoresIrrelevantTasks(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()
	end := now.Add(time.Hour)
	// queued 任务带超时心跳：不改状态
	mustCreateTask(t, s, "test-irrel-1", store.TaskQueued, &now, &end)
	if err := s.queue.Heartbeat(ctx, "test-irrel-1", now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("写入心跳失败: %v", err)
	}
	// DB 不存在的任务带超时心跳：静默跳过（GetByID 返回 nil）
	if err := s.queue.Heartbeat(ctx, "test-irrel-gone", now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("写入心跳失败: %v", err)
	}

	s.detectExpired(ctx) // 不应 Fatal / Panic

	assertTaskStatus(t, s, "test-irrel-1", store.TaskQueued)
	if n, _ := s.queue.ScheduledLen(ctx); n != 0 {
		t.Errorf("无关任务不应触发任何排期, 定时队列实际 %d", n)
	}
	// 残留心跳应被清扫：超时心跳 zset 清空，不再每轮空查（防泄漏）
	if left, err := s.queue.ExpiredHeartbeats(ctx, now); err != nil || len(left) != 0 {
		t.Errorf("终态/无关任务的残留心跳应被清扫, 仍剩 %v (err %v)", left, err)
	}
}

// TestDetectExpiredRetryCapExhausted 中断自动重试受 max_retry 约束：
// retry_count 已达上限的 recovering 任务被置 interrupted 后不再排自动重试，保持终态。
func TestDetectExpiredRetryCapExhausted(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()
	end := now.Add(time.Hour) // 未过截止时间：若无视上限，此处会排重试
	mustCreateTask(t, s, "test-interrupt-cap", store.TaskRecovering, &now, &end)
	// retry_count 打满 max_retry（均 3）
	if err := testDB(t).Model(&store.Task{}).Where("id = ?", "test-interrupt-cap").
		Updates(map[string]interface{}{"retry_count": 3, "max_retry": 3}).Error; err != nil {
		t.Fatalf("造数失败（打满重试计数）: %v", err)
	}
	if err := s.queue.Heartbeat(ctx, "test-interrupt-cap", now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("写入心跳失败: %v", err)
	}

	s.detectExpired(ctx)

	assertTaskStatus(t, s, "test-interrupt-cap", store.TaskInterrupted)
	if n, _ := s.queue.ScheduledLen(ctx); n != 0 {
		t.Errorf("重试次数用尽不应再排自动重试, 定时队列实际 %d", n)
	}
}

// TestDetectExpiredZeroConfigFallback 配置缺省（recover_window/interrupt_after 为 0）时
// detectExpired 使用兜底窗口（3m/30m）：窗口内的心跳不判超时、不误中断。
// 对应 dispatcher.go 中的缺省兜底修复。
func TestDetectExpiredZeroConfigFallback(t *testing.T) {
	s, _ := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()
	s.cfg.Scheduler.RecoverWindow = 0
	s.cfg.Scheduler.InterruptAfter = 0

	end := now.Add(time.Hour)
	// running 任务 1m 前有心跳：若误用 0 值窗口（cutoff=now）会立刻判超时
	mustCreateTask(t, s, "test-zero-cfg-running", store.TaskRunning, &now, &end)
	if err := s.queue.Heartbeat(ctx, "test-zero-cfg-running", now.Add(-time.Minute)); err != nil {
		t.Fatalf("写入心跳失败: %v", err)
	}
	// recovering 任务 10m 前有心跳：若误用 0 值中断阈值会立刻被中断
	mustCreateTask(t, s, "test-zero-cfg-recovering", store.TaskRecovering, &now, &end)
	if err := s.queue.Heartbeat(ctx, "test-zero-cfg-recovering", now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("写入心跳失败: %v", err)
	}

	s.detectExpired(ctx)

	assertTaskStatus(t, s, "test-zero-cfg-running", store.TaskRunning)
	assertTaskStatus(t, s, "test-zero-cfg-recovering", store.TaskRecovering)
}

// TestRebuildQueues 队列重建兜底：Redis 数据丢失后按 DB 重建——
// 未来 pending 进定时队列；到点 pending 置 queued 入执行队列；queued 补投执行队列。
func TestRebuildQueues(t *testing.T) {
	s, mr := newTestScheduler(t)
	ctx := context.Background()
	now := time.Now()

	futureStart := now.Add(time.Hour)
	end := now.Add(2 * time.Hour)
	pastStart := now.Add(-time.Minute)
	// 未来 pending：应进定时队列
	mustCreateTask(t, s, "test-rb-future", store.TaskPending, &futureStart, &end)
	// 到点 pending：应置 queued 并入执行队列
	mustCreateTask(t, s, "test-rb-due", store.TaskPending, &pastStart, &end)
	// 已 queued：应补投执行队列
	mustCreateTask(t, s, "test-rb-queued", store.TaskQueued, nil, &end)

	// 先放入队列再模拟 Redis 数据整体丢失
	if err := s.queue.Schedule(ctx, "test-rb-future", futureStart); err != nil {
		t.Fatalf("Schedule 失败: %v", err)
	}
	mr.FlushAll()

	s.RebuildQueues(ctx)

	assertTaskStatus(t, s, "test-rb-future", store.TaskPending) // 未到点仍 pending
	assertTaskStatus(t, s, "test-rb-due", store.TaskQueued)
	assertTaskStatus(t, s, "test-rb-queued", store.TaskQueued)

	if n, _ := s.queue.ScheduledLen(ctx); n != 1 {
		t.Errorf("重建后定时队列期望 1 条（未来任务）, 实际 %d", n)
	}
	got := drainQueue(t, s)
	if len(got) != 2 {
		t.Fatalf("重建后执行队列期望 2 条, 实际 %v", got)
	}
	seen := map[string]bool{}
	for _, id := range got {
		seen[id] = true
	}
	if !seen["test-rb-due"] || !seen["test-rb-queued"] {
		t.Errorf("重建后执行队列应含到点 pending 与原 queued 任务, 实际 %v", got)
	}
}
