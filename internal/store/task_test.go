package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// newTestTask 构造一个测试任务，状态由调用方指定。
func newTestTask(id string, status TaskStatus) *Task {
	return &Task{
		ID:             id,
		Name:           "测试直播间-" + id,
		StreamURL:      "rtmp://example.com/live/" + id,
		Status:         status,
		Priority:       5,
		SegmentSeconds: 30,
		MaxRetry:       3,
	}
}

// TestTaskCreateAndGet 验证任务插入与按 ID 查询的字段回读，以及不存在时返回 (nil, nil)。
func TestTaskCreateAndGet(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	start := time.Now().Add(time.Hour).Truncate(time.Second)
	end := start.Add(2 * time.Hour)
	var uid uint64 = 42
	want := &Task{
		ID:             "task-get-1",
		Name:           "晚会主会场",
		CreatedBy:      &uid,
		StreamURL:      "rtmp://example.com/live/main",
		StartTime:      &start,
		EndTime:        &end,
		Status:         TaskPending,
		Priority:       9,
		SegmentSeconds: 60,
		MaxRetry:       2,
	}
	if err := ts.Create(ctx, want); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	got, err := ts.GetByID(ctx, "task-get-1")
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if got == nil {
		t.Fatal("任务应存在，实际返回 nil")
	}
	if got.Name != want.Name || got.StreamURL != want.StreamURL {
		t.Errorf("基础字段不匹配: name=%q stream_url=%q", got.Name, got.StreamURL)
	}
	if got.CreatedBy == nil || *got.CreatedBy != uid {
		t.Errorf("created_by 不匹配: %+v", got.CreatedBy)
	}
	if got.StartTime == nil || !sameSecond(*got.StartTime, start) {
		t.Errorf("start_time 不匹配: %+v", got.StartTime)
	}
	if got.EndTime == nil || !sameSecond(*got.EndTime, end) {
		t.Errorf("end_time 不匹配: %+v", got.EndTime)
	}
	if got.Status != TaskPending || got.Priority != 9 || got.SegmentSeconds != 60 || got.MaxRetry != 2 {
		t.Errorf("配置字段不匹配: %+v", got)
	}
	if got.RetryCount != 0 {
		t.Errorf("新任务 retry_count 应为 0，实际 %d", got.RetryCount)
	}

	// 不存在的 ID 约定返回 (nil, nil) 而非报错
	miss, err := ts.GetByID(ctx, "task-not-exist")
	if err != nil {
		t.Fatalf("查询不存在任务应无错误，实际: %v", err)
	}
	if miss != nil {
		t.Errorf("查询不存在任务应返回 nil，实际 %+v", miss)
	}
}

