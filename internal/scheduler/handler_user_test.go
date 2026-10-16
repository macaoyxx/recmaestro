package scheduler

// handler_user_test.go — 账号锁定解锁闭环测试：
// 登录失败 5 次触发锁定（disabled + 计数≥5）后，管理员重置密码或重新启用账号
// 必须同步清零失败计数，账号才能重新登录（修复：锁定后永久无法登录）。

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"recmaestro/internal/auth"
	"recmaestro/internal/store"
)

// lockUser 造出锁定态用户：enabled + 真实密码哈希，失败计数打满 5 并置 disabled
// （复刻 handleLogin 的锁定路径），注册自清理。
func lockUser(t *testing.T, s *Server, username, password string) *store.User {
	t.Helper()
	ctx := context.Background()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("加密密码失败: %v", err)
	}
	u := &store.User{
		Username:     username,
		PasswordHash: hash,
		Role:         store.RoleUser,
		Status:       store.StatusEnabled,
	}
	if err := s.users.Create(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	t.Cleanup(func() {
		if err := testDB(t).Where("id = ?", u.ID).Delete(&store.User{}).Error; err != nil {
			t.Errorf("清理测试用户 %s 失败: %v", username, err)
		}
	})
	for i := 0; i < 5; i++ {
		if _, err := s.users.IncrFailedLogin(ctx, u.ID); err != nil {
			t.Fatalf("递增失败次数失败: %v", err)
		}
	}
	if err := s.users.UpdateStatus(ctx, u.ID, store.StatusDisabled); err != nil {
		t.Fatalf("置禁用失败: %v", err)
	}
	return u
}

// loginAs 以指定密码发起登录，返回状态码。
func loginAs(t *testing.T, r http.Handler, username, password string) int {
	t.Helper()
	w := doRequest(t, r, http.MethodPost, "/api/login", "",
		`{"username":"`+username+`","password":"`+password+`"}`)
	return w.Code
}

