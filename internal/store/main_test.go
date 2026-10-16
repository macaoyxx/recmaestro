package store

import (
	"os"
	"testing"
	"time"

	"gorm.io/gorm"

	"recmaestro/internal/config"
	"recmaestro/internal/migrate"
)

// testDB 是全部用例共享的 GORM 连接，指向本包专用测试库 live_recorder_store_test。
var testDB *gorm.DB

// testMySQLConfig 返回测试库连接配置：默认 127.0.0.1:3306 / root / live_recorder_store_test。
// 密码从环境变量 MYSQL_PASSWORD 读取（约定默认 root123456），主机可经 MYSQL_HOST 覆盖
// （Windows 侧跑测试时指向 WSL 内部 IP，本机 localhost 转发不可用），不读取仓库内任何 yml。
// 注意：本包用例经 resetTables 整表清空数据，go test ./... 跨包并行时会殃及
// worker/scheduler 包在同一库里的并行测试数据（任务行被物理删除导致其轮询超时），
// 故必须用独立库隔离，不能与 worker/scheduler 共用 live_recorder_test。
func testMySQLConfig() config.MySQLConfig {
	pwd := os.Getenv("MYSQL_PASSWORD")
	if pwd == "" {
		pwd = "root123456"
	}
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	return config.MySQLConfig{
		Host:     host,
		Port:     3306,
		Database: "live_recorder_store_test",
		Username: "root",
		Password: pwd,
		Charset:  "utf8mb4",
		// 连接池须覆盖并发用例规模（20 个 goroutine 同时 Claim）
		MaxOpenConns: 30,
		MaxIdleConns: 30,
	}
}

// TestMain 在全部用例前确保测试库存在并经 internal/migrate 建表（幂等），结束后关闭连接。
func TestMain(m *testing.M) {
	cfg := testMySQLConfig()
	if err := migrate.Up(cfg); err != nil {
		os.Stderr.WriteString("测试库迁移失败（请确认 127.0.0.1:3306 可达、MYSQL_PASSWORD 正确）: " + err.Error() + "\n")
		os.Exit(1)
	}
	db, err := NewDB(cfg)
	if err != nil {
		os.Stderr.WriteString("测试库连接失败: " + err.Error() + "\n")
		os.Exit(1)
	}
	testDB = db

	code := m.Run()

	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	os.Exit(code)
}

// resetTables 在用例开始前清空指定表，并注册 t.Cleanup 在用例结束后再次清空，保证用例自清理、互不污染。
func resetTables(t *testing.T, tables ...string) {
	t.Helper()
	clean := func() {
		for _, tb := range tables {
			if err := testDB.Exec("DELETE FROM " + tb).Error; err != nil {
				t.Errorf("清理表 %s 失败: %v", tb, err)
			}
		}
	}
	clean()
	t.Cleanup(clean)
}

// sameSecond 判断两个时间在秒精度上相等：MySQL DATETIME 不存毫秒，时间断言统一降为秒级比较。
func sameSecond(a, b time.Time) bool { return a.Unix() == b.Unix() }