// TestTaskListFiltersAndPaging 验证 List 的状态筛选、名称/地址模糊搜索、组合筛选与分页排序。
func TestTaskListFiltersAndPaging(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	base := time.Now().Truncate(time.Second)
	// 三条任务：created_at 各差 1 秒保证排序稳定；t2 的地址含 "two"、名称不含关键词
	seed := []struct {
		id     string
		name   string
		url    string
		status TaskStatus
		ageSec int // 距 now 的秒数，越大越早创建
	}{
		{"task-l-1", "晚会主会场", "rtmp://a/live/one", TaskQueued, 3},
		{"task-l-2", "游戏分会场", "rtmp://b/live/two", TaskCompleted, 2},
		{"task-l-3", "晚会分会场", "rtmp://c/live/three", TaskPending, 1},
	}
	for _, s := range seed {
		task := newTestTask(s.id, s.status)
		task.Name = s.name
		task.StreamURL = s.url
		task.CreatedAt = base.Add(-time.Duration(s.ageSec) * time.Second)
		if err := ts.Create(ctx, task); err != nil {
			t.Fatalf("创建任务 %s 失败: %v", s.id, err)
		}
	}

	ids := func(tasks []Task) []string {
		out := make([]string, 0, len(tasks))
		for _, x := range tasks {
			out = append(out, x.ID)
		}
		return out
	}
	eq := func(a, b []string) bool {
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

	cases := []struct {
		name     string
		status   string
		keyword  string
		page     int
		pageSize int
		wantIDs  []string
		wantTot  int64
	}{
		// 组合筛选是回归用例：关键词若只命中其他状态任务的地址，不得绕过状态条件
		{"状态+关键词只命中他状态地址", "queued", "two", 1, 20, nil, 0},
		{"状态+关键词命中本状态名称", "queued", "晚会", 1, 20, []string{"task-l-1"}, 1},
		{"仅状态筛选", "completed", "", 1, 20, []string{"task-l-2"}, 1},
		{"关键词命中多个名称", "", "晚会", 1, 20, []string{"task-l-3", "task-l-1"}, 2},
		{"关键词命中地址", "", "two", 1, 20, []string{"task-l-2"}, 1},
		{"关键词无命中", "", "不存在的关键词", 1, 20, nil, 0},
		{"无条件按创建时间倒序", "", "", 1, 20, []string{"task-l-3", "task-l-2", "task-l-1"}, 3},
		{"分页第 1 页", "", "", 1, 2, []string{"task-l-3", "task-l-2"}, 3},
		{"分页第 2 页", "", "", 2, 2, []string{"task-l-1"}, 3},
		{"非法分页参数走默认值", "", "", 0, 0, []string{"task-l-3", "task-l-2", "task-l-1"}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tasks, total, err := ts.List(ctx, c.status, c.keyword, c.page, c.pageSize)
			if err != nil {
				t.Fatalf("List 失败: %v", err)
			}
			if total != c.wantTot {
				t.Errorf("total = %d, want %d", total, c.wantTot)
			}
			if got := ids(tasks); !eq(got, c.wantIDs) {
				t.Errorf("结果集 = %v, want %v", got, c.wantIDs)
			}
		})
	}
}

// TestTaskUpdateStatusAndHeartbeat 验证无条件状态更新与心跳时间写入。
func TestTaskUpdateStatusAndHeartbeat(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	if err := ts.Create(ctx, newTestTask("task-uh-1", TaskPending)); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := ts.UpdateStatus(ctx, "task-uh-1", TaskQueued); err != nil {
		t.Fatalf("更新状态失败: %v", err)
	}
	hb := time.Now().Truncate(time.Second)
	if err := ts.Heartbeat(ctx, "task-uh-1", hb); err != nil {
		t.Fatalf("更新心跳失败: %v", err)
	}

	got, err := ts.GetByID(ctx, "task-uh-1")
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if got.Status != TaskQueued {
		t.Errorf("status = %q, want %q", got.Status, TaskQueued)
	}
	if got.LastHeartbeatAt == nil || !sameSecond(*got.LastHeartbeatAt, hb) {
		t.Errorf("last_heartbeat_at 不匹配: %+v", got.LastHeartbeatAt)
	}
}

// TestTaskListByStatusAndCount 验证按状态列全量与按状态分组计数。
func TestTaskListByStatusAndCount(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	for _, tsk := range []*Task{
		newTestTask("task-sc-1", TaskQueued),
		newTestTask("task-sc-2", TaskQueued),
		newTestTask("task-sc-3", TaskRunning),
		newTestTask("task-sc-4", TaskCompleted),
	} {
		if err := ts.Create(ctx, tsk); err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
	}

	queued, err := ts.ListByStatus(ctx, TaskQueued)
	if err != nil {
		t.Fatalf("ListByStatus 失败: %v", err)
	}
	if len(queued) != 2 {
		t.Errorf("queued 任务数 = %d, want 2", len(queued))
	}

	counts, err := ts.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus 失败: %v", err)
	}
	want := map[string]int64{string(TaskQueued): 2, string(TaskRunning): 1, string(TaskCompleted): 1}
	if len(counts) != len(want) {
		t.Errorf("分组数 = %d (%v), want %d", len(counts), counts, len(want))
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("counts[%q] = %d, want %d", k, counts[k], v)
		}
	}
}

