// 高并发-3 修复测试：收尾终态写库有界重试（不吞错）+ healLoop 对「终态写库失败
// 残留 running」任务的幂等自愈补写（主表/文件表最终一致）。
package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"recmaestro/internal/recorder"
	"recmaestro/internal/store"
)

// TestHealFinalizeConvergesStuckTask 定向复现高并发-3 现场并验证自愈收敛：
// 正常收尾完成后，人为把主表打回 running、删除产物记录（等价于 Complete/产物
// 记录写库被 Error 1040 吞掉的残留现场：日志表已 validated/completed、主表
// running、文件表无 output 记录），healFinalizePending 应幂等补写一致。
func TestHealFinalizeConvergesStuckTask(t *testing.T) {
	env := newTestWorker(t)
	ctx := context.Background()
	id := testTaskPrefix + "heal-stuck"
	task := newTestTask(t, env, id, store.TaskRunning, func(tk *store.Task) {
		tk.WorkerID = env.w.nodeID // 绑定本节点，才会被自愈扫描命中
	})

	// 产物目录必须在 RecordsDir/<id> 下（healTask 按此约定拼路径）
	dir := filepath.Join(env.w.cfg.Recorder.RecordsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建产物目录失败: %v", err)
	}
	segs := []recorder.Segment{
		putSegment(t, env, id, dir, 0, ""),
		putSegment(t, env, id, dir, 1, ""),
	}

	// 正常收尾（回归：重试 + 顺序调整后主路径不回归）
	env.w.finishTask(ctx, task, dir, segs)
	after, err := env.w.tasks.GetByID(ctx, id)
	if err != nil || after == nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if after.Status != store.TaskCompleted {
		t.Fatalf("正常收尾后状态 = %s, 期望 completed", after.Status)
	}

	// 人为制造事故现场：主表打回 running、删产物记录。
	// 日志表补一条 validated（TaskLogger 异步落库，测试基建不启动 flush 循环，
	// 直接落一条等价记录——它就是自愈判定的「时长对账已通过」铁证）
	if err := env.db.WithContext(ctx).Model(&store.Task{}).
		Where("id = ?", id).Update("status", store.TaskRunning).Error; err != nil {
		t.Fatalf("打回 running 失败: %v", err)
	}
	if err := env.db.WithContext(ctx).
		Where("task_id = ? AND type = ?", id, store.FileTypeOutput).
		Delete(&store.TaskFile{}).Error; err != nil {
		t.Fatalf("删产物记录失败: %v", err)
	}
	if err := env.db.WithContext(ctx).Create(&store.TaskLog{
		TaskID: id, Ts: time.Now(), Level: store.LogLevelInfo,
		Source: store.LogSourceWorker, Stage: store.LogStageValidated,
		Message: "产物时长 2.0s（段合计 2.0s，对账通过）", NodeID: env.w.nodeID,
	}).Error; err != nil {
		t.Fatalf("补 validated 日志失败: %v", err)
	}

	// 自愈一轮：主表 completed、文件表补回唯一一条 output 记录、产物文件仍在
	env.w.healFinalizePending(ctx)

	after, err = env.w.tasks.GetByID(ctx, id)
	if err != nil || after == nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if after.Status != store.TaskCompleted {
		t.Fatalf("自愈后状态 = %s, 期望 completed", after.Status)
	}
	if after.OutputPath == "" {
		t.Errorf("自愈后 output_path 不应为空")
	}
	var outputs []store.TaskFile
	if err := env.db.WithContext(ctx).
		Where("task_id = ? AND type = ?", id, store.FileTypeOutput).
		Find(&outputs).Error; err != nil {
		t.Fatalf("查产物记录失败: %v", err)
	}
	if len(outputs) != 1 {
		t.Fatalf("产物记录应恰好 1 条（幂等补写不产生重复行），实际 %d", len(outputs))
	}
	if _, err := env.w.rec.ProbeDuration(ctx, filepath.Join(dir, "output.mp4")); err != nil {
		t.Errorf("自愈后产物文件应保留且可探测: %v", err)
	}

	// 幂等性：再扫一轮不产生新变化（completed 不再命中 ListRunningByWorker）
	env.w.healFinalizePending(ctx)
	var outputs2 []store.TaskFile
	if err := env.db.WithContext(ctx).
		Where("task_id = ? AND type = ?", id, store.FileTypeOutput).
		Find(&outputs2).Error; err != nil {
		t.Fatalf("复查产物记录失败: %v", err)
	}
	if len(outputs2) != 1 {
		t.Errorf("重复自愈不应新增产物记录，实际 %d 条", len(outputs2))
	}
}

