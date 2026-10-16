package scheduler

// handler_task_test.go — handler 层 httptest 测试：
// 创建任务参数校验（end_time 必填且晚于 now 等）、立即/定时两条入队路径、
// 重复 ID 409，以及认证中间件拒绝无 token / 坏 token / refresh token。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"recmaestro/internal/store"
)

// doRequest 发起 JSON 请求并返回响应记录器；authHeader 为完整 Authorization 头值
// （空串表示不带认证头），便于覆盖无 Bearer 前缀等异常形态。
func doRequest(t *testing.T, r http.Handler, method, path, authHeader, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// mustToken 签发指定角色的 access token。
func mustToken(t *testing.T, s *Server, role string) string {
	t.Helper()
	token, err := s.authMgr.SignAccess(1, "tester", role)
	if err != nil {
		t.Fatalf("签发 %s token 失败: %v", role, err)
	}
	return token
}

// decodeErr 解析响应体中的 error 字段。
func decodeErr(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应体失败: %v（原始 %s）", err, w.Body.String())
	}
	return body.Msg
}

// cleanupTask 注册测试结束删除指定任务行的清理函数。
func cleanupTask(t *testing.T, id string) {
	t.Helper()
	t.Cleanup(func() {
		if err := testDB(t).Where("id = ?", id).Delete(&store.Task{}).Error; err != nil {
			t.Errorf("清理测试任务 %s 失败: %v", id, err)
		}
	})
}

// TestHandleCreateTaskValidation 创建任务参数校验（表驱动）：
// stream_url 必填、end_time 必填且须晚于当前时间与 start_time、segment_seconds 范围。
// validate_test.go 已覆盖纯函数，此处验证 handler 到校验函数的接线与 HTTP 语义。
func TestHandleCreateTaskValidation(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()
	token := mustToken(t, s, "admin")

	now := time.Now()
	past := now.Add(-time.Hour)
	in1h := now.Add(time.Hour)
	rfc := func(tm time.Time) string { return tm.Format(time.RFC3339) }
	const url = `{"stream_url":"rtmp://example.local/live/a"`

	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"缺少 stream_url", `{"end_time":"` + rfc(in1h) + `"}`, "请求参数错误"},
		{"缺少 end_time", url + `}`, "截止时间（end_time）必填"},
		{"end_time 早于当前时间", url + `,"end_time":"` + rfc(past) + `"}`, "截止时间不能早于当前时间"},
		{"end_time 等于当前时间", url + `,"end_time":"` + rfc(now) + `"}`, "截止时间不能早于当前时间"},
		{"end_time 不晚于 start_time", url + `,"start_time":"` + rfc(in1h) + `","end_time":"` + rfc(in1h) + `"}`, "截止时间必须晚于开始时间"},
		{"segment_seconds 过小", url + `,"end_time":"` + rfc(in1h) + `","segment_seconds":4}`, "分段时长"},
		{"segment_seconds 过大", url + `,"end_time":"` + rfc(in1h) + `","segment_seconds":3601}`, "分段时长"},
		{"JSON 格式非法", `{not-a-json`, "请求参数错误"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := doRequest(t, r, http.MethodPost, "/api/tasks", "Bearer "+token, c.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("期望 400, 实际 %d（body %s）", w.Code, w.Body.String())
			}
			if got := decodeErr(t, w); !strings.Contains(got, c.wantErr) {
				t.Errorf("error 期望含 %q, 实际 %q", c.wantErr, got)
			}
		})
	}
}

// TestHandleCreateTaskImmediate 立即任务（无 start_time）：201、状态直接 queued、
// 进入执行队列，不进定时队列。
func TestHandleCreateTaskImmediate(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()
	ctx := context.Background()

	end := time.Now().Add(time.Hour)
	body := `{"name":"httptest 立即任务","stream_url":"rtmp://example.local/live/a","end_time":"` +
		end.Format(time.RFC3339) + `"}`
	w := doRequest(t, r, http.MethodPost, "/api/tasks", "Bearer "+mustToken(t, s, "admin"), body)
	if w.Code != http.StatusCreated {
		t.Fatalf("期望 201, 实际 %d（body %s）", w.Code, w.Body.String())
	}

	var resp struct {
		Code int `json:"code"`
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v（原始 %s）", err, w.Body.String())
	}
	if resp.Code != 0 {
		t.Fatalf("信封 code 期望 0, 实际 %d（body %s）", resp.Code, w.Body.String())
	}
	cleanupTask(t, resp.Data.ID)

	if resp.Data.Status != string(store.TaskQueued) {
		t.Errorf("立即任务响应状态期望 queued, 实际 %s", resp.Data.Status)
	}
	if task := mustGetTask(t, s, resp.Data.ID); task.Status != store.TaskQueued {
		t.Errorf("DB 状态期望 queued, 实际 %s", task.Status)
	}
	if n, _ := s.queue.QueueLen(ctx); n != 1 {
		t.Errorf("执行队列长度期望 1, 实际 %d", n)
	}
	if n, _ := s.queue.ScheduledLen(ctx); n != 0 {
		t.Errorf("立即任务不应进定时队列, 实际 %d", n)
	}
}