// TestTaskMarkQueued 表驱动验证条件置 queued：仅 pending/record_failed/interrupted 允许，
// 防止把 running/queued 等状态打回 queued 造成双录。
func TestTaskMarkQueued(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	cases := []struct {
		name   string
		status TaskStatus
		want   bool
	}{
		{"pending 允许入队", TaskPending, true},
		{"record_failed 允许入队", TaskRecordFailed, true},
		{"interrupted 允许入队", TaskInterrupted, true},
		{"已 queued 不允许再次入队", TaskQueued, false},
		{"running 不允许入队", TaskRunning, false},
		{"recovering 不允许入队", TaskRecovering, false},
		{"completed 不允许入队", TaskCompleted, false},
		{"merge_failed 不允许入队", TaskMergeFailed, false},
		{"cancelled 不允许入队", TaskCancelled, false},
		{"aborted 不允许入队", TaskAborted, false},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := fmt.Sprintf("task-mq-%d", i)
			if err := ts.Create(ctx, newTestTask(id, c.status)); err != nil {
				t.Fatalf("创建任务失败: %v", err)
			}
			ok, err := ts.MarkQueued(ctx, id)
			if err != nil {
				t.Fatalf("MarkQueued 失败: %v", err)
			}
			if ok != c.want {
				t.Errorf("MarkQueued 返回 %v, want %v", ok, c.want)
			}
			got, err := ts.GetByID(ctx, id)
			if err != nil {
				t.Fatalf("查询任务失败: %v", err)
			}
			wantStatus := c.status
			if c.want {
				wantStatus = TaskQueued
			}
			if got.Status != wantStatus {
				t.Errorf("终态 status = %q, want %q", got.Status, wantStatus)
			}
		})
	}
}

// TestTaskClaimSingle 验证单个 Claim 的语义：queued 才能认领成功，认领后置 running 并绑定 worker；
// 第二次认领（状态已是 running）必须失败。
func TestTaskClaimSingle(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	if err := ts.Create(ctx, newTestTask("task-claim-1", TaskQueued)); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	now := time.Now().Truncate(time.Second)
	ok, err := ts.Claim(ctx, "task-claim-1", "worker-a", now)
	if err != nil {
		t.Fatalf("Claim 失败: %v", err)
	}
	if !ok {
		t.Fatal("queued 任务首次 Claim 应成功")
	}

	got, err := ts.GetByID(ctx, "task-claim-1")
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if got.Status != TaskRunning {
		t.Errorf("status = %q, want %q", got.Status, TaskRunning)
	}
	if got.WorkerID != "worker-a" {
		t.Errorf("worker_id = %q, want %q", got.WorkerID, "worker-a")
	}
	if got.ActualStartTime == nil || !sameSecond(*got.ActualStartTime, now) {
		t.Errorf("actual_start_time 不匹配: %+v", got.ActualStartTime)
	}

	// 已被认领（running）后再次 Claim 必须失败
	ok2, err := ts.Claim(ctx, "task-claim-1", "worker-b", now)
	if err != nil {
		t.Fatalf("第二次 Claim 失败: %v", err)
	}
	if ok2 {
		t.Error("running 任务再次 Claim 不应成功")
	}
	got2, _ := ts.GetByID(ctx, "task-claim-1")
	if got2.WorkerID != "worker-a" {
		t.Errorf("worker_id 被覆盖为 %q, 应保持 worker-a", got2.WorkerID)
	}
}

