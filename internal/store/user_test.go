package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

// newTestUser 构造一个测试用户，默认 user 角色、enabled 状态。
func newTestUser(username string) *User {
	return &User{
		Username:     username,
		PasswordHash: "hash-" + username,
		Role:         RoleUser,
		Status:       StatusEnabled,
	}
}

// TestUserCreateAndGet 验证用户插入（回填自增 ID）、按用户名/ID 查询字段回读、
// 不存在时返回 (nil, nil)，以及 username 唯一索引对重名插入的拦截。
func TestUserCreateAndGet(t *testing.T) {
	resetTables(t, "live_rec_users")
	ctx := context.Background()
	us := NewUserStore(testDB)

	lastLogin := time.Now().Add(-time.Hour).Truncate(time.Second)
	want := &User{
		Username:     "alice",
		PasswordHash: "hash-alice",
		Role:         RoleAdmin,
		Status:       StatusEnabled,
		LastLoginAt:  &lastLogin,
	}
	if err := us.Create(ctx, want); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if want.ID == 0 {
		t.Fatal("创建后应回填自增 ID")
	}

	got, err := us.GetByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("按用户名查询失败: %v", err)
	}
	if got == nil {
		t.Fatal("用户应存在，实际返回 nil")
	}
	if got.Username != want.Username || got.PasswordHash != want.PasswordHash {
		t.Errorf("基础字段不匹配: %+v", got)
	}
	if got.Role != RoleAdmin || got.Status != StatusEnabled {
		t.Errorf("角色/状态不匹配: role=%q status=%q", got.Role, got.Status)
	}
	if got.FailedLoginCount != 0 {
		t.Errorf("新用户 failed_login_count 应为 0，实际 %d", got.FailedLoginCount)
	}
	if got.LastLoginAt == nil || !sameSecond(*got.LastLoginAt, lastLogin) {
		t.Errorf("last_login_at 不匹配: %+v", got.LastLoginAt)
	}

	byID, err := us.GetByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按 ID 查询失败: %v", err)
	}
	if byID == nil || byID.Username != "alice" {
		t.Errorf("按 ID 查询结果不匹配: %+v", byID)
	}

	// 不存在的用户名/ID 约定返回 (nil, nil) 而非报错
	missName, err := us.GetByUsername(ctx, "nobody")
	if err != nil {
		t.Fatalf("查询不存在用户名应无错误，实际: %v", err)
	}
	if missName != nil {
		t.Errorf("查询不存在用户名应返回 nil，实际 %+v", missName)
	}
	missID, err := us.GetByID(ctx, 999999999)
	if err != nil {
		t.Fatalf("查询不存在 ID 应无错误，实际: %v", err)
	}
	if missID != nil {
		t.Errorf("查询不存在 ID 应返回 nil，实际 %+v", missID)
	}

	// 重名插入必须被 uk_username 唯一索引拒绝，而非静默成功产生两行
	if err := us.Create(ctx, newTestUser("alice")); err == nil {
		t.Error("重名用户创建应因唯一索引冲突报错")
	}
}

// TestUserList 验证用户列表返回全部用户且按 id 升序。
func TestUserList(t *testing.T) {
	resetTables(t, "live_rec_users")
	ctx := context.Background()
	us := NewUserStore(testDB)

	for _, name := range []string{"dave", "erin", "frank"} {
		if err := us.Create(ctx, newTestUser(name)); err != nil {
			t.Fatalf("创建用户 %s 失败: %v", name, err)
		}
	}

	users, err := us.List(ctx)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("用户数 = %d, want 3", len(users))
	}
	for i := 1; i < len(users); i++ {
		if users[i-1].ID >= users[i].ID {
			t.Errorf("列表应按 id 升序: %d >= %d", users[i-1].ID, users[i].ID)
		}
	}
}