// TestHandleCreateTaskEmptyStartTime start_time 传空串（P-1）：应与省略字段等价，
// 视为立即执行返回 201，而不是 400 请求参数错误。
func TestHandleCreateTaskEmptyStartTime(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()
	ctx := context.Background()

	end := time.Now().Add(time.Hour)
	body := `{"name":"httptest 空串开始时间","stream_url":"rtmp://example.local/live/a","start_time":"","end_time":"` +
		end.Format(time.RFC3339) + `"}`
	w := doRequest(t, r, http.MethodPost, "/api/tasks", "Bearer "+mustToken(t, s, "admin"), body)
	if w.Code != http.StatusCreated {
		t.Fatalf("期望 201, 实际 %d（body %s）", w.Code, w.Body.String())
	}

	var resp struct {
		Code int `json:"code"`
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v（原始 %s）", err, w.Body.String())
	}
	if resp.Code != 0 {
		t.Fatalf("信封 code 期望 0, 实际 %d（body %s）", resp.Code, w.Body.String())
	}
	cleanupTask(t, resp.Data.ID)

	if resp.Data.Status != string(store.TaskQueued) {
		t.Errorf("空串 start_time 应视为立即执行（queued）, 实际 %s", resp.Data.Status)
	}
	if task := mustGetTask(t, s, resp.Data.ID); task.StartTime != nil {
		t.Errorf("空串 start_time 落库应为 NULL, 实际 %v", task.StartTime)
	}
	if n, _ := s.queue.QueueLen(ctx); n != 1 {
		t.Errorf("执行队列长度期望 1, 实际 %d", n)
	}
}

// TestHandleCreateTaskScheduled 定时任务（start_time 未来）：201、状态保持 pending、
// 进入定时队列且 score 为开始时间，不进执行队列。
func TestHandleCreateTaskScheduled(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()
	ctx := context.Background()

	start := time.Now().Add(30 * time.Minute)
	end := start.Add(time.Hour)
	body := `{"id":"test-http-sched","name":"httptest 定时任务","stream_url":"rtmp://example.local/live/a",` +
		`"start_time":"` + start.Format(time.RFC3339) + `","end_time":"` + end.Format(time.RFC3339) + `"}`
	w := doRequest(t, r, http.MethodPost, "/api/tasks", "Bearer "+mustToken(t, s, "admin"), body)
	if w.Code != http.StatusCreated {
		t.Fatalf("期望 201, 实际 %d（body %s）", w.Code, w.Body.String())
	}
	cleanupTask(t, "test-http-sched")

	var resp struct {
		Code int `json:"code"`
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v（原始 %s）", err, w.Body.String())
	}
	if resp.Code != 0 {
		t.Fatalf("信封 code 期望 0, 实际 %d（body %s）", resp.Code, w.Body.String())
	}
	if resp.Data.ID != "test-http-sched" {
		t.Errorf("应沿用外部传入 ID, 实际 %q", resp.Data.ID)
	}
	if resp.Data.Status != string(store.TaskPending) {
		t.Errorf("定时任务响应状态期望 pending, 实际 %s", resp.Data.Status)
	}
	if task := mustGetTask(t, s, "test-http-sched"); task.Status != store.TaskPending {
		t.Errorf("DB 状态期望 pending, 实际 %s", task.Status)
	}
	if n, _ := s.queue.QueueLen(ctx); n != 0 {
		t.Errorf("定时任务不应进执行队列, 实际 %d", n)
	}
	if n, _ := s.queue.ScheduledLen(ctx); n != 1 {
		t.Fatalf("定时队列长度期望 1, 实际 %d", n)
	}
	// score 应为开始时间：开始时间前不可见，开始时间后弹出
	if due, _ := s.queue.PopDue(ctx, start.Add(-time.Minute)); len(due) != 0 {
		t.Errorf("开始时间前不应弹出, 实际 %v", due)
	}
	due, err := s.queue.PopDue(ctx, start.Add(time.Minute))
	if err != nil || len(due) != 1 || due[0] != "test-http-sched" {
		t.Errorf("到开始时间后应弹出定时任务, due=%v err=%v", due, err)
	}
}

