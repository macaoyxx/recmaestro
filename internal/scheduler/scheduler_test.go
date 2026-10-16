package scheduler

// scheduler_test.go — 本包测试共享基建：
// MySQL 测试库（127.0.0.1:3306/live_recorder_test，经 internal/migrate 建表，用例自清理）
// 与 miniredis 内存 Redis（严禁连真实 6379）。

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"recmaestro/internal/auth"
	"recmaestro/internal/config"
	"recmaestro/internal/migrate"
	"recmaestro/internal/queue"
	"recmaestro/internal/store"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// testDBState 缓存整个测试进程共享的 MySQL 测试库连接（迁移 + 建库只做一次）。
var testDBState struct {
	once sync.Once
	db   *gorm.DB
	err  error
}

// testMySQLPwd 返回测试库密码：优先环境变量 MYSQL_PASSWORD，缺省 root123456。
func testMySQLPwd() string {
	if pwd := os.Getenv("MYSQL_PASSWORD"); pwd != "" {
		return pwd
	}
	return "root123456"
}

// testDB 返回 MySQL 测试库连接（进程内共享），首次调用时建库并迁移。
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	testDBState.once.Do(func() {
		// 主机默认 127.0.0.1，可经 MYSQL_HOST 覆盖（Windows 侧跑测试时指向 WSL 内部 IP）
		host := os.Getenv("MYSQL_HOST")
		if host == "" {
			host = "127.0.0.1"
		}
		cfg := config.MySQLConfig{
			Host:     host,
			Port:     3306,
			Database: "live_recorder_test",
			Username: "root",
			Password: testMySQLPwd(),
			Charset:  "utf8mb4",
		}
		// 建库 + 全量迁移（幂等，已有 schema_migrations 版本则跳过）
		if err := migrate.Up(cfg); err != nil {
			testDBState.err = fmt.Errorf("测试库迁移失败: %w", err)
			return
		}
		db, err := store.NewDB(cfg)
		if err != nil {
			testDBState.err = fmt.Errorf("连接测试库失败: %w", err)
			return
		}
		testDBState.db = db
	})
	if testDBState.err != nil {
		// 环境守卫：MySQL 测试库不可达时跳过依赖 DB 的用例（报告为 SKIP，不冒充通过），
		// MySQL 可用时全部用例照常真跑真断言
		t.Skipf("MySQL 测试库不可用（127.0.0.1:3306），跳过依赖 DB 的用例: %v", testDBState.err)
	}
	return testDBState.db
}

// newTestScheduler 构造用于测试的 Server：MySQL 测试库 + miniredis，
// 不经 NewServer 以避开服务注册/ Consul 等外部依赖；日志写入器只进内存缓冲不落库。
func newTestScheduler(t *testing.T) (*Server, *miniredis.Miniredis) {
	t.Helper()
	db := testDB(t)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	cfg := &config.Config{}
	cfg.Scheduler.ScanInterval = 100 * time.Millisecond // 扫描间隔（scanAndDispatch 直接调用，仅影响锁 TTL 下限）
	cfg.Scheduler.RecoverWindow = time.Minute           // 心跳超过 1 分钟判超时
	cfg.Scheduler.InterruptAfter = 5 * time.Minute      // 超时 5 分钟未恢复判中断
	cfg.Recorder.RetryBackoff = 30 * time.Second        // 自动重试退避封顶
	cfg.Recorder.SegmentSeconds = 300
	cfg.JWT.Secret = "unit-test-secret"
	cfg.JWT.Expire = time.Hour

	s := &Server{
		cfg:     cfg,
		users:   store.NewUserStore(db),
		tasks:   store.NewTaskStore(db),
		files:   store.NewTaskFileStore(db),
		logs:    store.NewTaskLogStore(db),
		nodes:   store.NewNodeStore(db),
		audits:  store.NewAuditStore(db),
		queue:   queue.NewQueue(rdb),
		authMgr: auth.NewManager(cfg.JWT.Secret, cfg.JWT.Expire, refreshExpire),
		logger:  store.NewTaskLogger(db), // 不启动落库循环，Log 只写内存缓冲
	}
	return s, mr
}

// mustCreateTask 在测试库造一条任务（初始 pending，再按需改状态），测试结束自动清理。
func mustCreateTask(t *testing.T, s *Server, id string, status store.TaskStatus, start, end *time.Time) {
	t.Helper()
	ctx := context.Background()
	task := &store.Task{
		ID:             id,
		Name:           "调度测试-" + id,
		StreamURL:      "rtmp://example.local/live/unit",
		StartTime:      start,
		EndTime:        end,
		Status:         store.TaskPending,
		SegmentSeconds: 300,
		MaxRetry:       3,
	}
	if err := s.tasks.Create(ctx, task); err != nil {
		t.Fatalf("造数失败（创建任务 %s）: %v", id, err)
	}
	if status != store.TaskPending {
		if err := s.tasks.UpdateStatus(ctx, id, status); err != nil {
			t.Fatalf("造数失败（任务 %s 置 %s）: %v", id, status, err)
		}
	}
	// 用例自清理：无条件物理删除本用例造的任务行（运行态任务 Delete 会被条件拦截）
	t.Cleanup(func() {
		if err := testDB(t).Where("id = ?", id).Delete(&store.Task{}).Error; err != nil {
			t.Errorf("清理测试任务 %s 失败: %v", id, err)
		}
	})
}

// mustGetTask 查询任务当前状态，任务不存在时 Fatal。
func mustGetTask(t *testing.T, s *Server, id string) *store.Task {
	t.Helper()
	task, err := s.tasks.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("查询任务 %s 失败: %v", id, err)
	}
	if task == nil {
		t.Fatalf("任务 %s 应存在于测试库", id)
	}
	return task
}
