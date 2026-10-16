// worker 包测试公共基础设施 + 可脱离调度链路的单元逻辑测试。
package worker

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"recmaestro/internal/config"
	"recmaestro/internal/migrate"
	"recmaestro/internal/queue"
	"recmaestro/internal/recorder"
	"recmaestro/internal/store"
)

// testTaskPrefix 测试数据统一前缀，结束时按前缀清理，避免污染测试库。
const testTaskPrefix = "wtest-"

// TestMain 进程级初始化：配置目录指向仓库 config/（t.Setenv 不能用于并行测试，
// 故在所有测试开始前统一设置）。
func TestMain(m *testing.M) {
	_ = os.Setenv("CONFIG_DIR", "../../config")
	os.Exit(m.Run())
}

// migrateOnce 保证并发测试只执行一次建库迁移（幂等）。
var migrateOnce sync.Once

// testEnv 汇聚一个测试所需的全部依赖：worker、MySQL 连接、miniredis 客户端。
// taskIDs 登记本测试创建的任务，结束时按 ID 精确清理（并行测试数据互相隔离）。
type testEnv struct {
	w       *Worker
	db      *gorm.DB
	rdb     *redis.Client
	taskIDs []string
}

// newTestWorker 构造测试用 Worker：
//   - 配置走 config.test.yaml（测试库 live_recorder_test），RecordsDir 指向临时目录；
//   - 自动执行数据库迁移建表（sync.Once 幂等，供并行测试共用）；
//   - Redis 用 miniredis 内存实例，严禁连真实 Redis；
//   - TaskLogger 不启动落库循环（日志只进缓冲 channel，不写库）。
//
// 测试数据以 taskID 前缀隔离，各用例可安全并行（t.Parallel）。
func newTestWorker(t *testing.T) *testEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	if os.Getenv("MYSQL_PASSWORD") == "" {
		t.Fatalf("需要设置环境变量 MYSQL_PASSWORD（本地 MySQL root 密码）")
	}
	cfg, err := config.Load("test")
	if err != nil {
		t.Fatalf("加载 test 配置失败: %v", err)
	}
	// 测试库隔离：go test ./... 跨包并行时若与 scheduler 包共用 live_recorder_test，
	// 本包造的 queued 任务会被 scheduler 的 RebuildQueues 全表查询捞走（其断言队列
	// 条数即失败），故改用本包独立库（migrate.Up 自动建库建表）。
	cfg.MySQL.Database = "live_recorder_worker_test"
	cfg.Recorder.RecordsDir = t.TempDir()
	// 内部接口鉴权密钥（生产从 INTERNAL_API_TOKEN 环境变量注入，测试用固定值）
	cfg.Server.InternalAPIToken = testInternalToken

	var migrateErr error
	migrateOnce.Do(func() {
		migrateErr = migrate.Up(cfg.MySQL)
	})
	if migrateErr != nil {
		t.Fatalf("迁移测试库失败: %v", migrateErr)
	}
	db, err := store.NewDB(cfg.MySQL)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	w := &Worker{
		nodeID: "node-test",
		nodeIP: "127.0.0.1",
		cfg:    cfg,
		rec:    recorder.New(cfg.Recorder.FFmpegPath, cfg.Recorder.FFmpegLogLevel, cfg.Recorder.Reconnect.DelayMax),
		tasks:  store.NewTaskStore(db),
		files:  store.NewTaskFileStore(db),
		nodes:  store.NewNodeStore(db),
		queue:  queue.NewQueue(rdb),
		logger: store.NewTaskLogger(db),
		logs:   store.NewTaskLogStore(db),
	}

	env := &testEnv{w: w, db: db, rdb: rdb}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range env.taskIDs {
			db.WithContext(ctx).Where("task_id = ?", id).Delete(&store.TaskFile{})
			db.WithContext(ctx).Where("task_id = ?", id).Delete(&store.TaskLog{})
			db.WithContext(ctx).Where("id = ?", id).Delete(&store.Task{})
		}
	})
	return env
}