// TestHandleCreateTaskDuplicateID 外部传入重复 ID：第二次创建返回 409。
func TestHandleCreateTaskDuplicateID(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()

	end := time.Now().Add(time.Hour)
	body := `{"id":"test-http-dup","stream_url":"rtmp://example.local/live/a","end_time":"` +
		end.Format(time.RFC3339) + `"}`

	w1 := doRequest(t, r, http.MethodPost, "/api/tasks", "Bearer "+mustToken(t, s, "admin"), body)
	if w1.Code != http.StatusCreated {
		t.Fatalf("首次创建期望 201, 实际 %d（body %s）", w1.Code, w1.Body.String())
	}
	cleanupTask(t, "test-http-dup")

	w2 := doRequest(t, r, http.MethodPost, "/api/tasks", "Bearer "+mustToken(t, s, "admin"), body)
	if w2.Code != http.StatusConflict {
		t.Fatalf("重复 ID 期望 409, 实际 %d（body %s）", w2.Code, w2.Body.String())
	}
	if got := decodeErr(t, w2); !strings.Contains(got, "已存在") {
		t.Errorf("error 期望含 \"已存在\", 实际 %q", got)
	}
}

// TestAuthMiddlewareRejectsInvalidTokens 认证中间件（表驱动）：
// 无 token / 非 Bearer / 伪造 token / refresh token 均拒绝，合法 access token 放行。
func TestAuthMiddlewareRejectsInvalidTokens(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()

	refresh, err := s.authMgr.SignRefresh(1, "tester", "admin")
	if err != nil {
		t.Fatalf("签发 refresh token 失败: %v", err)
	}
	access := mustToken(t, s, "admin")

	cases := []struct {
		name    string
		authHdr string // 完整 Authorization 头值，空串表示不带
		wantOK  bool
	}{
		{"无 Authorization 头", "", false},
		{"非 Bearer 前缀（Basic）", "Basic dXNlcjpwYXNz", false},
		{"Bearer 伪造 token", "Bearer eyJhbGciOiJIUzI1NiJ9.fake.sig", false},
		{"Bearer refresh token 当 access 用", "Bearer " + refresh, false},
		{"Bearer 合法 access token", "Bearer " + access, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := doRequest(t, r, http.MethodPost, "/api/tasks/list", c.authHdr, "{}")
			if c.wantOK {
				if w.Code != http.StatusOK {
					t.Fatalf("合法 token 期望 200, 实际 %d（body %s）", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("期望 401, 实际 %d（body %s）", w.Code, w.Body.String())
			}
		})
	}
}

// TestRequireRoleRejectsNonAdmin 管理员接口角色控制：
// 普通用户 token 访问 cancel 返回 403；admin token 通过角色关卡（任务不存在返回 409）。
func TestRequireRoleRejectsNonAdmin(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()

	w := doRequest(t, r, http.MethodPost, "/api/tasks/test-role-404/cancel", "Bearer "+mustToken(t, s, "user"), "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通用户访问管理接口期望 403, 实际 %d（body %s）", w.Code, w.Body.String())
	}
	if got := decodeErr(t, w); !strings.Contains(got, "无权限") {
		t.Errorf("error 期望含 \"无权限\", 实际 %q", got)
	}

	w2 := doRequest(t, r, http.MethodPost, "/api/tasks/test-role-404/cancel", "Bearer "+mustToken(t, s, "admin"), "")
	if w2.Code == http.StatusForbidden {
		t.Fatalf("admin 不应被角色关卡拦截, 实际 403（body %s）", w2.Body.String())
	}
	if w2.Code != http.StatusConflict { // 任务不存在 → Cancel 条件更新失败 → 409
		t.Errorf("admin 访问不存在任务期望 409, 实际 %d（body %s）", w2.Code, w2.Body.String())
	}
}