// TestTaskClaimConcurrent 并发用例：20 个 goroutine 同时 Claim 同一条 queued 任务，
// 依赖单条条件 UPDATE 的行锁语义，成功者必须恰好为 1，其余 19 个全部失败。
func TestTaskClaimConcurrent(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	const n = 20
	const taskID = "task-claim-concurrent"
	if err := ts.Create(ctx, newTestTask(taskID, TaskQueued)); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	workerOf := func(i int) string { return fmt.Sprintf("worker-%02d", i) }

	results := make([]bool, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 统一发令，尽量让 20 个请求同时打向数据库
			results[i], errs[i] = ts.Claim(ctx, taskID, workerOf(i), now)
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	winnerWorker := ""
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("goroutine %d Claim 返回错误: %v", i, errs[i])
		}
		if results[i] {
			winners++
			winnerWorker = workerOf(i)
		}
	}
	if winners != 1 {
		t.Errorf("并发 Claim 成功数 = %d, want 恰好 1", winners)
	}

	got, err := ts.GetByID(ctx, taskID)
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if got.Status != TaskRunning {
		t.Errorf("并发认领后 status = %q, want %q", got.Status, TaskRunning)
	}
	if winnerWorker != "" && got.WorkerID != winnerWorker {
		t.Errorf("worker_id = %q, want 认领成功者 %q", got.WorkerID, winnerWorker)
	}
	if got.ActualStartTime == nil || !sameSecond(*got.ActualStartTime, now) {
		t.Errorf("actual_start_time 不匹配: %+v", got.ActualStartTime)
	}
}

// TestTaskCompleteAndFail 验证完成与失败落库字段。
func TestTaskCompleteAndFail(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	end := time.Now().Truncate(time.Second)
	if err := ts.Create(ctx, newTestTask("task-cf-1", TaskRunning)); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := ts.Complete(ctx, "task-cf-1", "/data/out/task-cf-1.mp4", end); err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	got, _ := ts.GetByID(ctx, "task-cf-1")
	if got.Status != TaskCompleted {
		t.Errorf("status = %q, want %q", got.Status, TaskCompleted)
	}
	if got.OutputPath != "/data/out/task-cf-1.mp4" {
		t.Errorf("output_path = %q 不匹配", got.OutputPath)
	}
	if got.ActualEndTime == nil || !sameSecond(*got.ActualEndTime, end) {
		t.Errorf("actual_end_time 不匹配: %+v", got.ActualEndTime)
	}

	fails := []struct {
		id     string
		status TaskStatus
	}{
		{"task-cf-2", TaskRecordFailed},
		{"task-cf-3", TaskMergeFailed},
	}
	for i, f := range fails {
		if err := ts.Create(ctx, newTestTask(f.id, TaskRunning)); err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
		if err := ts.Fail(ctx, f.id, f.status, "E_TEST", "测试失败原因"); err != nil {
			t.Fatalf("Fail 失败: %v", err)
		}
		got, _ := ts.GetByID(ctx, f.id)
		if got.Status != f.status {
			t.Errorf("用例 %d status = %q, want %q", i, got.Status, f.status)
		}
		if got.ErrorCode != "E_TEST" || got.ErrorMsg != "测试失败原因" {
			t.Errorf("用例 %d 错误信息不匹配: code=%q msg=%q", i, got.ErrorCode, got.ErrorMsg)
		}
	}
}

// TestTaskFailWithRetry 验证仅 running 可标记失败并原子自增 retry_count，且返回自增后的新值。
func TestTaskFailWithRetry(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	if err := ts.Create(ctx, newTestTask("task-fr-1", TaskRunning)); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	newCount, ok, err := ts.FailWithRetry(ctx, "task-fr-1", "E_RECORD", "流中断")
	if err != nil || !ok {
		t.Fatalf("running 任务 FailWithRetry 应成功: ok=%v err=%v", ok, err)
	}
	if newCount != 1 {
		t.Errorf("首次失败 retry_count = %d, want 1", newCount)
	}
	got, _ := ts.GetByID(ctx, "task-fr-1")
	if got.Status != TaskRecordFailed || got.RetryCount != 1 {
		t.Errorf("落库结果不匹配: status=%q retry_count=%d", got.Status, got.RetryCount)
	}

	// record_failed 状态再调用应失败（防覆盖），retry_count 不再自增
	newCount2, ok2, err := ts.FailWithRetry(ctx, "task-fr-1", "E_RECORD", "流中断")
	if err != nil {
		t.Fatalf("FailWithRetry 失败: %v", err)
	}
	if ok2 {
		t.Error("非 running 任务 FailWithRetry 不应成功")
	}
	if newCount2 != 0 {
		t.Errorf("失败时返回 retry_count = %d, want 0", newCount2)
	}
	got2, _ := ts.GetByID(ctx, "task-fr-1")
	if got2.RetryCount != 1 {
		t.Errorf("失败后 retry_count = %d, 应保持 1", got2.RetryCount)
	}
}

