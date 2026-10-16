package config

import (
	"os"
	"testing"
	"time"
)

// setConfigDir 将配置目录指向项目根下的 config/，测试结束后恢复。
func setConfigDir(t *testing.T) {
	t.Helper()
	if err := os.Setenv("CONFIG_DIR", "../../config"); err != nil {
		t.Fatalf("设置 CONFIG_DIR 失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("CONFIG_DIR") })
}

// TestRetryBackoffFor 验证分层退避：前 3 次 10s，之后每次 +10s，封顶配置值。
func TestRetryBackoffFor(t *testing.T) {
	c := &RecorderConfig{RetryBackoff: 30 * time.Second}
	cases := []struct {
		retryCount int
		want       time.Duration
	}{
		{1, 10 * time.Second},
		{2, 10 * time.Second},
		{3, 10 * time.Second},
		{4, 20 * time.Second},
		{5, 30 * time.Second},
		{8, 30 * time.Second}, // 封顶后不再增长
	}
	for _, tc := range cases {
		if got := c.RetryBackoffFor(tc.retryCount); got != tc.want {
			t.Errorf("RetryBackoffFor(%d) = %v, want %v", tc.retryCount, got, tc.want)
		}
	}

	// 封顶调高后退避继续递增到新封顶
	c.RetryBackoff = 60 * time.Second
	if got := c.RetryBackoffFor(4); got != 20*time.Second {
		t.Errorf("cap=60s RetryBackoffFor(4) = %v, want 20s", got)
	}
	if got := c.RetryBackoffFor(6); got != 40*time.Second {
		t.Errorf("cap=60s RetryBackoffFor(6) = %v, want 40s", got)
	}
	if got := c.RetryBackoffFor(9); got != 60*time.Second {
		t.Errorf("cap=60s RetryBackoffFor(9) = %v, want 60s", got)
	}

	// 配置异常（0/负值）时兜底为最小步长
	c.RetryBackoff = 0
	if got := c.RetryBackoffFor(1); got != 10*time.Second {
		t.Errorf("cap=0 RetryBackoffFor(1) = %v, want 10s", got)
	}
}

func TestLoadBaseDefaults(t *testing.T) {
	setConfigDir(t)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("加载基础配置失败: %v", err)
	}
	if cfg.MySQL.TablePrefix != "live_rec_" {
		t.Errorf("期望表前缀 live_rec_，实际 %q", cfg.MySQL.TablePrefix)
	}
	if cfg.Recorder.SegmentSeconds != 300 {
		t.Errorf("期望 TS 分段 300s，实际 %d", cfg.Recorder.SegmentSeconds)
	}
	if !cfg.Recorder.SegmentCleanup.CleanupEnabled {
		t.Errorf("期望默认清理 TS 段")
	}
}

func TestLoadEnvOverride(t *testing.T) {
	setConfigDir(t)

	cfg, err := Load("dev")
	if err != nil {
		t.Fatalf("加载 dev 配置失败: %v", err)
	}
	if cfg.Server.Env != "dev" {
		t.Errorf("期望 env=dev，实际 %q", cfg.Server.Env)
	}
	if cfg.MySQL.Database != "live_recorder_dev" {
		t.Errorf("期望 dev 库名 live_recorder_dev，实际 %q", cfg.MySQL.Database)
	}
}

func TestLoadTestEnv(t *testing.T) {
	setConfigDir(t)

	cfg, err := Load("test")
	if err != nil {
		t.Fatalf("加载 test 配置失败: %v", err)
	}
	if cfg.MySQL.Database != "live_recorder_test" {
		t.Errorf("期望 test 库名 live_recorder_test，实际 %q", cfg.MySQL.Database)
	}
}

func TestEnvVarOverridesSensitive(t *testing.T) {
	setConfigDir(t)
	t.Setenv("MYSQL_PASSWORD", "secret123")
	t.Setenv("JWT_SECRET", "jwt-secret")

	cfg, err := Load("local")
	if err != nil {
		t.Fatalf("加载 local 配置失败: %v", err)
	}
	if cfg.MySQL.Password != "secret123" {
		t.Errorf("期望环境变量覆盖 MySQL 密码，实际 %q", cfg.MySQL.Password)
	}
	if cfg.JWT.Secret != "jwt-secret" {
		t.Errorf("期望环境变量覆盖 JWT secret，实际 %q", cfg.JWT.Secret)
	}
}
