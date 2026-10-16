package scheduler

// login_ratelimit_test.go — 登录 IP 限流测试：窗口内超阈值 429（带 Retry-After），
// 窗口滚动后恢复放行；不同 IP 互不影响。

import (
	"net/http"
	"testing"
	"time"
)

// TestLoginRateLimited 同一 IP 连续登录：前 10 次放行（含 401/403 等业务响应），
// 第 11 次 429；窗口滚动后重新放行。
func TestLoginRateLimited(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()

	code := 0
	for i := 1; i <= loginRateLimit; i++ {
		code = loginAs(t, r, "nobody", "whatever") // 用户不存在 → 401，但计入限流
		if code == http.StatusTooManyRequests {
			t.Fatalf("第 %d 次不应触发限流", i)
		}
	}
	w11 := doRequest(t, r, http.MethodPost, "/api/login", "", `{"username":"nobody","password":"whatever"}`)
	if w11.Code != http.StatusTooManyRequests {
		t.Fatalf("第 %d 次登录期望 429, 实际 %d（body %s）", loginRateLimit+1, w11.Code, w11.Body.String())
	}
	if ra := w11.Header().Get("Retry-After"); ra == "" {
		t.Error("429 响应应携带 Retry-After 头")
	}
}

// TestLoginRateLimitWindowReset 窗口滚动后计数重置（直接驱动 limiter 时间参数验证）。
func TestLoginRateLimitWindowReset(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()
	for i := 0; i < loginRateLimit; i++ {
		if !l.allow("10.0.0.9", now) {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	if l.allow("10.0.0.9", now) {
		t.Fatal("窗口内超阈值应拒绝")
	}
	// 时间推进过窗口：重新放行
	if !l.allow("10.0.0.9", now.Add(loginRateWindow+time.Second)) {
		t.Fatal("窗口滚动后应重新放行")
	}
	// 不同 IP 独立计数
	if !l.allow("10.0.0.8", now) {
		t.Fatal("不同 IP 不应受 10.0.0.9 计数影响")
	}
}