// TestTaskRetry 表驱动验证重试条件：仅 record_failed/interrupted 可重试并入队，retry_count 随之自增。
func TestTaskRetry(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	cases := []struct {
		name      string
		status    TaskStatus
		want      bool
		wantCount int
	}{
		{"record_failed 可重试", TaskRecordFailed, true, 1},
		{"interrupted 可重试", TaskInterrupted, true, 1},
		{"pending 不可重试", TaskPending, false, 0},
		{"queued 不可重试", TaskQueued, false, 0},
		{"running 不可重试", TaskRunning, false, 0},
		{"completed 不可重试", TaskCompleted, false, 0},
		{"cancelled 不可重试", TaskCancelled, false, 0},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := fmt.Sprintf("task-rt-%d", i)
			if err := ts.Create(ctx, newTestTask(id, c.status)); err != nil {
				t.Fatalf("创建任务失败: %v", err)
			}
			ok, count, err := ts.Retry(ctx, id)
			if err != nil {
				t.Fatalf("Retry 失败: %v", err)
			}
			if ok != c.want {
				t.Errorf("Retry 返回 %v, want %v", ok, c.want)
			}
			if ok && count != c.wantCount {
				t.Errorf("Retry 返回的 retry_count = %d, want %d", count, c.wantCount)
			}
			got, _ := ts.GetByID(ctx, id)
			if got.RetryCount != c.wantCount {
				t.Errorf("retry_count = %d, want %d", got.RetryCount, c.wantCount)
			}
			wantStatus := c.status
			if c.want {
				wantStatus = TaskQueued
			}
			if got.Status != wantStatus {
				t.Errorf("终态 status = %q, want %q", got.Status, wantStatus)
			}
			// 连续第二次重试：queued 状态不允许，防止重复入队
			if c.want {
				ok2, count2, err := ts.Retry(ctx, id)
				if err != nil {
					t.Fatalf("第二次 Retry 失败: %v", err)
				}
				if ok2 {
					t.Error("queued 状态第二次 Retry 不应成功")
				}
				if count2 != 0 {
					t.Errorf("第二次 Retry 返回 count = %d, want 0", count2)
				}
			}
		})
	}
}

// TestTaskCancel 表驱动验证取消条件：未完成的（pending/queued/running/recovering）可取消，终态不可。
func TestTaskCancel(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	cases := []struct {
		name   string
		status TaskStatus
		want   bool
	}{
		{"pending 可取消", TaskPending, true},
		{"queued 可取消", TaskQueued, true},
		{"running 可取消", TaskRunning, true},
		{"recovering 可取消", TaskRecovering, true},
		{"completed 不可取消", TaskCompleted, false},
		{"record_failed 不可取消", TaskRecordFailed, false},
		{"merge_failed 不可取消", TaskMergeFailed, false},
		{"interrupted 不可取消", TaskInterrupted, false},
		{"cancelled 不可重复取消", TaskCancelled, false},
		{"aborted 不可取消", TaskAborted, false},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := fmt.Sprintf("task-cc-%d", i)
			if err := ts.Create(ctx, newTestTask(id, c.status)); err != nil {
				t.Fatalf("创建任务失败: %v", err)
			}
			ok, err := ts.Cancel(ctx, id)
			if err != nil {
				t.Fatalf("Cancel 失败: %v", err)
			}
			if ok != c.want {
				t.Errorf("Cancel 返回 %v, want %v", ok, c.want)
			}
			got, _ := ts.GetByID(ctx, id)
			wantStatus := c.status
			if c.want {
				wantStatus = TaskCancelled
			}
			if got.Status != wantStatus {
				t.Errorf("终态 status = %q, want %q", got.Status, wantStatus)
			}
		})
	}
}

