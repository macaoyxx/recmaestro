package auth

import "testing"

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("secret123")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if hash == "secret123" {
		t.Errorf("密码不应明文存储")
	}
	if !CheckPassword(hash, "secret123") {
		t.Errorf("正确密码应校验通过")
	}
	if CheckPassword(hash, "wrong") {
		t.Errorf("错误密码不应校验通过")
	}
}