// newTestTask 创建一条任务记录。默认截止时间已过（失败时直接终态，不进自动重试），
// mutate 可覆盖字段（如改 StreamURL / 状态专属字段）。
// 建前先清同 ID 残留（上次测试若中途断连，cleanup 可能没执行完），保证可重复运行。
func newTestTask(t *testing.T, env *testEnv, id string, status store.TaskStatus, mutate func(*store.Task)) *store.Task {
	t.Helper()
	ctx := context.Background()
	env.db.WithContext(ctx).Where("task_id = ?", id).Delete(&store.TaskFile{})
	env.db.WithContext(ctx).Where("task_id = ?", id).Delete(&store.TaskLog{})
	env.db.WithContext(ctx).Where("id = ?", id).Delete(&store.Task{})

	end := time.Now().Add(-time.Hour)
	task := &store.Task{
		ID:             id,
		Name:           "测试-" + id,
		StreamURL:      "http://127.0.0.1:1/live", // 端口 1 无监听，探测立即失败
		Status:         status,
		SegmentSeconds: 300,
		EndTime:        &end,
	}
	if mutate != nil {
		mutate(task)
	}
	if err := env.w.tasks.Create(ctx, task); err != nil {
		t.Fatalf("创建任务 %s 失败: %v", id, err)
	}
	env.taskIDs = append(env.taskIDs, id)
	return task
}

// TestFailSuffix 验证重试次数后缀文案。
func TestFailSuffix(t *testing.T) {
	cases := []struct {
		retryCount int
		want       string
	}{
		{0, ""},
		{1, "（第 1 次重试后失败）"},
		{3, "（第 3 次重试后失败）"},
	}
	for _, c := range cases {
		if got := failSuffix(c.retryCount); got != c.want {
			t.Errorf("failSuffix(%d) = %q, want %q", c.retryCount, got, c.want)
		}
	}
}

// TestWorkerAddr 验证内部接口地址：WORKER_ADDR 覆盖优先，默认 nodeIP:port。
func TestWorkerAddr(t *testing.T) {
	w := &Worker{nodeIP: "10.1.2.3", cfg: &config.Config{}}
	w.cfg.Server.Port = 8080

	// 未设置环境变量：默认用 nodeIP 与配置端口
	if got := w.workerAddr(); got != "10.1.2.3:8080" {
		t.Errorf("默认地址 = %q, want 10.1.2.3:8080", got)
	}

	// 设置 WORKER_ADDR 后覆盖
	t.Setenv("WORKER_ADDR", "172.20.0.5:9090")
	if got := w.workerAddr(); got != "172.20.0.5:9090" {
		t.Errorf("覆盖地址 = %q, want 172.20.0.5:9090", got)
	}
}

// TestDetectFFmpegVersion 验证版本探测：命令不存在返回空串；PATH 上的 ffmpeg 返回首行（截断内）。
func TestDetectFFmpegVersion(t *testing.T) {
	if got := detectFFmpegVersion("recmaestro-no-such-ffmpeg"); got != "" {
		t.Errorf("不存在的命令应返回空串，实际 %q", got)
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skipf("PATH 中无 ffmpeg，跳过真实探测: %v", err)
	}
	got := detectFFmpegVersion("ffmpeg")
	if got == "" {
		t.Fatalf("PATH 上有 ffmpeg，应探测到版本")
	}
	if !strings.HasPrefix(got, "ffmpeg version") {
		t.Errorf("版本串应以 \"ffmpeg version\" 开头，实际 %q", got)
	}
	if len(got) > 60 {
		t.Errorf("版本串应截断到 60 字符内，实际长度 %d", len(got))
	}
}

