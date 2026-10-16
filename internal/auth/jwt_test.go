package auth

import (
	"testing"
	"time"
)

func TestSignAndParseAccess(t *testing.T) {
	m := NewManager("test-secret", time.Hour, 7*24*time.Hour)
	token, err := m.SignAccess(1, "admin", "admin")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	claims, err := m.Parse(token)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if claims.UserID != 1 || claims.Username != "admin" || claims.Role != "admin" {
		t.Errorf("claims 不匹配: %+v", claims)
	}
	if claims.Type != TokenAccess {
		t.Errorf("期望 access token，实际 %q", claims.Type)
	}
}

func TestParseExpired(t *testing.T) {
	m := NewManager("test-secret", -time.Minute, time.Hour) // access 已过期
	token, err := m.SignAccess(1, "admin", "admin")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if _, err := m.Parse(token); err == nil {
		t.Errorf("期望过期 token 解析失败")
	}
}

func TestParseTampered(t *testing.T) {
	m := NewManager("test-secret", time.Hour, time.Hour)
	token, _ := m.SignAccess(1, "admin", "admin")
	if _, err := m.Parse(token + "x"); err == nil {
		t.Errorf("期望篡改 token 解析失败")
	}
}

func TestParseWrongSecret(t *testing.T) {
	m1 := NewManager("secret-a", time.Hour, time.Hour)
	m2 := NewManager("secret-b", time.Hour, time.Hour)
	token, _ := m1.SignAccess(1, "admin", "admin")
	if _, err := m2.Parse(token); err == nil {
		t.Errorf("期望错误 secret 解析失败")
	}
}
