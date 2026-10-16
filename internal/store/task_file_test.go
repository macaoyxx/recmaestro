package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// newSegmentFile 构造一个 TS 段文件记录，node 默认 node-a、时间可指定。
func newSegmentFile(taskID string, seq int, createdAt time.Time) *TaskFile {
	return &TaskFile{
		TaskID:     taskID,
		NodeID:     "node-a",
		Filename:   fmt.Sprintf("seg_%04d.ts", seq),
		Type:       FileTypeSegment,
		Path:       fmt.Sprintf("/data/%s/seg_%04d.ts", taskID, seq),
		Seq:        seq,
		SizeBytes:  int64(1024 * (seq + 1)),
		DurationMS: 30000,
		FileHash:   fmt.Sprintf("hash-%04d", seq),
		Status:     "ok",
		CreatedAt:  createdAt,
	}
}

// TestTaskFileCreateAndGet 验证文件记录插入与按 ID 回读，以及不存在时返回 (nil, nil)。
func TestTaskFileCreateAndGet(t *testing.T) {
	resetTables(t, "live_rec_task_files")
	ctx := context.Background()
	fs := NewTaskFileStore(testDB)

	created := time.Now().Add(-time.Minute).Truncate(time.Second)
	want := newSegmentFile("task-f-1", 0, created)
	if err := fs.Create(ctx, want); err != nil {
		t.Fatalf("创建文件记录失败: %v", err)
	}

	got, err := fs.GetByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("查询文件失败: %v", err)
	}
	if got == nil {
		t.Fatal("文件应存在，实际返回 nil")
	}
	if got.TaskID != want.TaskID || got.NodeID != want.NodeID || got.Filename != want.Filename {
		t.Errorf("基础字段不匹配: %+v", got)
	}
	if got.Type != FileTypeSegment || got.Path != want.Path || got.Seq != want.Seq {
		t.Errorf("段信息不匹配: %+v", got)
	}
	if got.SizeBytes != want.SizeBytes || got.DurationMS != want.DurationMS || got.FileHash != want.FileHash {
		t.Errorf("元数据不匹配: %+v", got)
	}
	if !sameSecond(got.CreatedAt, created) {
		t.Errorf("created_at 不匹配: %v", got.CreatedAt)
	}
	if got.DeletedAt != nil {
		t.Errorf("新记录 deleted_at 应为 NULL，实际 %+v", got.DeletedAt)
	}

	// 不存在的 ID 约定返回 (nil, nil)
	miss, err := fs.GetByID(ctx, 999999999)
	if err != nil {
		t.Fatalf("查询不存在文件应无错误，实际: %v", err)
	}
	if miss != nil {
		t.Errorf("查询不存在文件应返回 nil，实际 %+v", miss)
	}
}

