package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// seedTaskLog 插入一条种子日志并回填自增 ID（TaskLogStore 未暴露 Create，种子直接经 GORM 写入）。
func seedTaskLog(t *testing.T, taskID, level, stage, message string, ts time.Time) uint64 {
	t.Helper()
	l := &TaskLog{
		TaskID:  taskID,
		Ts:      ts,
		Level:   level,
		Stage:   stage,
		Source:  LogSourceScheduler,
		Message: message,
		NodeID:  "node-a",
	}
	if err := testDB.Create(l).Error; err != nil {
		t.Fatalf("插入种子日志 %s 失败: %v", message, err)
	}
	return l.ID
}

// TestTaskLogList 表驱动验证日志查询：按任务隔离、级别/阶段筛选、before 游标、
// limit 截断与默认值，结果一律按 id 倒序（最新在前）。
func TestTaskLogList(t *testing.T) {
	resetTables(t, "live_rec_task_logs")
	ctx := context.Background()
	ls := NewTaskLogStore(testDB)

	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	// 按时间顺序插入 5 条 A 任务日志，自增 id 与插入顺序一致（a0 < a1 < ... < a4）
	ids := make(map[string]uint64, 6)
	for i, s := range []struct {
		key   string
		level string
		stage string
	}{
		{"a0", LogLevelInfo, LogStageCreated},
		{"a1", LogLevelInfo, LogStageStarted},
		{"a2", LogLevelError, LogStageFailed},
		{"a3", LogLevelWarn, LogStageFailed},
		{"a4", LogLevelError, LogStageCompleted},
	} {
		ids[s.key] = seedTaskLog(t, "task-lg-a", s.level, s.stage,
			fmt.Sprintf("种子-%s", s.key), base.Add(time.Duration(i)*time.Second))
	}
	// 其他任务的日志：任何筛选条件下都不应混入
	ids["b0"] = seedTaskLog(t, "task-lg-b", LogLevelError, LogStageFailed, "种子-b0", base)

	rev := func(keys ...string) []uint64 {
		// keys 按时间正序给出，期望返回顺序为其倒序（id DESC）
		out := make([]uint64, 0, len(keys))
		for i := len(keys) - 1; i >= 0; i-- {
			out = append(out, ids[keys[i]])
		}
		return out
	}
	eq := func(a, b []uint64) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	gotIDs := func(logs []TaskLog) []uint64 {
		out := make([]uint64, 0, len(logs))
		for _, l := range logs {
			out = append(out, l.ID)
		}
		return out
	}

	cases := []struct {
		name    string
		level   string
		stage   string
		before  int64
		limit   int
		wantIDs []uint64
	}{
		{"仅按任务倒序全量", "", "", 0, 0, rev("a0", "a1", "a2", "a3", "a4")},
		{"level 筛选", LogLevelError, "", 0, 0, rev("a2", "a4")},
		{"stage 筛选", "", LogStageFailed, 0, 0, rev("a2", "a3")},
		{"level+stage 组合", LogLevelError, LogStageFailed, 0, 0, rev("a2")},
		{"before 游标取更早日志", "", "", int64(ids["a3"]), 0, rev("a0", "a1", "a2")},
		{"limit 截最新两条", "", "", 0, 2, rev("a3", "a4")},
		{"无匹配返回空", "", LogStageMerging, 0, 0, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs, err := ls.List(ctx, "task-lg-a", c.level, c.stage, c.before, c.limit)
			if err != nil {
				t.Fatalf("List 失败: %v", err)
			}
			if got := gotIDs(logs); !eq(got, c.wantIDs) {
				t.Errorf("结果 ID = %v, want %v", got, c.wantIDs)
			}
		})
	}

	// limit<=0 走默认值 100：5 条全部返回
	logs, err := ls.List(ctx, "task-lg-a", "", "", 0, 0)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(logs) != 5 {
		t.Errorf("limit 默认值下返回 %d 条, want 5", len(logs))
	}
}

