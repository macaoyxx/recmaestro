// Package migrate 负责数据库版本迁移（flyway 式）。
//
// SQL 迁移文件经 go:embed 打包进二进制，服务启动时自动执行 Up，
// 版本记录在 golang-migrate 的 schema_migrations 表中。
// 迁移 SQL 的表名固定使用默认前缀 live_rec_；如需自定义前缀，
// 需同步修改迁移 SQL 与 table_prefix 配置。
package migrate

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"regexp"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"recmaestro/internal/config"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Up 确保目标数据库存在，并应用所有未执行的迁移。
func Up(cfg config.MySQLConfig) error {
	if err := ensureDatabase(cfg); err != nil {
		return err
	}

	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("加载迁移源失败: %w", err)
	}

	db, err := sql.Open("mysql", cfg.DSNForMigration())
	if err != nil {
		return fmt.Errorf("打开数据库连接失败: %w", err)
	}
	defer db.Close()

	driver, err := migratemysql.WithInstance(db, &migratemysql.Config{})
	if err != nil {
		return fmt.Errorf("初始化迁移 driver 失败: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, cfg.Database, driver)
	if err != nil {
		return fmt.Errorf("初始化迁移器失败: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("执行迁移失败: %w", err)
	}
	return nil
}

// dbNameRe 校验数据库名，防止通过配置注入反引号等非法字符。
var dbNameRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// ensureDatabase 在目标库不存在时创建它。先以不带库名的连接建库。
func ensureDatabase(cfg config.MySQLConfig) error {
	if !dbNameRe.MatchString(cfg.Database) {
		return fmt.Errorf("非法数据库名 %q（仅允许字母/数字/下划线）", cfg.Database)
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=%s&parseTime=true",
		cfg.Username, cfg.Password, cfg.Host, cfg.Port, cfg.Charset)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("打开 MySQL 连接失败: %w", err)
	}
	defer db.Close()

	if _, err := db.Exec(fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		cfg.Database)); err != nil {
		return fmt.Errorf("创建数据库 %s 失败: %w", cfg.Database, err)
	}
	return nil
}