// TestTaskFileCreateSegmentIfAbsent 验证段记录幂等上报：同 task_id+seq+type 已存在则跳过
// （续录时旧段重复上报不产生新行、已有数据不被覆盖），不同 seq/type/task 则正常插入。
func TestTaskFileCreateSegmentIfAbsent(t *testing.T) {
	resetTables(t, "live_rec_task_files")
	ctx := context.Background()
	fs := NewTaskFileStore(testDB)

	created := time.Now().Truncate(time.Second)

	// 首次上报：插入成功且回填自增 ID
	first := newSegmentFile("task-fa-1", 0, created)
	if err := fs.CreateSegmentIfAbsent(ctx, first); err != nil {
		t.Fatalf("首次上报失败: %v", err)
	}
	if first.ID == 0 {
		t.Fatal("首次上报应回填自增 ID")
	}

	// 重复上报同 task_id+seq+type：跳过，不产生新行、size 不被新值覆盖
	dup := newSegmentFile("task-fa-1", 0, created)
	dup.SizeBytes = 999999 // 故意带不同的 size，验证已存在记录不被改动
	if err := fs.CreateSegmentIfAbsent(ctx, dup); err != nil {
		t.Fatalf("重复上报失败: %v", err)
	}
	rows, err := fs.ListByTask(ctx, "task-fa-1")
	if err != nil {
		t.Fatalf("ListByTask 失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("重复上报后记录数 = %d, want 1", len(rows))
	}
	if rows[0].SizeBytes != first.SizeBytes {
		t.Errorf("已存在段的 size_bytes = %d, 应保持首次值 %d", rows[0].SizeBytes, first.SizeBytes)
	}
	if dup.ID != first.ID {
		t.Errorf("重复上报应复用已有记录 ID %d，实际 %d", first.ID, dup.ID)
	}

	// 不同 seq：正常插入
	if err := fs.CreateSegmentIfAbsent(ctx, newSegmentFile("task-fa-1", 1, created)); err != nil {
		t.Fatalf("上报 seq=1 失败: %v", err)
	}
	// 同 seq 但不同 type（output）：不受段幂等影响，正常插入
	out := &TaskFile{
		TaskID: "task-fa-1", NodeID: "node-a", Filename: "out.mp4",
		Type: FileTypeOutput, Path: "/data/task-fa-1/out.mp4", Seq: 1,
		SizeBytes: 4096, Status: "ok", CreatedAt: created,
	}
	if err := fs.CreateSegmentIfAbsent(ctx, out); err != nil {
		t.Fatalf("上报 output 失败: %v", err)
	}
	// 不同 task 同 seq：正常插入
	if err := fs.CreateSegmentIfAbsent(ctx, newSegmentFile("task-fa-2", 0, created)); err != nil {
		t.Fatalf("上报其他任务同 seq 失败: %v", err)
	}

	rows, err = fs.ListByTask(ctx, "task-fa-1")
	if err != nil {
		t.Fatalf("ListByTask 失败: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("task-fa-1 记录数 = %d, want 3（seq0 段 + seq1 段 + seq1 output）", len(rows))
	}
}

// TestTaskFileMarkDeletedByID 验证单条软删后：未删除列表不再包含，全量列表仍包含且 deleted_at 非空。
func TestTaskFileMarkDeletedByID(t *testing.T) {
	resetTables(t, "live_rec_task_files")
	ctx := context.Background()
	fs := NewTaskFileStore(testDB)

	created := time.Now().Truncate(time.Second)
	f1 := newSegmentFile("task-fd-1", 0, created)
	f2 := newSegmentFile("task-fd-1", 1, created)
	for _, f := range []*TaskFile{f1, f2} {
		if err := fs.Create(ctx, f); err != nil {
			t.Fatalf("创建文件记录失败: %v", err)
		}
	}

	if err := fs.MarkDeletedByID(ctx, f1.ID); err != nil {
		t.Fatalf("软删失败: %v", err)
	}

	active, err := fs.ListActiveByTask(ctx, "task-fd-1")
	if err != nil {
		t.Fatalf("ListActiveByTask 失败: %v", err)
	}
	if len(active) != 1 || active[0].ID != f2.ID {
		t.Errorf("未删除列表应只剩 seq=1，实际 %+v", active)
	}

	all, err := fs.ListByTask(ctx, "task-fd-1")
	if err != nil {
		t.Fatalf("ListByTask 失败: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("全量列表应仍为 2 条，实际 %d", len(all))
	}
	var deleted *TaskFile
	for i := range all {
		if all[i].ID == f1.ID {
			deleted = &all[i]
		}
	}
	if deleted == nil || deleted.DeletedAt == nil {
		t.Errorf("软删记录 deleted_at 应非空: %+v", deleted)
	}
}

// TestTaskFileUpdateSegmentSize 验证按 task_id+seq 精确修正段大小，且只命中 segment 类型。
func TestTaskFileUpdateSegmentSize(t *testing.T) {
	resetTables(t, "live_rec_task_files")
	ctx := context.Background()
	fs := NewTaskFileStore(testDB)

	created := time.Now().Truncate(time.Second)
	seg1 := newSegmentFile("task-us-1", 1, created)
	// 与段同 seq 的 output 记录：不应被 UpdateSegmentSize 改动
	out := &TaskFile{
		TaskID: "task-us-1", NodeID: "node-a", Filename: "out.mp4",
		Type: FileTypeOutput, Path: "/data/task-us-1/out.mp4", Seq: 1,
		SizeBytes: 4096, Status: "ok", CreatedAt: created,
	}
	for _, f := range []*TaskFile{seg1, out} {
		if err := fs.Create(ctx, f); err != nil {
			t.Fatalf("创建文件记录失败: %v", err)
		}
	}

	if err := fs.UpdateSegmentSize(ctx, "task-us-1", 1, 888888); err != nil {
		t.Fatalf("更新段大小失败: %v", err)
	}

	gotSeg, _ := fs.GetByID(ctx, seg1.ID)
	if gotSeg.SizeBytes != 888888 {
		t.Errorf("segment size_bytes = %d, want 888888", gotSeg.SizeBytes)
	}
	gotOut, _ := fs.GetByID(ctx, out.ID)
	if gotOut.SizeBytes != 4096 {
		t.Errorf("同 seq 的 output 记录不应被更新，size = %d, want 4096", gotOut.SizeBytes)
	}
}

// TestTaskFileMarkDeletedByType 验证按类型批量软删（合并后清理 TS 段），其他类型不受影响。
func TestTaskFileMarkDeletedByType(t *testing.T) {
	resetTables(t, "live_rec_task_files")
	ctx := context.Background()
	fs := NewTaskFileStore(testDB)

	created := time.Now().Truncate(time.Second)
	seg0 := newSegmentFile("task-dt-1", 0, created)
	seg1 := newSegmentFile("task-dt-1", 1, created)
	out := &TaskFile{
		TaskID: "task-dt-1", NodeID: "node-a", Filename: "out.mp4",
		Type: FileTypeOutput, Path: "/data/task-dt-1/out.mp4", Seq: 2,
		SizeBytes: 8192, Status: "ok", CreatedAt: created,
	}
	for _, f := range []*TaskFile{seg0, seg1, out} {
		if err := fs.Create(ctx, f); err != nil {
			t.Fatalf("创建文件记录失败: %v", err)
		}
	}

	if err := fs.MarkDeletedByType(ctx, "task-dt-1", FileTypeSegment); err != nil {
		t.Fatalf("按类型软删失败: %v", err)
	}

	active, err := fs.ListActiveByTask(ctx, "task-dt-1")
	if err != nil {
		t.Fatalf("ListActiveByTask 失败: %v", err)
	}
	if len(active) != 1 || active[0].Type != FileTypeOutput {
		t.Errorf("清理段后应只剩 output，实际 %+v", active)
	}

	// 再次按同类型软删不报错（幂等）
	if err := fs.MarkDeletedByType(ctx, "task-dt-1", FileTypeSegment); err != nil {
		t.Fatalf("重复按类型软删失败: %v", err)
	}
}

// TestTaskFileListExpiredSegments 验证过期段兜底清理查询：按节点、类型、创建时间过滤，且排除已软删记录。
func TestTaskFileListExpiredSegments(t *testing.T) {
	resetTables(t, "live_rec_task_files")
	ctx := context.Background()
	fs := NewTaskFileStore(testDB)

	base := time.Now().Truncate(time.Second)
	cutoff := base.Add(-time.Hour)

	// node-a 上的过期段（命中）
	oldSeg := newSegmentFile("task-ex-1", 0, base.Add(-2*time.Hour))
	// node-a 上的新段（创建时间晚于 cutoff，不命中）
	newSeg := newSegmentFile("task-ex-1", 1, base.Add(-30*time.Minute))
	// node-a 上的过期 output（类型不符，不命中）
	oldOut := &TaskFile{
		TaskID: "task-ex-1", NodeID: "node-a", Filename: "old.mp4",
		Type: FileTypeOutput, Path: "/data/task-ex-1/old.mp4", Seq: 2,
		SizeBytes: 4096, Status: "ok", CreatedAt: base.Add(-2 * time.Hour),
	}
	// node-b 上的过期段（节点不符，不命中）
	otherNode := newSegmentFile("task-ex-2", 0, base.Add(-2*time.Hour))
	otherNode.NodeID = "node-b"
	// node-a 上已软删的过期段（不命中）
	deletedSeg := newSegmentFile("task-ex-1", 3, base.Add(-2*time.Hour))

	for _, f := range []*TaskFile{oldSeg, newSeg, oldOut, otherNode, deletedSeg} {
		if err := fs.Create(ctx, f); err != nil {
			t.Fatalf("创建文件记录失败: %v", err)
		}
	}
	if err := fs.MarkDeletedByID(ctx, deletedSeg.ID); err != nil {
		t.Fatalf("软删失败: %v", err)
	}

	got, err := fs.ListExpiredSegments(ctx, "node-a", cutoff)
	if err != nil {
		t.Fatalf("ListExpiredSegments 失败: %v", err)
	}
	if len(got) != 1 || got[0].ID != oldSeg.ID {
		t.Errorf("过期段查询结果不匹配，应只命中 node-a 未删除的旧段: %+v", got)
	}
}

// TestTaskFileMaxSegment 验证续录起点查询：返回最大 seq 的段且只统计 segment 类型；无段时返回 (nil, nil)。
func TestTaskFileMaxSegment(t *testing.T) {
	resetTables(t, "live_rec_task_files")
	ctx := context.Background()
	fs := NewTaskFileStore(testDB)

	created := time.Now().Truncate(time.Second)
	for _, seq := range []int{0, 3, 7} {
		if err := fs.Create(ctx, newSegmentFile("task-mx-1", seq, created)); err != nil {
			t.Fatalf("创建段记录失败: %v", err)
		}
	}
	// output 记录 seq 更大，但不是段，不应成为最大段
	out := &TaskFile{
		TaskID: "task-mx-1", NodeID: "node-a", Filename: "out.mp4",
		Type: FileTypeOutput, Path: "/data/task-mx-1/out.mp4", Seq: 9,
		SizeBytes: 4096, Status: "ok", CreatedAt: created,
	}
	if err := fs.Create(ctx, out); err != nil {
		t.Fatalf("创建 output 记录失败: %v", err)
	}

	got, err := fs.MaxSegment(ctx, "task-mx-1")
	if err != nil {
		t.Fatalf("MaxSegment 失败: %v", err)
	}
	if got == nil {
		t.Fatal("应存在最大段，实际 nil")
	}
	if got.Seq != 7 || got.Type != FileTypeSegment {
		t.Errorf("最大段 = seq %d type %q, want seq 7 type segment", got.Seq, got.Type)
	}

	// 无段任务返回 (nil, nil)
	none, err := fs.MaxSegment(ctx, "task-mx-empty")
	if err != nil {
		t.Fatalf("MaxSegment 失败: %v", err)
	}
	if none != nil {
		t.Errorf("无段任务应返回 nil，实际 %+v", none)
	}
}
