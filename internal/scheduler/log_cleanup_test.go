package scheduler

// log_cleanup_test.go — 任务日志定期清理核心逻辑：超期删除、保留期内不动、
// 未配置保留天数时禁用清理。

import (
	"context"
	"testing"
	"time"

	"recmaestro/internal/store"
)

// seedCleanupLog 直写一条任务日志（带指定时间）。
func seedCleanupLog(t *testing.T, taskID string, ts time.Time) {
	t.Helper()
	l := &store.TaskLog{
		TaskID: taskID, Ts: ts.Truncate(time.Second),
		Level: store.LogLevelInfo, Source: store.LogSourceScheduler, Stage: store.LogStageRecording,
		Message: "清理测试日志", NodeID: "node-cleanup",
	}
	if err := testDB(t).Create(l).Error; err != nil {
		t.Fatalf("造日志失败（%s）: %v", taskID, err)
	}
}

// TestCleanupOldTaskLogs 保留 7 天：8 天前的日志被删，刚写入的保留。
func TestCleanupOldTaskLogs(t *testing.T) {
	s, _ := newTestScheduler(t)
	s.cfg.Log.RetentionDays = 7
	ctx := context.Background()

	oldID, newID := "lc-old-task", "lc-new-task"
	seedCleanupLog(t, oldID, time.Now().Add(-8*24*time.Hour))
	seedCleanupLog(t, newID, time.Now())
	t.Cleanup(func() {
		if err := testDB(t).Where("task_id IN ?", []string{oldID, newID}).Delete(&store.TaskLog{}).Error; err != nil {
			t.Errorf("清理测试日志失败: %v", err)
		}
	})

	s.cleanupOldTaskLogs(ctx)

	var oldLeft, newLeft int64
	testDB(t).Model(&store.TaskLog{}).Where("task_id = ?", oldID).Count(&oldLeft)
	testDB(t).Model(&store.TaskLog{}).Where("task_id = ?", newID).Count(&newLeft)
	if oldLeft != 0 {
		t.Errorf("超期日志应被删除, 仍剩 %d 条", oldLeft)
	}
	if newLeft != 1 {
		t.Errorf("保留期内日志不应删除, 剩 %d 条（want 1）", newLeft)
	}
}

// TestRunLogCleanupDisabled retention_days≤0 时不启用清理：直接返回不 panic、不删任何日志。
func TestRunLogCleanupDisabled(t *testing.T) {
	s, _ := newTestScheduler(t)
	s.cfg.Log.RetentionDays = 0

	seedCleanupLog(t, "lc-disabled-task", time.Now().Add(-30*24*time.Hour))
	t.Cleanup(func() {
		if err := testDB(t).Where("task_id = ?", "lc-disabled-task").Delete(&store.TaskLog{}).Error; err != nil {
			t.Errorf("清理测试日志失败: %v", err)
		}
	})

	s.RunLogCleanup(context.Background()) // 告警分支：不启动循环

	var left int64
	testDB(t).Model(&store.TaskLog{}).Where("task_id = ?", "lc-disabled-task").Count(&left)
	if left != 1 {
		t.Errorf("清理禁用时日志不应被删除, 剩 %d 条（want 1）", left)
	}
}
