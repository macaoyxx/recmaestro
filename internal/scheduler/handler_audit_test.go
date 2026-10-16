package scheduler

// handler_audit_test.go — 审计日志集成测试：登录成功/失败落审计、
// 查询接口仅管理员可用。用户/任务/文件操作的审计写入走同一 s.audit 助手，
// 此处以登录链路为代表做端到端断言。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"recmaestro/internal/auth"
	"recmaestro/internal/store"
)

// clearAuditLogs 清空审计表并注册结束清理（本包测试不并行，直接清表保证可重复运行）。
func clearAuditLogs(t *testing.T, s *Server) {
	t.Helper()
	del := func() {
		if err := testDB(t).Where("1=1").Delete(&store.AuditLog{}).Error; err != nil {
			t.Errorf("清理审计表失败: %v", err)
		}
	}
	del()
	t.Cleanup(del)
}

// TestAuditWrittenOnLogin 登录失败/成功分别落 login_fail / login_success 审计，
// 记录携带用户 ID 与 IP。
func TestAuditWrittenOnLogin(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()
	clearAuditLogs(t, s)

	hash, err := auth.HashPassword("audit-pass-1")
	if err != nil {
		t.Fatalf("加密密码失败: %v", err)
	}
	u := &store.User{Username: "audit-login-user", PasswordHash: hash, Role: store.RoleUser, Status: store.StatusEnabled}
	if err := s.users.Create(context.Background(), u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	t.Cleanup(func() {
		if err := testDB(t).Where("id = ?", u.ID).Delete(&store.User{}).Error; err != nil {
			t.Errorf("清理测试用户失败: %v", err)
		}
	})

	if code := loginAs(t, r, "audit-login-user", "wrong-pass"); code != http.StatusUnauthorized {
		t.Fatalf("错误密码登录期望 401, 实际 %d", code)
	}
	if code := loginAs(t, r, "audit-login-user", "audit-pass-1"); code != http.StatusOK {
		t.Fatalf("正确密码登录期望 200, 实际 %d", code)
	}

	logs, total, err := s.audits.List(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("查询审计失败: %v", err)
	}
	if total != 2 || len(logs) != 2 {
		t.Fatalf("期望恰好 2 条审计（fail+success）, 实际 %d 条: %+v", total, logs)
	}
	// 倒序：最新在前（先 fail 后 success）
	byAction := map[string]store.AuditLog{}
	for _, l := range logs {
		byAction[l.Action] = l
	}
	fail, okF := byAction[store.AuditLoginFail]
	succ, okS := byAction[store.AuditLoginSuccess]
	if !okF || !okS {
		t.Fatalf("期望 login_fail 与 login_success 各一条, 实际 %v", byAction)
	}
	// 名称快照：操作者=登录者本人（用户名随行落库），对象=user/id+用户名
	for name, l := range map[string]store.AuditLog{"fail": fail, "success": succ} {
		if l.OperatorName != u.Username {
			t.Errorf("%s 审计 operator_name 期望 %q, 实际 %q", name, u.Username, l.OperatorName)
		}
		if l.Resource != store.AuditResourceUser || l.ResourceName != u.Username {
			t.Errorf("%s 审计对象三元组不匹配: %s/%s/%s", name, l.Resource, l.ResourceID, l.ResourceName)
		}
	}
	for name, l := range map[string]store.AuditLog{"fail": fail, "success": succ} {
		if l.UserID == nil || *l.UserID != u.ID {
			t.Errorf("%s 审计 user_id 期望 %d, 实际 %v", name, u.ID, l.UserID)
		}
		if l.Resource != "user" || l.ResourceID == "" {
			t.Errorf("%s 审计 resource 信息不完整: %+v", name, l)
		}
	}
}

// TestAuditLogsAdminOnly 查询接口：admin 200 返回分页结构，普通用户 403。
func TestAuditLogsAdminOnly(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()
	clearAuditLogs(t, s)

	w := doRequest(t, r, http.MethodPost, "/api/audit-logs/list", "Bearer "+mustToken(t, s, "user"), "{}")
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通用户查询审计期望 403, 实际 %d", w.Code)
	}

	w2 := doRequest(t, r, http.MethodPost, "/api/audit-logs/list", "Bearer "+mustToken(t, s, "admin"), "{}")
	if w2.Code != http.StatusOK {
		t.Fatalf("admin 查询审计期望 200, 实际 %d（body %s）", w2.Code, w2.Body.String())
	}
	if !containsAll(w2.Body.String(), `"data"`, `"total"`) {
		t.Errorf("审计列表响应应含 data/total 字段, 实际 %s", w2.Body.String())
	}
}

// containsAll 判断 s 是否包含全部子串。
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