// TestHealSkipsNonCandidates 自愈判定的防误伤面：in-flight 任务（录制/收尾进行中）、
// 无 validated 证据的任务（可能是合并中被杀）、磁盘无产物的任务（走宕机恢复续录）
// 都不得被补写，保持 running。
func TestHealSkipsNonCandidates(t *testing.T) {
	env := newTestWorker(t)
	ctx := context.Background()

	mk := func(suffix string, prepare func(id, dir string)) {
		id := testTaskPrefix + "heal-skip-" + suffix
		newTestTask(t, env, id, store.TaskRunning, func(tk *store.Task) {
			tk.WorkerID = env.w.nodeID
		})
		dir := filepath.Join(env.w.cfg.Recorder.RecordsDir, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("建产物目录失败: %v", err)
		}
		prepare(id, dir)
	}

	// A：in-flight 占坑（产物与证据俱全也不许动）
	mk("inflight", func(id, dir string) {
		env.w.finishTask(ctx, mustGet(t, env, id), dir, []recorder.Segment{
			putSegment(t, env, id, dir, 0, ""),
		})
		env.db.WithContext(ctx).Model(&store.Task{}).Where("id = ?", id).
			Update("status", store.TaskRunning)
		env.db.WithContext(ctx).Create(&store.TaskLog{
			TaskID: id, Ts: time.Now(), Source: store.LogSourceWorker, Stage: store.LogStageValidated,
		})
		env.w.inflight.Store(id, struct{}{})
	})
	// B：无 validated 证据（合并中被杀的形态：产物在但未对账。TaskLogger 测试中
	// 不落库，finishTask 产生的 validated 日志天然不存在）
	mk("novalid", func(id, dir string) {
		env.w.finishTask(ctx, mustGet(t, env, id), dir, []recorder.Segment{
			putSegment(t, env, id, dir, 0, ""),
		})
		env.db.WithContext(ctx).Model(&store.Task{}).Where("id = ?", id).
			Update("status", store.TaskRunning)
		env.db.WithContext(ctx).
			Where("task_id = ? AND type = ?", id, store.FileTypeOutput).
			Delete(&store.TaskFile{})
	})
	// C：磁盘无产物（未走到合并，走宕机恢复续录）
	mk("nooutput", func(id, dir string) {
		putSegment(t, env, id, dir, 0, "")
		env.db.WithContext(ctx).Create(&store.TaskLog{
			TaskID: id, Ts: time.Now(), Source: store.LogSourceWorker, Stage: store.LogStageValidated,
		})
	})

	env.w.healFinalizePending(ctx)
	for _, suffix := range []string{"inflight", "novalid", "nooutput"} {
		id := testTaskPrefix + "heal-skip-" + suffix
		after, err := env.w.tasks.GetByID(ctx, id)
		if err != nil || after == nil {
			t.Fatalf("查询任务 %s 失败: %v", id, err)
		}
		if after.Status != store.TaskRunning {
			t.Errorf("任务 %s 状态 = %s, 期望保持 running（非自愈范畴不得补写）", suffix, after.Status)
		}
	}
}

// TestFinalizeDBOpRetrySemantics 收尾写库重试语义：瞬态错误（前几次失败）重试跨过；
// 恒败错误穷尽后如实返回（不吞错），尝试次数有界。
func TestFinalizeDBOpRetrySemantics(t *testing.T) {
	env := newTestWorker(t)
	ctx := context.Background()

	// 第 2 次成功（1 次退避 ≈1s）
	calls := 0
	err := env.w.finalizeDBOp(ctx, "测试操作", "wtest-op", func() error {
		calls++
		if calls == 1 {
			return errors.New("Error 1040: Too many connections")
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("瞬态错误应重试跨过: err=%v calls=%d, 期望 nil/2", err, calls)
	}

	// 恒败：穷尽 finalizeMaxAttempts 次后如实返回错误
	calls = 0
	err = env.w.finalizeDBOp(ctx, "测试操作", "wtest-op", func() error {
		calls++
		return errors.New("Error 1040: Too many connections")
	})
	if err == nil {
		t.Fatal("恒败错误不得被吞掉，必须返回")
	}
	if calls != finalizeMaxAttempts {
		t.Errorf("尝试次数 = %d, 期望 %d（有界）", calls, finalizeMaxAttempts)
	}
}

// mustGet 查询测试任务，失败即 Fatal。
func mustGet(t *testing.T, env *testEnv, id string) *store.Task {
	t.Helper()
	task, err := env.w.tasks.GetByID(context.Background(), id)
	if err != nil || task == nil {
		t.Fatalf("查询任务 %s 失败: %v", id, err)
	}
	return task
}
