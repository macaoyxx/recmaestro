// finishTask 健壮性测试（P-2 修复）：损坏段隔离、时长对账前置校验、段保留不静默丢失。
package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"recmaestro/internal/recorder"
	"recmaestro/internal/store"
)

// 登记段记录（模拟录制期间扫描上报），返回段描述。
func putSegment(t *testing.T, env *testEnv, taskID, dir string, seq int, corruptContent string) recorder.Segment {
	t.Helper()
	path := filepath.Join(dir, segName(seq))
	if corruptContent == "" {
		makeTSSegment(t, path)
	} else if err := os.WriteFile(path, []byte(corruptContent), 0o644); err != nil {
		t.Fatalf("写段 %d 失败: %v", seq, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 段 %d 失败: %v", seq, err)
	}
	if err := env.w.files.CreateSegmentIfAbsent(context.Background(), &store.TaskFile{
		TaskID: taskID, NodeID: env.w.nodeID, Filename: segName(seq),
		Type: store.FileTypeSegment, Path: path, Seq: seq, Status: "ok",
	}); err != nil {
		t.Fatalf("登记段 %d 失败: %v", seq, err)
	}
	return recorder.Segment{Seq: seq, Path: path, Size: info.Size()}
}

// TestFinishTaskIsolatesCorruptSegment 清单中间混入损坏段（异常退出截断）：
// 损坏段被隔离（改名 .corrupt + DB 标记）不进合并清单，健康段正常合并、
// 时长对账通过后任务完成；损坏段文件保留取证。
func TestFinishTaskIsolatesCorruptSegment(t *testing.T) {
	env := newTestWorker(t)
	id := testTaskPrefix + "finish-corrupt"
	task := newTestTask(t, env, id, store.TaskRunning, nil)
	dir := t.TempDir()

	segs := []recorder.Segment{
		putSegment(t, env, id, dir, 0, ""),
		putSegment(t, env, id, dir, 1, "truncated garbage, not a mpegts stream"),
		putSegment(t, env, id, dir, 2, ""),
	}
	env.w.finishTask(context.Background(), task, dir, segs)

	after, err := env.w.tasks.GetByID(context.Background(), id)
	if err != nil || after == nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if after.Status != store.TaskCompleted {
		t.Fatalf("任务状态 = %s（err=%s）, 期望 completed：损坏段隔离后健康段应正常完成",
			after.Status, after.ErrorMsg)
	}

	// 产物时长 ≈ 两个健康段（各 1s），证明未丢健康内容
	d, err := env.w.rec.ProbeDuration(context.Background(), filepath.Join(dir, "output.mp4"))
	if err != nil {
		t.Fatalf("探测产物失败: %v", err)
	}
	if d < 1.6 || d > 2.5 {
		t.Errorf("产物时长 = %.2fs, 期望约 2s（两健康段无损拼接）", d)
	}

	// 损坏段隔离保留取证；健康段按配置完成清理
	if _, err := os.Stat(filepath.Join(dir, segName(1)+".corrupt")); err != nil {
		t.Errorf("损坏段应隔离为 .corrupt 保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, segName(0))); err == nil {
		t.Errorf("健康段 %s 应在完成对账后清理", segName(0))
	}

	// DB 标记 corrupt，不再计入活动段清单
	var f store.TaskFile
	if err := env.db.Where("task_id = ? AND seq = ?", id, 1).First(&f).Error; err != nil {
		t.Fatalf("查损坏段记录失败: %v", err)
	}
	if f.Status != "corrupt" {
		t.Errorf("损坏段记录 status = %s, 期望 corrupt", f.Status)
	}
}

// TestFinishTaskAllCorruptKeepsSegments 全部段损坏：不合并、不清理、置 merge_failed
// 保留 TS 段人工介入，绝不静默置 completed。
func TestFinishTaskAllCorruptKeepsSegments(t *testing.T) {
	env := newTestWorker(t)
	id := testTaskPrefix + "finish-allcorrupt"
	task := newTestTask(t, env, id, store.TaskRunning, nil)
	dir := t.TempDir()

	segs := []recorder.Segment{
		putSegment(t, env, id, dir, 0, "garbage bytes one"),
		putSegment(t, env, id, dir, 1, "garbage bytes two"),
	}
	env.w.finishTask(context.Background(), task, dir, segs)

	after, err := env.w.tasks.GetByID(context.Background(), id)
	if err != nil || after == nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if after.Status != store.TaskMergeFailed {
		t.Fatalf("任务状态 = %s, 期望 merge_failed（全部段损坏须显式失败）", after.Status)
	}
	if after.ErrorCode != "segments_corrupt" {
		t.Errorf("error_code = %s, 期望 segments_corrupt", after.ErrorCode)
	}
	// 段文件隔离保留（.corrupt），未被清理
	for _, seq := range []int{0, 1} {
		if _, err := os.Stat(filepath.Join(dir, segName(seq)+".corrupt")); err != nil {
			t.Errorf("损坏段 %d 应隔离保留: %v", seq, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "output.mp4")); err == nil {
		t.Errorf("全部段损坏时不应产出 output.mp4")
	}
}

// TestNextStartSeq 续录起始段号 = max(DB 最大段号, 磁盘最大段号) + 1：
// 宕机落在段扫描周期内时 DB 落后于磁盘，只看 DB 会算小导致覆盖重写磁盘段文件。
func TestNextStartSeq(t *testing.T) {
	env := newTestWorker(t)
	id := testTaskPrefix + "seq-align"
	newTestTask(t, env, id, store.TaskRunning, nil)
	dir := t.TempDir()
	ctx := context.Background()

	// 全空 → 1
	if got := env.w.nextStartSeq(ctx, id, dir); got != 1 {
		t.Errorf("空目录 nextStartSeq = %d, 期望 1", got)
	}

	// DB 记 seq=2（磁盘无文件）→ 3
	if err := env.w.files.CreateSegmentIfAbsent(ctx, &store.TaskFile{
		TaskID: id, NodeID: env.w.nodeID, Filename: segName(2),
		Type: store.FileTypeSegment, Path: filepath.Join(dir, segName(2)), Seq: 2, Status: "ok",
	}); err != nil {
		t.Fatalf("登记段失败: %v", err)
	}
	if got := env.w.nextStartSeq(ctx, id, dir); got != 3 {
		t.Errorf("仅 DB 记录时 nextStartSeq = %d, 期望 3", got)
	}

	// 磁盘出现 seq=3/5（宕机窗口内 DB 没记上）→ 取磁盘最大 5 → 6，避免覆盖重写
	for _, seq := range []int{3, 5} {
		if err := os.WriteFile(filepath.Join(dir, segName(seq)), []byte("x"), 0o644); err != nil {
			t.Fatalf("造磁盘段 %d 失败: %v", seq, err)
		}
	}
	if got := env.w.nextStartSeq(ctx, id, dir); got != 6 {
		t.Errorf("磁盘领先 DB 时 nextStartSeq = %d, 期望 6（max(DB,磁盘)+1）", got)
	}
}