// TestLockedUserCanLoginAfterPasswordReset 锁定 → 管理员重置密码 → 新旧密码登录表现：
// 旧密码被拒（401）、新密码可登录（200），不再被残留失败计数拦成 403。
func TestLockedUserCanLoginAfterPasswordReset(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()
	u := lockUser(t, s, "lock-reset-user", "old-pass-123")

	// 锁定态直接登录：被计数拦截（403），确认起点确实锁死
	if code := loginAs(t, r, "lock-reset-user", "old-pass-123"); code != http.StatusForbidden {
		t.Fatalf("锁定账号登录期望 403, 实际 %d", code)
	}

	w := doRequest(t, r, http.MethodPatch, "/api/users/"+strconv.FormatUint(u.ID, 10)+"/password",
		"Bearer "+mustToken(t, s, "admin"), `{"password":"new-pass-456"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("重置密码期望 200, 实际 %d（body %s）", w.Code, w.Body.String())
	}

	// 重置密码不清禁用状态：先启用，再验证登录
	w = doRequest(t, r, http.MethodPatch, "/api/users/"+strconv.FormatUint(u.ID, 10)+"/status",
		"Bearer "+mustToken(t, s, "admin"), `{"status":"enabled"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("启用账号期望 200, 实际 %d（body %s）", w.Code, w.Body.String())
	}

	if code := loginAs(t, r, "lock-reset-user", "new-pass-456"); code != http.StatusOK {
		t.Errorf("重置密码+启用后登录期望 200, 实际 %d", code)
	}
}

// TestLockedUserCanLoginAfterEnable 锁定 → 管理员直接重新启用（不重置密码）→ 原密码可登录。
func TestLockedUserCanLoginAfterEnable(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()
	u := lockUser(t, s, "lock-enable-user", "keep-pass-789")

	w := doRequest(t, r, http.MethodPatch, "/api/users/"+strconv.FormatUint(u.ID, 10)+"/status",
		"Bearer "+mustToken(t, s, "admin"), `{"status":"enabled"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("启用账号期望 200, 实际 %d（body %s）", w.Code, w.Body.String())
	}

	if code := loginAs(t, r, "lock-enable-user", "keep-pass-789"); code != http.StatusOK {
		t.Errorf("重新启用后原密码登录期望 200, 实际 %d", code)
	}
}

// TestLoginMessageLockedVsDisabled 锁定与禁用两种成因的登录提示必须可区分（Issue 单机基线-1）：
// 锁定复用 disabled 状态，若禁用检查先于失败计数检查，锁定用户会被误报「账号已被禁用」。
// 锁定成因（disabled + 计数≥5）→ 403「账号已锁定，请联系管理员」；
// 管理员禁用成因（disabled + 计数<5）→ 403「账号已被禁用」。
func TestLoginMessageLockedVsDisabled(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()
	ctx := context.Background()

	// 成因一：连续错密码锁定（disabled + 计数打满 5）
	lockUser(t, s, "lock-msg-user", "lock-pass-1")
	w := doRequest(t, r, http.MethodPost, "/api/login", "",
		`{"username":"lock-msg-user","password":"lock-pass-1"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("锁定账号登录期望 403, 实际 %d（body %s）", w.Code, w.Body.String())
	}
	if msg := decodeErr(t, w); msg != "账号已锁定，请联系管理员" {
		t.Errorf("锁定成因提示期望「账号已锁定，请联系管理员」, 实际「%s」", msg)
	}

	// 成因二：管理员禁用（disabled + 计数为 0）
	hash, err := auth.HashPassword("disable-pass-2")
	if err != nil {
		t.Fatalf("加密密码失败: %v", err)
	}
	u := &store.User{Username: "disable-msg-user", PasswordHash: hash, Role: store.RoleUser, Status: store.StatusEnabled}
	if err := s.users.Create(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	t.Cleanup(func() {
		_ = testDB(t).Where("id = ?", u.ID).Delete(&store.User{}).Error
	})
	if err := s.users.UpdateStatus(ctx, u.ID, store.StatusDisabled); err != nil {
		t.Fatalf("置禁用失败: %v", err)
	}
	w2 := doRequest(t, r, http.MethodPost, "/api/login", "",
		`{"username":"disable-msg-user","password":"disable-pass-2"}`)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("禁用账号登录期望 403, 实际 %d（body %s）", w2.Code, w2.Body.String())
	}
	if msg := decodeErr(t, w2); msg != "账号已被禁用" {
		t.Errorf("禁用成因提示期望「账号已被禁用」, 实际「%s」", msg)
	}
}

// TestDeleteUserGuards 删除用户护栏：不能删自己（409）、不存在 404、删普通用户成功 200。
func TestDeleteUserGuards(t *testing.T) {
	s, _ := newTestScheduler(t)
	r := s.Router()
	ctx := context.Background()
	token := "Bearer " + mustToken(t, s, "admin") // 操作者 user_id=1

	// 造一个普通用户
	hash, err := auth.HashPassword("pass-1")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	u := &store.User{Username: "del-guard-user", PasswordHash: hash, Role: store.RoleUser, Status: store.StatusEnabled}
	if err := s.users.Create(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	t.Cleanup(func() {
		_ = testDB(t).Where("id = ?", u.ID).Delete(&store.User{}).Error
	})

	// 删自己：造一个真实 admin 并以它本人的 token 发起（mustToken 签的 uid=1 在测试库无实体，会先 404）
	selfAdmin := &store.User{Username: "del-self-admin", PasswordHash: "h", Role: store.RoleAdmin, Status: store.StatusEnabled}
	if err := s.users.Create(ctx, selfAdmin); err != nil {
		t.Fatalf("创建自删管理员失败: %v", err)
	}
	t.Cleanup(func() {
		_ = testDB(t).Where("id = ?", selfAdmin.ID).Delete(&store.User{}).Error
	})
	selfTok, err := s.authMgr.SignAccess(selfAdmin.ID, selfAdmin.Username, "admin")
	if err != nil {
		t.Fatalf("签发 token 失败: %v", err)
	}
	w := doRequest(t, r, http.MethodDelete, "/api/users/"+strconv.FormatUint(selfAdmin.ID, 10), "Bearer "+selfTok, "")
	if w.Code != http.StatusConflict {
		t.Errorf("删除自己期望 409, 实际 %d（body %s）", w.Code, w.Body.String())
	}
	// 不存在
	w2 := doRequest(t, r, http.MethodDelete, "/api/users/999999999", token, "")
	if w2.Code != http.StatusNotFound {
		t.Errorf("删除不存在用户期望 404, 实际 %d", w2.Code)
	}
	// 内置 admin 不可删：造一个 username=admin 的用户（测试库默认没有）
	builtin := &store.User{Username: store.BuiltinAdminUsername, PasswordHash: "h", Role: store.RoleAdmin, Status: store.StatusEnabled}
	if err := s.users.Create(ctx, builtin); err != nil {
		t.Fatalf("创建内置 admin 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = testDB(t).Where("id = ?", builtin.ID).Delete(&store.User{}).Error
	})
	wBuiltin := doRequest(t, r, http.MethodDelete, "/api/users/"+strconv.FormatUint(builtin.ID, 10), token, "")
	if wBuiltin.Code != http.StatusConflict {
		t.Errorf("删除内置 admin 期望 409, 实际 %d（body %s）", wBuiltin.Code, wBuiltin.Body.String())
	}
	// 内置 admin 不可禁用；自删管理员的账号也不可自禁
	wDis := doRequest(t, r, http.MethodPatch, "/api/users/"+strconv.FormatUint(builtin.ID, 10)+"/status", token, `{"status":"disabled"}`)
	if wDis.Code != http.StatusConflict {
		t.Errorf("禁用内置 admin 期望 409, 实际 %d（body %s）", wDis.Code, wDis.Body.String())
	}
	selfDis := doRequest(t, r, http.MethodPatch, "/api/users/"+strconv.FormatUint(selfAdmin.ID, 10)+"/status", "Bearer "+selfTok, `{"status":"disabled"}`)
	if selfDis.Code != http.StatusConflict {
		t.Errorf("禁用当前登录账号期望 409, 实际 %d（body %s）", selfDis.Code, selfDis.Body.String())
	}

	// 删普通用户成功
	w3 := doRequest(t, r, http.MethodDelete, "/api/users/"+strconv.FormatUint(u.ID, 10), token, "")
	if w3.Code != http.StatusOK {
		t.Fatalf("删除普通用户期望 200, 实际 %d（body %s）", w3.Code, w3.Body.String())
	}
	if left, err := s.users.GetByID(ctx, u.ID); err != nil || left != nil {
		t.Errorf("删除后应查不到, got %v err %v", left, err)
	}
}
