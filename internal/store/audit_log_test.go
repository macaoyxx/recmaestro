package store

import (
	"context"
	"strconv"
	"testing"
)

// TestAuditCreateAndList 验证审计日志插入（回填自增 ID）与分页倒序查询。
func TestAuditCreateAndList(t *testing.T) {
	resetTables(t, "live_rec_audit_logs")
	ctx := context.Background()
	as := NewAuditStore(testDB)

	uid := uint64(42)
	for i, action := range []string{
		AuditLoginSuccess, AuditLoginFail, AuditTaskAbort, AuditFileDelete, AuditUserCreate,
	} {
		if err := as.Create(ctx, &AuditLog{
			UserID:     &uid,
			Action:     action,
			Resource:   "user",
			ResourceID: "42",
			Detail:     "第" + string(rune('A'+i)) + "条",
			IP:         "127.0.0.1",
			UserAgent:  "unit-test",
		}); err != nil {
			t.Fatalf("写入审计 %s 失败: %v", action, err)
		}
	}

	// 全量：5 条、按 id 倒序（最新在前）
	logs, total, err := as.List(ctx, 1, 10)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if total != 5 || len(logs) != 5 {
		t.Fatalf("total=%d len=%d, want 5/5", total, len(logs))
	}
	if logs[0].Action != AuditUserCreate || logs[4].Action != AuditLoginSuccess {
		t.Errorf("应按时间倒序，首条 %s 末条 %s", logs[0].Action, logs[4].Action)
	}
	if logs[0].UserID == nil || *logs[0].UserID != uid {
		t.Errorf("user_id 应回读为 %d, 实际 %v", uid, logs[0].UserID)
	}

	// 分页：第 2 页每页 3 条 → 2 条
	page2, total2, err := as.List(ctx, 2, 3)
	if err != nil {
		t.Fatalf("List 第 2 页失败: %v", err)
	}
	if total2 != 5 || len(page2) != 2 {
		t.Fatalf("分页 total=%d len=%d, want 5/2", total2, len(page2))
	}

	// UserID 为 NULL（匿名/未知操作者）可正常写入与回读
	if err := as.Create(ctx, &AuditLog{Action: AuditLoginFail, Resource: "user", IP: "10.0.0.8"}); err != nil {
		t.Fatalf("匿名审计写入失败: %v", err)
	}
	last, _, err := as.List(ctx, 1, 1)
	if err != nil || len(last) != 1 {
		t.Fatalf("查最新一条失败: %v", err)
	}
	if last[0].UserID != nil {
		t.Errorf("匿名审计 user_id 应为 NULL, 实际 %v", last[0].UserID)
	}
}

// TestAuditNameSnapshot 验证名称快照列随行落库读回：操作者名称 + 对象三元组（类型/ID/名称）。
func TestAuditNameSnapshot(t *testing.T) {
	resetTables(t, "live_rec_audit_logs", "live_rec_users")
	ctx := context.Background()
	as := NewAuditStore(testDB)
	us := NewUserStore(testDB)

	u := newTestUser("snap-user")
	if err := us.Create(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	uid := u.ID

	if err := as.Create(ctx, &AuditLog{
		UserID:       &uid,
		OperatorName: "admin",
		Action:       AuditUserDisable,
		Resource:     AuditResourceUser,
		ResourceID:   strconv.FormatUint(uid, 10),
		ResourceName: "snap-user",
		Detail:       "名称快照测试",
		IP:           "127.0.0.1",
	}); err != nil {
		t.Fatalf("写入审计失败: %v", err)
	}

	logs, total, err := as.List(ctx, 1, 10)
	if err != nil || total != 1 || len(logs) != 1 {
		t.Fatalf("查询失败: %v total=%d len=%d", err, total, len(logs))
	}
	got := logs[0]
	if got.OperatorName != "admin" {
		t.Errorf("operator_name = %q, want admin", got.OperatorName)
	}
	if got.Resource != AuditResourceUser || got.ResourceID != strconv.FormatUint(uid, 10) {
		t.Errorf("对象类型/ID 不匹配: %s/%s", got.Resource, got.ResourceID)
	}
	if got.ResourceName != "snap-user" {
		t.Errorf("resource_name = %q, want snap-user", got.ResourceName)
	}
}
