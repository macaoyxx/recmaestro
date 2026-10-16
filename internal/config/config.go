// Package config 负责加载并解析 RecMaestro 的多环境配置。
//
// 配置文件位于 config/ 目录（可用环境变量 CONFIG_DIR 覆盖），
// 先加载 config.yaml（公共默认），再合并 config.<env>.yaml（环境覆盖）。
// 密码、secret、token 等敏感信息一律从环境变量读取，避免写入仓库。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/viper"
)

// Config 是 RecMaestro 的全局配置。
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	MySQL     MySQLConfig     `mapstructure:"mysql"`
	Redis     RedisConfig     `mapstructure:"redis"`
	Consul    ConsulConfig    `mapstructure:"consul"`
	Registry  RegistryConfig  `mapstructure:"registry"`
	Recorder  RecorderConfig  `mapstructure:"recorder"`
	JWT       JWTConfig       `mapstructure:"jwt"`
	Log       LogConfig       `mapstructure:"log"`
	Scheduler SchedulerConfig `mapstructure:"scheduler"`
}

// InternalTokenHeader 是 scheduler 调用 worker 内部接口时携带共享密钥的 HTTP 头，
// 双方共用此常量（scheduler 下发/反代时设置，worker 中间件校验）。
const InternalTokenHeader = "X-Internal-Token"

// ServerConfig 服务基础配置。
type ServerConfig struct {
	Role             string `mapstructure:"role"`               // 运行角色：scheduler | worker
	Host             string `mapstructure:"host"`               // 监听地址
	Port             int    `mapstructure:"port"`               // 监听端口
	Env              string `mapstructure:"env"`                // 运行环境：dev | test | local | prod
	InternalAPIToken string `mapstructure:"internal_api_token"` // worker 内部接口共享密钥（从环境变量 INTERNAL_API_TOKEN 读取）
}