// TestUserUpdateStatusAndPassword 验证禁用/启用状态切换与密码哈希更新均按 ID 精确生效。
func TestUserUpdateStatusAndPassword(t *testing.T) {
	resetTables(t, "live_rec_users")
	ctx := context.Background()
	us := NewUserStore(testDB)

	u := newTestUser("gina")
	if err := us.Create(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	if err := us.UpdateStatus(ctx, u.ID, StatusDisabled); err != nil {
		t.Fatalf("禁用用户失败: %v", err)
	}
	got, _ := us.GetByID(ctx, u.ID)
	if got.Status != StatusDisabled {
		t.Errorf("status = %q, want %q", got.Status, StatusDisabled)
	}

	if err := us.UpdateStatus(ctx, u.ID, StatusEnabled); err != nil {
		t.Fatalf("启用用户失败: %v", err)
	}
	got, _ = us.GetByID(ctx, u.ID)
	if got.Status != StatusEnabled {
		t.Errorf("status = %q, want %q", got.Status, StatusEnabled)
	}

	if err := us.UpdatePassword(ctx, u.ID, "hash-new"); err != nil {
		t.Fatalf("更新密码失败: %v", err)
	}
	got, _ = us.GetByID(ctx, u.ID)
	if got.PasswordHash != "hash-new" {
		t.Errorf("password_hash = %q, want %q", got.PasswordHash, "hash-new")
	}
}

// TestUserIncrAndResetFailedLogin 验证登录失败次数原子递增并返回最新值、
// 登录成功后清零并记录最后登录时间；对不存在用户递增必须报错而非空指针崩溃。
func TestUserIncrAndResetFailedLogin(t *testing.T) {
	resetTables(t, "live_rec_users")
	ctx := context.Background()
	us := NewUserStore(testDB)

	u := newTestUser("heidi")
	if err := us.Create(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	c1, err := us.IncrFailedLogin(ctx, u.ID)
	if err != nil {
		t.Fatalf("第一次递增失败: %v", err)
	}
	if c1 != 1 {
		t.Errorf("第一次递增返回 %d, want 1", c1)
	}
	c2, err := us.IncrFailedLogin(ctx, u.ID)
	if err != nil {
		t.Fatalf("第二次递增失败: %v", err)
	}
	if c2 != 2 {
		t.Errorf("第二次递增返回 %d, want 2", c2)
	}

	// 不存在的用户：UPDATE 影响 0 行后回读为 nil，必须转为错误返回（防空指针解引用）
	if _, err := us.IncrFailedLogin(ctx, 999999999); err == nil {
		t.Error("对不存在的用户递增失败次数应报错")
	}

	loginAt := time.Now().Truncate(time.Second)
	if err := us.ResetFailedLogin(ctx, u.ID, loginAt); err != nil {
		t.Fatalf("清零失败次数失败: %v", err)
	}
	got, err := us.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if got.FailedLoginCount != 0 {
		t.Errorf("清零后 failed_login_count = %d, want 0", got.FailedLoginCount)
	}
	if got.LastLoginAt == nil || !sameSecond(*got.LastLoginAt, loginAt) {
		t.Errorf("last_login_at 不匹配: %+v", got.LastLoginAt)
	}
}

// TestUserUnlock 验证 Unlock 仅清零失败计数：锁定状态的计数清零、last_login_at 保持不动。
func TestUserUnlock(t *testing.T) {
	resetTables(t, "live_rec_users")
	ctx := context.Background()
	us := NewUserStore(testDB)

	loginAt := time.Now().Add(-time.Hour).Truncate(time.Second)
	u := &User{
		Username:     "iris",
		PasswordHash: "hash-iris",
		Role:         RoleUser,
		Status:       StatusEnabled,
		LastLoginAt:  &loginAt,
	}
	if err := us.Create(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	// 造出锁定态：失败计数打满 5
	for i := 0; i < 5; i++ {
		if _, err := us.IncrFailedLogin(ctx, u.ID); err != nil {
			t.Fatalf("递增失败次数失败: %v", err)
		}
	}

	if err := us.Unlock(ctx, u.ID); err != nil {
		t.Fatalf("Unlock 失败: %v", err)
	}
	got, err := us.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if got.FailedLoginCount != 0 {
		t.Errorf("Unlock 后 failed_login_count = %d, want 0", got.FailedLoginCount)
	}
	if got.LastLoginAt == nil || !sameSecond(*got.LastLoginAt, loginAt) {
		t.Errorf("Unlock 不应改动 last_login_at: %+v", got.LastLoginAt)
	}
}

// TestUserIncrFailedLoginConcurrent 并发用例：20 个 goroutine 对同一用户各递增一次失败次数。
// 递增由单条 UPDATE 的原子自增完成，落库终值必须恰为 20；并发下回读的返回值允许乱序，但不得报错。
func TestUserIncrFailedLoginConcurrent(t *testing.T) {
	resetTables(t, "live_rec_users")
	ctx := context.Background()
	us := NewUserStore(testDB)

	u := newTestUser("carol")
	if err := us.Create(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	const n = 20
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 统一发令，让 20 个请求同时打向数据库
			if _, err := us.IncrFailedLogin(ctx, u.ID); err != nil {
				errs[i] = err
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d IncrFailedLogin 报错: %v", i, err)
		}
	}

	got, err := us.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if got.FailedLoginCount != n {
		t.Errorf("并发递增后 failed_login_count = %d, want 恰好 %d", got.FailedLoginCount, n)
	}
}

// TestUserDeleteAndAdminGuard 验证物理删除生效，以及"最后一个启用管理员"护栏的计数。
func TestUserDeleteAndAdminGuard(t *testing.T) {
	resetTables(t, "live_rec_users")
	ctx := context.Background()
	us := NewUserStore(testDB)

	victim := newTestUser("del-victim")
	if err := us.Create(ctx, victim); err != nil {
		t.Fatalf("创建用户失败: %v", victim)
	}
	admin2 := &User{Username: "admin2", PasswordHash: "h", Role: RoleAdmin, Status: StatusEnabled}
	if err := us.Create(ctx, admin2); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	// 除 admin2 外启用管理员数：0（victim 是普通用户、库里无其他 admin）
	if n, err := us.CountOtherEnabledAdmins(ctx, admin2.ID); err != nil || n != 0 {
		t.Errorf("CountOtherEnabledAdmins = %d (err %v), want 0", n, err)
	}
	// 再造一个启用 admin 后应为 1
	admin3 := &User{Username: "admin3", PasswordHash: "h", Role: RoleAdmin, Status: StatusEnabled}
	if err := us.Create(ctx, admin3); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}
	if n, err := us.CountOtherEnabledAdmins(ctx, admin2.ID); err != nil || n != 1 {
		t.Errorf("CountOtherEnabledAdmins = %d (err %v), want 1", n, err)
	}

	// 物理删除后查询不到
	if err := us.Delete(ctx, victim.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	got, err := us.GetByID(ctx, victim.ID)
	if err != nil || got != nil {
		t.Errorf("删除后应查不到用户, got %v err %v", got, err)
	}
}
