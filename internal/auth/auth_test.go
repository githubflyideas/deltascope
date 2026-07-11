package auth

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHashVerify(t *testing.T) {
	h, err := HashPassword("s3cret-密码")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$600000$") {
		t.Fatalf("哈希格式异常: %s", h)
	}
	if !VerifyPassword(h, "s3cret-密码") {
		t.Error("正确口令校验失败")
	}
	if VerifyPassword(h, "wrong") {
		t.Error("错误口令居然通过")
	}
	h2, _ := HashPassword("s3cret-密码")
	if h == h2 {
		t.Error("两次哈希相同, 盐值未生效")
	}
	if VerifyPassword("garbage", "x") || VerifyPassword("a$b$c$d", "x") {
		t.Error("畸形哈希串应校验失败")
	}
}

func TestSessions(t *testing.T) {
	secret, _ := GenerateSecret()
	s := NewSessions(secret, time.Hour)
	tok := s.Issue("admin")
	u, ok := s.Verify(tok)
	if !ok || u != "admin" {
		t.Fatalf("会话校验失败: %v %v", u, ok)
	}
	if _, ok := s.Verify(tok + "x"); ok {
		t.Error("篡改签名应失败")
	}
	body, sig, _ := strings.Cut(tok, ".")
	if _, ok := s.Verify(body + "A." + sig); ok {
		t.Error("篡改 payload 应失败")
	}
	sExp := NewSessions(secret, -time.Minute)
	if _, ok := sExp.Verify(sExp.Issue("admin")); ok {
		t.Error("过期会话应失败")
	}
	secret2, _ := GenerateSecret()
	if _, ok := NewSessions(secret2, time.Hour).Verify(tok); ok {
		t.Error("异密钥校验应失败")
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(3, time.Hour)
	ip := "10.0.0.1"
	for i := 0; i < 3; i++ {
		if ok, _ := rl.Allow(ip); !ok {
			t.Fatalf("第 %d 次尝试不应被拒", i+1)
		}
		rl.Fail(ip)
	}
	if ok, wait := rl.Allow(ip); ok || wait <= 0 {
		t.Error("超限后应被拒且返回剩余时长")
	}
	if ok, _ := rl.Allow("10.0.0.2"); !ok {
		t.Error("其他 IP 不应受影响")
	}
	rl.Reset(ip)
	if ok, _ := rl.Allow(ip); !ok {
		t.Error("Reset 后应恢复")
	}
}

func TestPBKDF2Vectors(t *testing.T) {
	cases := []struct {
		iter int
		want string
	}{
		{1, "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"},
		{4096, "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"},
	}
	for _, c := range cases {
		got := fmt.Sprintf("%x", pbkdf2SHA256([]byte("password"), []byte("salt"), c.iter, 32))
		if got != c.want {
			t.Errorf("iter=%d got %s want %s", c.iter, got, c.want)
		}
	}
}
