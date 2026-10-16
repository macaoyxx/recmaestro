// 认领与恢复链路测试：容量控制、claimLoop 认领状态流转、resumeOwnTasks 恢复。
// Redis 一律走 miniredis 内存实例（严禁连真实 Redis），MySQL 走测试库；
// 源流探测指向无监听端口，触发快速失败路径验证状态流转。
package worker

import (
	"context"
	"testing"
	"time"

	"recmaestro/internal/store"
)

// waitForStatus 轮询等待任务到达目标状态，超时致命失败。
func waitForStatus(t *testing.T, env *testEnv, id string, want store.TaskStatus) *store.Task {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		task, err := env.w.tasks.GetByID(context.Background(), id)
		if err == nil && task != nil && task.Status == want {
			return task
		}
		time.Sleep(200 * time.Millisecond)
	}
	task, _ := env.w.tasks.GetByID(context.Background(), id)
	got := "<nil>"
	if task != nil {
		got = string(task.Status)
	}
	t.Fatalf("等待任务 %s 变为 %s 超时（当前 %s）", id, want, got)
	return nil
}

// TestClaimLoopRespectsCapacity 容量满时不认领：任务滞留队列；
// 释放容量后认领执行，源流不可用且已过截止时间 → 终态 record_failed，槽位归还。
func TestClaimLoopRespectsCapacity(t *testing.T) {
	t.Parallel()
	env := newTestWorker(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	env.w.cfg.Recorder.WorkerCapacity = 1
	taskID := testTaskPrefix + "claim-cap"
	newTestTask(t, env, taskID, store.TaskQueued, nil)
	if err := env.w.queue.Enqueue(ctx, taskID); err != nil {
		t.Fatalf("入队失败: %v", err)
	}

	// 占满容量后启动认领循环：任务必须滞留队列（不被 BLPop 消费）
	env.w.currentTasks.Store(1)
	go env.w.claimLoop(ctx)
	time.Sleep(3 * time.Second) // 覆盖至少一个 2s 容量等待周期

	task, err := env.w.tasks.GetByID(ctx, taskID)
	if err != nil || task == nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if task.Status != store.TaskQueued {
		t.Fatalf("容量满时任务不应被认领，状态 = %s, want queued", task.Status)
	}
	if n, err := env.w.queue.QueueLen(ctx); err != nil || n != 1 {
		t.Fatalf("容量满时任务应滞留 Redis 队列，len = %d (err %v), want 1", n, err)
	}

	// 释放容量：认领 → running → 源流探测失败（截止时间已过）→ record_failed
	env.w.currentTasks.Store(0)
	final := waitForStatus(t, env, taskID, store.TaskRecordFailed)
	if final.ErrorCode != "stream_unavailable" {
		t.Errorf("错误码 = %q, want stream_unavailable", final.ErrorCode)
	}
	if final.WorkerID != env.w.nodeID {
		t.Errorf("认领后 worker_id = %q, want %q", final.WorkerID, env.w.nodeID)
	}
	// 任务失败后执行槽必须归还，否则容量被永久占用
	if got := env.w.currentTasks.Load(); got != 0 {
		t.Errorf("任务终态后 currentTasks = %d, want 0", got)
	}
	// 队列消费完毕
	if n, err := env.w.queue.QueueLen(ctx); err != nil || n != 0 {
		t.Errorf("认领后队列应清空，len = %d (err %v)", n, err)
	}
}

// TestClaimLoopSkipsClaimedTask 队列中的任务若已被认领（非 queued），本次弹出后不重复执行。
func TestClaimLoopSkipsClaimedTask(t *testing.T) {
	t.Parallel()
	env := newTestWorker(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	taskID := testTaskPrefix + "claim-skip"
	// 任务在别的节点名下 running（模拟已被并发认领）
	newTestTask(t, env, taskID, store.TaskRunning, func(task *store.Task) {
		task.WorkerID = "node-other"
	})
	if err := env.w.queue.Enqueue(ctx, taskID); err != nil {
		t.Fatalf("入队失败: %v", err)
	}

	go env.w.claimLoop(ctx)

	// 循环会立即弹出并因 Claim 条件不满足而跳过：任务保持 running 且槽位不增长
	time.Sleep(3 * time.Second)
	task, err := env.w.tasks.GetByID(ctx, taskID)
	if err != nil || task == nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if task.Status != store.TaskRunning || task.WorkerID != "node-other" {
		t.Fatalf("已认领任务不应被改写: status=%s worker=%s", task.Status, task.WorkerID)
	}
	if got := env.w.currentTasks.Load(); got != 0 {
		t.Errorf("跳过认领时不应占槽，currentTasks = %d, want 0", got)
	}
}

// TestResumeOwnTasks 恢复本节点名下 recovering 任务：转 running 继续执行；
// 其他节点的 recovering 任务不动。
func TestResumeOwnTasks(t *testing.T) {
	t.Parallel()
	env := newTestWorker(t)
	ctx := context.Background()

	mine := testTaskPrefix + "resume-mine"
	theirs := testTaskPrefix + "resume-theirs"
	newTestTask(t, env, mine, store.TaskRecovering, func(task *store.Task) {
		task.WorkerID = env.w.nodeID
	})
	newTestTask(t, env, theirs, store.TaskRecovering, func(task *store.Task) {
		task.WorkerID = "node-other"
	})

	env.w.resumeOwnTasks(ctx)

	// 本节点任务被恢复执行：源流探测失败 + 截止时间已过 → 终态 record_failed
	final := waitForStatus(t, env, mine, store.TaskRecordFailed)
	if final.WorkerID != env.w.nodeID {
		t.Errorf("恢复执行不应改变归属 worker，worker_id = %q", final.WorkerID)
	}
	if got := env.w.currentTasks.Load(); got != 0 {
		t.Errorf("恢复任务终态后 currentTasks = %d, want 0", got)
	}
	// 再次恢复：本节点已无 recovering 任务
	tasks, err := env.w.tasks.ListRecoveringByWorker(ctx, env.w.nodeID)
	if err != nil {
		t.Fatalf("查询待恢复任务失败: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("本节点 recovering 任务应已清空，剩 %d 条", len(tasks))
	}

	// 其他节点的任务不受影响
	other, err := env.w.tasks.GetByID(ctx, theirs)
	if err != nil || other == nil {
		t.Fatalf("查询他人任务失败: %v", err)
	}
	if other.Status != store.TaskRecovering || other.WorkerID != "node-other" {
		t.Errorf("他人节点任务不应被动: status=%s worker=%s", other.Status, other.WorkerID)
	}
}