// TestTaskRecoverFlow 验证心跳超时恢复链路：running→recovering→running（续租），
// 以及长期未恢复时 recovering→interrupted→MarkQueued 重新入队。
func TestTaskRecoverFlow(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	id := "task-rv-1"
	if err := ts.Create(ctx, newTestTask(id, TaskQueued)); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	// 认领绑定 worker-rv，进入 running
	if ok, err := ts.Claim(ctx, id, "worker-rv", time.Now().Truncate(time.Second)); err != nil || !ok {
		t.Fatalf("Claim 失败: ok=%v err=%v", ok, err)
	}

	status := func() TaskStatus {
		got, err := ts.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("查询任务失败: %v", err)
		}
		return got.Status
	}

	// MarkInterrupted 对非 recovering 无效
	if err := ts.MarkInterrupted(ctx, id); err != nil {
		t.Fatalf("MarkInterrupted 失败: %v", err)
	}
	if s := status(); s != TaskRunning {
		t.Errorf("running 状态 MarkInterrupted 后 status = %q, 应保持 running", s)
	}

	// 心跳超时 → recovering
	if err := ts.MarkRecovering(ctx, id); err != nil {
		t.Fatalf("MarkRecovering 失败: %v", err)
	}
	if s := status(); s != TaskRecovering {
		t.Errorf("MarkRecovering 后 status = %q, want %q", s, TaskRecovering)
	}

	// 按 worker 查待恢复任务：本人名下 1 条，其他 worker 名下 0 条
	tasks, err := ts.ListRecoveringByWorker(ctx, "worker-rv")
	if err != nil {
		t.Fatalf("ListRecoveringByWorker 失败: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != id {
		t.Errorf("worker-rv 名下 recovering 任务不匹配: %+v", tasks)
	}
	tasks, err = ts.ListRecoveringByWorker(ctx, "worker-other")
	if err != nil {
		t.Fatalf("ListRecoveringByWorker 失败: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("其他 worker 名下 recovering 任务 = %d, want 0", len(tasks))
	}

	// worker 续租 → running
	if err := ts.Resume(ctx, id); err != nil {
		t.Fatalf("Resume 失败: %v", err)
	}
	if s := status(); s != TaskRunning {
		t.Errorf("Resume 后 status = %q, want %q", s, TaskRunning)
	}

	// 长期未恢复 → interrupted，之后允许 MarkQueued 重新入队
	if err := ts.MarkRecovering(ctx, id); err != nil {
		t.Fatalf("MarkRecovering 失败: %v", err)
	}
	if s := status(); s != TaskRecovering {
		t.Errorf("MarkRecovering 后 status = %q, want %q", s, TaskRecovering)
	}
	if err := ts.MarkInterrupted(ctx, id); err != nil {
		t.Fatalf("MarkInterrupted 失败: %v", err)
	}
	if s := status(); s != TaskInterrupted {
		t.Errorf("MarkInterrupted 后 status = %q, want %q", s, TaskInterrupted)
	}
	ok, err := ts.MarkQueued(ctx, id)
	if err != nil || !ok {
		t.Fatalf("interrupted 任务 MarkQueued 应成功: ok=%v err=%v", ok, err)
	}
	if s := status(); s != TaskQueued {
		t.Errorf("MarkQueued 后 status = %q, want %q", s, TaskQueued)
	}
}

// TestTaskAbort 表驱动验证中止条件：仅 running/recovering 可中止。
func TestTaskAbort(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	cases := []struct {
		name   string
		status TaskStatus
		want   bool
	}{
		{"running 可中止", TaskRunning, true},
		{"recovering 可中止", TaskRecovering, true},
		{"pending 不可中止", TaskPending, false},
		{"queued 不可中止", TaskQueued, false},
		{"completed 不可中止", TaskCompleted, false},
		{"cancelled 不可中止", TaskCancelled, false},
		{"aborted 不可重复中止", TaskAborted, false},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := fmt.Sprintf("task-ab-%d", i)
			if err := ts.Create(ctx, newTestTask(id, c.status)); err != nil {
				t.Fatalf("创建任务失败: %v", err)
			}
			ok, err := ts.Abort(ctx, id)
			if err != nil {
				t.Fatalf("Abort 失败: %v", err)
			}
			if ok != c.want {
				t.Errorf("Abort 返回 %v, want %v", ok, c.want)
			}
			got, _ := ts.GetByID(ctx, id)
			wantStatus := c.status
			if c.want {
				wantStatus = TaskAborted
			}
			if got.Status != wantStatus {
				t.Errorf("终态 status = %q, want %q", got.Status, wantStatus)
			}
		})
	}
}

