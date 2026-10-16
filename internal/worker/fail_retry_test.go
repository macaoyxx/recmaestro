package worker

// fail_retry_test.go — failAndRetry 自动重试上限（max_retry）测试：
// 次数未用尽 → 置 record_failed 并按退避排 Redis 定时队列等自动重试；
// 达到上限 → 终态失败不再排期（人工重试不受限，另行验证于 scheduler Retry API）。

import (
	"context"
	"testing"
	"time"

	"recmaestro/internal/store"
)

// TestFailAndRetryRespectsMaxRetry 自动重试上限（表驱动两个场景）。
func TestFailAndRetryRespectsMaxRetry(t *testing.T) {
	t.Parallel()

	t.Run("未用尽排自动重试", func(t *testing.T) {
		env := newTestWorker(t)
		ctx := context.Background()
		end := time.Now().Add(time.Hour) // 未过截止时间
		task := newTestTask(t, env, testTaskPrefix+"retry-ok", store.TaskRunning, func(task *store.Task) {
			task.EndTime = &end
			task.MaxRetry = 3
			task.RetryCount = 0
		})

		env.w.failAndRetry(ctx, task, "stream_lost", "断流测试失败")

		after, err := env.w.tasks.GetByID(ctx, task.ID)
		if err != nil || after == nil {
			t.Fatalf("查询任务失败: %v", err)
		}
		if after.Status != store.TaskRecordFailed {
			t.Errorf("失败后状态 = %s, want record_failed", after.Status)
		}
		if after.RetryCount != 1 {
			t.Errorf("retry_count = %d, want 1", after.RetryCount)
		}
		if n, _ := env.w.queue.ScheduledLen(ctx); n != 1 {
			t.Errorf("未用尽上限应排 1 条自动重试, 定时队列实际 %d", n)
		}
	})

	t.Run("达到上限终态失败不再排期", func(t *testing.T) {
		env := newTestWorker(t)
		ctx := context.Background()
		end := time.Now().Add(time.Hour)
		task := newTestTask(t, env, testTaskPrefix+"retry-cap", store.TaskRunning, func(task *store.Task) {
			task.EndTime = &end
			task.MaxRetry = 3
			task.RetryCount = 2 // 本次 FailWithRetry 自增到 3，恰好达到上限
		})

		env.w.failAndRetry(ctx, task, "stream_lost", "断流测试失败")

		after, err := env.w.tasks.GetByID(ctx, task.ID)
		if err != nil || after == nil {
			t.Fatalf("查询任务失败: %v", err)
		}
		if after.Status != store.TaskRecordFailed {
			t.Errorf("达到上限后状态 = %s, want record_failed（终态）", after.Status)
		}
		if after.RetryCount != 3 {
			t.Errorf("retry_count = %d, want 3", after.RetryCount)
		}
		if n, _ := env.w.queue.ScheduledLen(ctx); n != 0 {
			t.Errorf("达到上限不应再排自动重试, 定时队列实际 %d", n)
		}
	})
}