// TestTaskLoggerFlush 验证异步批量日志写入器的两条落库路径与退出语义：
// 不足一批由 1 秒定时器落库、写满 100 条立即落库；logger 停止后新日志只进缓冲不再落库。
func TestTaskLoggerFlush(t *testing.T) {
	resetTables(t, "live_rec_task_logs")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logger := NewTaskLogger(testDB)
	logger.Start(ctx)

	// 轮询等待指定条数落库（最多 3 秒，满批路径毫秒级、定时路径一个周期内完成）
	waitFlush := func(want int64) bool {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var got int64
			if err := testDB.Table("live_rec_task_logs").Where("task_id = ?", "task-lf-1").Count(&got).Error; err != nil {
				t.Fatalf("统计日志失败: %v", err)
			}
			if got >= want {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}

	// 路径一：不足一批，等待定时器落库
	exitCode := 3
	logger.Log("task-lf-1", LogLevelWarn, LogSourceFFmpegRecord, LogStageReconnect, "断线重连第 1 次", &exitCode, "node-a")
	logger.Log("task-lf-1", LogLevelInfo, LogSourceWorker, LogStageRecording, "录制中", nil, "node-a")
	if !waitFlush(2) {
		t.Fatal("定时批量落库超时：3 秒内未写入 2 条日志")
	}

	// 落库字段抽查：exit_code / node_id / 级别 / 来源 / 阶段均应原样落库
	logs, err := NewTaskLogStore(testDB).List(context.Background(), "task-lf-1", "", "", 0, 100)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("落库日志数 = %d, want 2", len(logs))
	}
	byMsg := make(map[string]TaskLog, len(logs))
	for _, l := range logs {
		byMsg[l.Message] = l
	}
	reconnect, ok := byMsg["断线重连第 1 次"]
	if !ok {
		t.Fatalf("缺少重连日志，实际 %+v", logs)
	}
	if reconnect.Level != LogLevelWarn || reconnect.Source != LogSourceFFmpegRecord ||
		reconnect.Stage != LogStageReconnect || reconnect.NodeID != "node-a" {
		t.Errorf("重连日志字段不匹配: %+v", reconnect)
	}
	if reconnect.ExitCode == nil || *reconnect.ExitCode != 3 {
		t.Errorf("exit_code 应为 3，实际 %+v", reconnect.ExitCode)
	}
	if reconnect.Ts.IsZero() {
		t.Error("落库 ts 不应为零值")
	}
	recording, ok := byMsg["录制中"]
	if !ok {
		t.Fatalf("缺少录制日志，实际 %+v", logs)
	}
	if recording.Source != LogSourceWorker || recording.Stage != LogStageRecording {
		t.Errorf("录制日志字段不匹配: %+v", recording)
	}
	if recording.ExitCode != nil {
		t.Errorf("录制日志 exit_code 应为 nil，实际 %+v", recording.ExitCode)
	}

	// 路径二：写满 100 条触发批阈值立即落库，不再依赖定时器
	for i := 0; i < 100; i++ {
		logger.Log("task-lf-1", LogLevelInfo, LogSourceWorker, LogStageRecording, fmt.Sprintf("进度 %d", i), nil, "node-a")
	}
	if !waitFlush(102) {
		t.Fatal("满批落库超时：3 秒内未写满 102 条日志")
	}

	// 停止语义：cancel 后 flushLoop 退出，之后的日志只进缓冲、永远不落库。
	// 先等一个周期确保退出完成，再写入并等一个 ticker 周期以上验证总数不变。
	cancel()
	time.Sleep(200 * time.Millisecond)
	logger.Log("task-lf-1", LogLevelError, LogSourceWorker, LogStageStopped, "停止后的日志", nil, "node-a")
	time.Sleep(1200 * time.Millisecond)
	var final int64
	if err := testDB.Table("live_rec_task_logs").Where("task_id = ?", "task-lf-1").Count(&final).Error; err != nil {
		t.Fatalf("统计日志失败: %v", err)
	}
	if final != 102 {
		t.Errorf("停止后落库日志数 = %d, want 102（退出后不应再写入）", final)
	}
}

// TestTaskLogDeleteOlderThan 验证按时间分批删除：只删早于 cutoff 的日志，
// limit 限制单批量，返回值为实际删除条数。
func TestTaskLogDeleteOlderThan(t *testing.T) {
	resetTables(t, "live_rec_task_logs")
	ctx := context.Background()
	ls := NewTaskLogStore(testDB)

	now := time.Now()
	old := now.Add(-48 * time.Hour)
	for i := 0; i < 5; i++ {
		seedTaskLog(t, "del-old", LogLevelInfo, "recording", fmt.Sprintf("旧日志%d", i), old)
	}
	for i := 0; i < 3; i++ {
		seedTaskLog(t, "del-new", LogLevelInfo, "recording", fmt.Sprintf("新日志%d", i), now)
	}

	// cutoff 取 24h 前：5 条旧日志命中，3 条新日志保留
	n, err := ls.DeleteOlderThan(ctx, now.Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatalf("DeleteOlderThan 失败: %v", err)
	}
	if n != 5 {
		t.Fatalf("删除条数 = %d, want 5", n)
	}
	var left []TaskLog
	if err := testDB.Where("task_id IN ?", []string{"del-old", "del-new"}).Find(&left).Error; err != nil {
		t.Fatalf("查询剩余失败: %v", err)
	}
	if len(left) != 3 {
		t.Errorf("应剩 3 条新日志, 实际 %d", len(left))
	}

	// limit 生效：再灌 4 条旧日志，单批 limit=2 只删 2 条
	for i := 0; i < 4; i++ {
		seedTaskLog(t, "del-old2", LogLevelInfo, "recording", "旧日志", old)
	}
	n2, err := ls.DeleteOlderThan(ctx, now.Add(-24*time.Hour), 2)
	if err != nil {
		t.Fatalf("DeleteOlderThan(limit=2) 失败: %v", err)
	}
	if n2 != 2 {
		t.Fatalf("limit=2 时删除条数 = %d, want 2", n2)
	}
}