// TestLoadSegmentFiles 验证从 DB 构造合并输入：只取未软删的 segment、按 seq 升序、
// 排除 output 等其他类型。
func TestLoadSegmentFiles(t *testing.T) {
	t.Parallel()
	env := newTestWorker(t)
	ctx := context.Background()
	taskID := testTaskPrefix + "loadsegs"
	newTestTask(t, env, taskID, store.TaskRecordFailed, nil)

	dir := env.w.cfg.Recorder.RecordsDir
	// 乱序插入 seq=2、seq=1，另加一条 output 与一条已软删段
	mk := func(seq int, fileType string, path string) *store.TaskFile {
		f := &store.TaskFile{
			TaskID: taskID, NodeID: env.w.nodeID, Filename: path,
			Type: fileType, Path: path, Seq: seq, SizeBytes: 100, Status: "ok",
		}
		if err := env.w.files.Create(ctx, f); err != nil {
			t.Fatalf("创建文件记录失败: %v", err)
		}
		return f
	}
	mk(2, store.FileTypeSegment, dir+"/seg_00002.ts")
	deleted := mk(0, store.FileTypeSegment, dir+"/seg_00000.ts")
	mk(1, store.FileTypeSegment, dir+"/seg_00001.ts")
	mk(0, store.FileTypeOutput, dir+"/output.mp4")
	if err := env.w.files.MarkDeletedByID(ctx, deleted.ID); err != nil {
		t.Fatalf("软删段记录失败: %v", err)
	}

	segs := env.w.loadSegmentFiles(ctx, taskID)
	if len(segs) != 2 {
		t.Fatalf("期望 2 个有效段（排除 output 与已软删），实际 %d: %+v", len(segs), segs)
	}
	if segs[0].Seq != 1 || segs[1].Seq != 2 {
		t.Errorf("段应按 seq 升序，实际 seq=%d,%d", segs[0].Seq, segs[1].Seq)
	}
	if segs[0].Size != 100 {
		t.Errorf("段 size 应取记录的 size_bytes，实际 %d", segs[0].Size)
	}
}