// MySQLConfig MySQL 连接配置。密码从环境变量 MYSQL_PASSWORD 读取。
type MySQLConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	Database     string `mapstructure:"database"`
	Username     string `mapstructure:"username"`
	Password     string `mapstructure:"password"`
	Charset      string `mapstructure:"charset"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
	// 连接池生命周期治理，0 = 不限制（database/sql 语义）。部署应使 max_idle_conns
	// 与 max_open_conns 等大以全量复用峰值连接，详见 store.NewDB 的复用说明。
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `mapstructure:"conn_max_idle_time"`
	TablePrefix     string        `mapstructure:"table_prefix"`
}

// DSN 返回 go-sql-driver/mysql 的连接串（含目标库）。
// 供 GORM 等常规连接使用，不含 multiStatements（避免多语句执行风险）。
func (m MySQLConfig) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=true&loc=Local",
		m.Username, m.Password, m.Host, m.Port, m.Database, m.Charset)
}

// DSNForMigration 返回带 multiStatements=true 的连接串，
// 供 golang-migrate 执行含多条语句的迁移文件使用。
func (m MySQLConfig) DSNForMigration() string {
	return m.DSN() + "&multiStatements=true"
}

// RedisConfig Redis 连接配置。密码从环境变量 REDIS_PASSWORD 读取。
type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

// ConsulConfig Consul 连接配置。ACL token 从环境变量 CONSUL_TOKEN 读取。
type ConsulConfig struct {
	Addr   string `mapstructure:"addr"`
	Token  string `mapstructure:"token"`
	Scheme string `mapstructure:"scheme"`
}

// RegistryConfig 服务注册引擎配置。engine 默认 redis（零额外依赖），可选 consul。
type RegistryConfig struct {
	Engine string `mapstructure:"engine"`
}

// RecorderConfig 录制相关配置。
type RecorderConfig struct {
	RecordsDir     string               `mapstructure:"records_dir"`
	FFmpegPath     string               `mapstructure:"ffmpeg_path"`
	FFmpegLogLevel string               `mapstructure:"ffmpeg_loglevel"`
	SegmentSeconds int                  `mapstructure:"segment_seconds"`
	WorkerCapacity int                  `mapstructure:"worker_capacity"`
	RetryBackoff   time.Duration        `mapstructure:"retry_backoff"`
	Reconnect      ReconnectConfig      `mapstructure:"reconnect"`
	SegmentCleanup SegmentCleanupConfig `mapstructure:"segment_cleanup"`
}

// ReconnectConfig 断流重连参数。
type ReconnectConfig struct {
	DelayMax int `mapstructure:"delay_max"`
}

// RetryBackoffFor 按重试次数计算自动重试退避间隔：前 3 次 10s，之后每次递增 10s，
// 封顶 retry_backoff（即配置值为封顶而非起步）。
func (c *RecorderConfig) RetryBackoffFor(retryCount int) time.Duration {
	const step = 10 * time.Second
	capInterval := c.RetryBackoff
	if capInterval < step {
		capInterval = step
	}
	backoff := step
	if n := retryCount - 3; n > 0 {
		backoff += time.Duration(n) * step
	}
	if backoff > capInterval {
		backoff = capInterval
	}
	return backoff
}

// SegmentCleanupConfig TS 段清理策略。
type SegmentCleanupConfig struct {
	CleanupEnabled bool `mapstructure:"cleanup_enabled"`
	RetentionDays  int  `mapstructure:"retention_days"`
}

// JWTConfig 认证配置。secret 从环境变量 JWT_SECRET 读取。
type JWTConfig struct {
	Secret string        `mapstructure:"secret"`
	Expire time.Duration `mapstructure:"expire"`
}

// LogConfig 日志配置。RetentionDays 是任务日志保留天数（≤0 视为关闭定期清理）。
type LogConfig struct {
	Level         string `mapstructure:"level"`
	RetentionDays int    `mapstructure:"retention_days"`
}

// SchedulerConfig 调度器配置。
type SchedulerConfig struct {
	ScanInterval   time.Duration `mapstructure:"scan_interval"`
	RecoverWindow  time.Duration `mapstructure:"recover_window"`
	InterruptAfter time.Duration `mapstructure:"interrupt_after"`
}

// Load 加载配置。env 为空时只读 config.yaml，否则合并 config.<env>.yaml。
func Load(env string) (*Config, error) {
	dir := os.Getenv("CONFIG_DIR")
	if dir == "" {
		dir = "config"
	}

	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(dir)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("读取基础配置失败: %w", err)
	}

	// 合并环境覆盖文件；环境文件不存在时忽略，仅依赖基础配置。
	if env != "" {
		v.SetConfigName("config." + env)
		if err := v.MergeInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				return nil, fmt.Errorf("读取环境配置 config.%s.yaml 失败: %w", env, err)
			}
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}

	applyEnvOverrides(&cfg)
	return &cfg, nil
}

// applyEnvOverrides 用环境变量覆盖敏感配置项。环境变量为空则保留配置文件值。
func applyEnvOverrides(c *Config) {
	if v := os.Getenv("SERVER_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.Server.Port = p
		}
	}
	if v := os.Getenv("MYSQL_HOST"); v != "" {
		c.MySQL.Host = v
	}
	if v := os.Getenv("MYSQL_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.MySQL.Port = p
		}
	}
	// 连接池上限按角色注入（高并发-3）：scheduler 是高并发 API 面需要大池，
	// worker 是 worker_capacity 级执行器用小池，避免 N 副本 worker 各持大池
	// 把 MySQL max_connections 总账打满（容量算式见 deploy/docker-compose.yml）
	if v := os.Getenv("MYSQL_MAX_OPEN_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MySQL.MaxOpenConns = n
		}
	}
	if v := os.Getenv("MYSQL_MAX_IDLE_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MySQL.MaxIdleConns = n
		}
	}
	if v := os.Getenv("MYSQL_PASSWORD"); v != "" {
		c.MySQL.Password = v
	}
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		c.Redis.Addr = v
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		c.Redis.Password = v
	}
	if v := os.Getenv("CONSUL_ADDR"); v != "" {
		c.Consul.Addr = v
	}
	if v := os.Getenv("RECORDS_DIR"); v != "" {
		c.Recorder.RecordsDir = v
	}
	if v := os.Getenv("JWT_SECRET"); v != "" {
		c.JWT.Secret = v
	}
	if v := os.Getenv("INTERNAL_API_TOKEN"); v != "" {
		c.Server.InternalAPIToken = v
	}
	if v := os.Getenv("CONSUL_TOKEN"); v != "" {
		c.Consul.Token = v
	}
	if v := os.Getenv("SCHEDULER_SCAN_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Scheduler.ScanInterval = d
		}
	}
	if v := os.Getenv("SCHEDULER_RECOVER_WINDOW"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Scheduler.RecoverWindow = d
		}
	}
	if v := os.Getenv("SCHEDULER_INTERRUPT_AFTER"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Scheduler.InterruptAfter = d
		}
	}
	if v := os.Getenv("TASK_LOG_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Log.RetentionDays = n
		}
	}
}
