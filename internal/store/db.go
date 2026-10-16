package store

import (
	"fmt"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"recmaestro/internal/config"
)

// NewDB 初始化 GORM 连接并设置连接池参数。
func NewDB(cfg config.MySQLConfig) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(cfg.DSN()), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("打开数据库连接失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层连接失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	// 连接复用治理（0 值 = 不限制，database/sql 语义）：
	// 高并发波动下若 idle 池小于峰值打开数，连接每次回落即被关闭、再高峰重建，
	// 客户端 TIME_WAIT 堆积会耗尽容器 ephemeral port（压测 100 并发档间歇 500 根因）。
	// 部署侧应使 max_idle_conns = max_open_conns 让峰值连接全量复用；
	// conn_max_lifetime 定期轮换长寿命连接（规避服务端 wait_timeout 断连产生的陈旧连接），
	// conn_max_idle_time 在低负载时释放空闲连接（MySQL max_connections 为 scheduler 与 worker 共用总账）。
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	return db, nil
}