// TestCleanupSegments 验证合并后清理：只删 seg_*.ts 文件与段记录，产物与 list.txt 保留。
func TestCleanupSegments(t *testing.T) {
	t.Parallel()
	env := newTestWorker(t)
	ctx := context.Background()
	taskID := testTaskPrefix + "cleansegs"
	newTestTask(t, env, taskID, store.TaskCompleted, nil)

	dir := t.TempDir()
	for _, name := range []string{"seg_00001.ts", "seg_00002.ts", "list.txt", "output.mp4"} {
		if err := os.WriteFile(dir+"/"+name, []byte("x"), 0o644); err != nil {
			t.Fatalf("造文件 %s 失败: %v", name, err)
		}
	}
	for seq := 1; seq <= 2; seq++ {
		if err := env.w.files.Create(ctx, &store.TaskFile{
			TaskID: taskID, NodeID: env.w.nodeID,
			Filename: "seg.ts", Type: store.FileTypeSegment,
			Path: dir, Seq: seq, Status: "ok",
		}); err != nil {
			t.Fatalf("造段记录失败: %v", err)
		}
	}

	env.w.cleanupSegments(ctx, taskID, dir)

	for _, name := range []string{"seg_00001.ts", "seg_00002.ts"} {
		if _, err := os.Stat(dir + "/" + name); !os.IsNotExist(err) {
			t.Errorf("段文件 %s 应被删除", name)
		}
	}
	for _, name := range []string{"list.txt", "output.mp4"} {
		if _, err := os.Stat(dir + "/" + name); err != nil {
			t.Errorf("非段文件 %s 不应被删除: %v", name, err)
		}
	}
	files, err := env.w.files.ListActiveByTask(ctx, taskID)
	if err != nil {
		t.Fatalf("查询文件记录失败: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("段记录应全部软删，仍剩 %d 条", len(files))
	}
}

// TestCleanupExpiredSegments 验证兜底清理只按文件粒度删过期段：
// 过期段（created_at 早于 cutoff）删文件并软删记录，同任务的新段必须保留。
func TestCleanupExpiredSegments(t *testing.T) {
	t.Parallel()
	env := newTestWorker(t)
	ctx := context.Background()
	taskID := testTaskPrefix + "expired"
	newTestTask(t, env, taskID, store.TaskMergeFailed, nil)

	dir := t.TempDir()
	mkSeg := func(name string) string {
		path := dir + "/" + name
		if err := os.WriteFile(path, []byte("ts"), 0o644); err != nil {
			t.Fatalf("造段文件 %s 失败: %v", name, err)
		}
		return path
	}
	oldSeg := mkSeg("seg_00001.ts")
	freshSeg := mkSeg("seg_00002.ts")

	oldRec := &store.TaskFile{TaskID: taskID, NodeID: env.w.nodeID, Filename: "seg_00001.ts",
		Type: store.FileTypeSegment, Path: oldSeg, Seq: 1, Status: "ok"}
	freshRec := &store.TaskFile{TaskID: taskID, NodeID: env.w.nodeID, Filename: "seg_00002.ts",
		Type: store.FileTypeSegment, Path: freshSeg, Seq: 2, Status: "ok"}
	if err := env.w.files.Create(ctx, oldRec); err != nil {
		t.Fatalf("造旧段记录失败: %v", err)
	}
	if err := env.w.files.Create(ctx, freshRec); err != nil {
		t.Fatalf("造新段记录失败: %v", err)
	}
	// 将旧段 created_at 改到保留期（默认 3 天）之外
	fiveDaysAgo := time.Now().Add(-5 * 24 * time.Hour)
	if err := env.db.WithContext(ctx).Model(&store.TaskFile{}).
		Where("id = ?", oldRec.ID).Update("created_at", fiveDaysAgo).Error; err != nil {
		t.Fatalf("回溯旧段 created_at 失败: %v", err)
	}

	env.w.cleanupExpiredSegments(ctx)

	if _, err := os.Stat(oldSeg); !os.IsNotExist(err) {
		t.Errorf("过期段文件应被删除")
	}
	if _, err := os.Stat(freshSeg); err != nil {
		t.Errorf("未过期的段文件不应被删除: %v", err)
	}
	oldAfter, err := env.w.files.GetByID(ctx, oldRec.ID)
	if err != nil || oldAfter == nil {
		t.Fatalf("查询旧段记录失败: %v", err)
	}
	if oldAfter.DeletedAt == nil {
		t.Errorf("过期段记录应被软删")
	}
	freshAfter, err := env.w.files.GetByID(ctx, freshRec.ID)
	if err != nil || freshAfter == nil {
		t.Fatalf("查询新段记录失败: %v", err)
	}
	if freshAfter.DeletedAt != nil {
		t.Errorf("未过期的段记录不应被软删（按文件粒度清理）")
	}
}

// TestReportNodeStatus 验证节点状态上报：upsert 落库、重复上报不产生重复行。
func TestReportNodeStatus(t *testing.T) {
	t.Parallel()
	env := newTestWorker(t)
	ctx := context.Background()
	env.w.cfg.Recorder.WorkerCapacity = 7

	env.w.reportNodeStatus(ctx)
	env.w.currentTasks.Store(3)
	env.w.reportNodeStatus(ctx)

	nodes, err := env.w.nodes.List(ctx)
	if err != nil {
		t.Fatalf("查询节点表失败: %v", err)
	}
	var mine []*store.Node
	for i := range nodes {
		if nodes[i].NodeID == env.w.nodeID {
			mine = append(mine, &nodes[i])
		}
	}
	if len(mine) != 1 {
		t.Fatalf("重复上报后应只有 1 条 node_id=%s 记录，实际 %d 条", env.w.nodeID, len(mine))
	}
	if mine[0].Status != "online" {
		t.Errorf("节点状态 = %q, want online", mine[0].Status)
	}
	if mine[0].Capacity != 7 {
		t.Errorf("节点容量 = %d, want 7", mine[0].Capacity)
	}
	if mine[0].CurrentTasks != 3 {
		t.Errorf("当前任务数 = %d, want 3", mine[0].CurrentTasks)
	}
}