// TestTaskUpdateConditional 验证编辑任务仅允许非运行状态：running/recovering 更新被拒绝且字段不变。
func TestTaskUpdateConditional(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	if err := ts.Create(ctx, newTestTask("task-up-1", TaskPending)); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	ok, err := ts.Update(ctx, "task-up-1", map[string]interface{}{"name": "改名后的任务"})
	if err != nil || !ok {
		t.Fatalf("pending 任务 Update 应成功: ok=%v err=%v", ok, err)
	}
	got, _ := ts.GetByID(ctx, "task-up-1")
	if got.Name != "改名后的任务" {
		t.Errorf("name = %q, want %q", got.Name, "改名后的任务")
	}

	if err := ts.Create(ctx, newTestTask("task-up-2", TaskRunning)); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	ok, err = ts.Update(ctx, "task-up-2", map[string]interface{}{"name": "不应生效"})
	if err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if ok {
		t.Error("running 任务 Update 不应成功")
	}
	got2, _ := ts.GetByID(ctx, "task-up-2")
	if got2.Name == "不应生效" {
		t.Error("running 任务名称不应被更新")
	}

	// 不存在的任务更新应返回 false
	ok, err = ts.Update(ctx, "task-not-exist", map[string]interface{}{"name": "x"})
	if err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if ok {
		t.Error("不存在任务的 Update 不应返回 true")
	}
}

// TestTaskDeleteConditional 验证删除任务仅允许非运行状态：running 拒绝删除，pending 可删且删后查不到。
func TestTaskDeleteConditional(t *testing.T) {
	resetTables(t, "live_rec_tasks")
	ctx := context.Background()
	ts := NewTaskStore(testDB)

	if err := ts.Create(ctx, newTestTask("task-dl-1", TaskPending)); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := ts.Create(ctx, newTestTask("task-dl-2", TaskRunning)); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	ok, err := ts.Delete(ctx, "task-dl-2")
	if err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if ok {
		t.Error("running 任务 Delete 不应成功")
	}
	still, _ := ts.GetByID(ctx, "task-dl-2")
	if still == nil {
		t.Error("running 任务不应被删除")
	}

	ok, err = ts.Delete(ctx, "task-dl-1")
	if err != nil || !ok {
		t.Fatalf("pending 任务 Delete 应成功: ok=%v err=%v", ok, err)
	}
	gone, _ := ts.GetByID(ctx, "task-dl-1")
	if gone != nil {
		t.Error("删除后任务不应再查到")
	}

	// 再删一次（已不存在）应返回 false
	ok, err = ts.Delete(ctx, "task-dl-1")
	if err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if ok {
		t.Error("删除不存在的任务不应返回 true")
	}
}
