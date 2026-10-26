package auth

import (
	"strings"
	"testing"
	"time"
)

// 邮箱校验：能过的是常见形态，不能过的是会让人卡在注册页的输入。
func TestValidEmail(t *testing.T) {
	ok := []string{"a@b.co", "user.name+tag@example.com", "x@sub.domain.org"}
	bad := []string{"", "a", "a@b", "a@b.", "@b.com", "a@.com", "a b@c.com", "a@@b.com"}
	for _, e := range ok {
		if !ValidEmail(e) {
			t.Errorf("%q 应该通过", e)
		}
	}
	for _, e := range bad {
		if ValidEmail(e) {
			t.Errorf("%q 不该通过", e)
		}
	}
}

// 归一化必须幂等：注册写入的小写值，登录时用同样规则才能命中。
func TestNormalizeEmail(t *testing.T) {
	if got := NormalizeEmail("  Foo@Bar.COM "); got != "foo@bar.com" {
		t.Fatalf("got %q", got)
	}
	if NormalizeEmail(NormalizeEmail("A@B.CO")) != "a@b.co" {
		t.Fatal("归一化不幂等")
	}
}

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("hunter2hunter2")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !VerifyPassword(hash, "hunter2hunter2") {
		t.Fatal("正确密码应通过")
	}
	if VerifyPassword(hash, "hunter2hunter3") {
		t.Fatal("错误密码不该通过")
	}
	// 短密码在哈希前就被拒，不会走到 bcrypt
	if _, err := HashPassword("short"); err != ErrWeakPassword {
		t.Fatalf("短密码应返回 ErrWeakPassword，得到 %v", err)
	}
	// 超过 bcrypt 72 字节上限要明确拒绝，否则用户以为设了超长密码
	if _, err := HashPassword(strings.Repeat("a", 100)); err != ErrLongPassword {
		t.Fatalf("超长密码应返回 ErrLongPassword，得到 %v", err)
	}
}

// TOTP：同一时间步内稳定，前后各一步可容错。
func TestTOTP(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	now := time.Now()
	code, err := TOTPCode(secret, now)
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("应为 6 位，得到 %q", code)
	}
	if !VerifyTOTP(secret, code) {
		t.Fatal("当前验证码应通过")
	}
	if VerifyTOTP(secret, "000000") && code != "000000" {
		t.Fatal("错误验证码不该通过")
	}
	if VerifyTOTP("", code) {
		t.Fatal("空密钥不该通过")
	}
	// 两个不同密钥不该产生同一个码
	other, _ := NewTOTPSecret()
	if other == secret {
		t.Fatal("密钥应随机")
	}
}

// 登录限流：10 次后锁定，重置后恢复。
func TestLimiter(t *testing.T) {
	l := newLimiter()
	key := "u:1|127.0.0.1"
	for i := 1; i <= maxLoginFails; i++ {
		left := l.fail(key)
		if i < maxLoginFails && left != maxLoginFails-i {
			t.Fatalf("第 %d 次失败后剩余应为 %d，得到 %d", i, maxLoginFails-i, left)
		}
		if i == maxLoginFails && left != 0 {
			t.Fatalf("最后一次失败后应为 0，得到 %d", left)
		}
	}
	if locked, _ := l.locked(key); !locked {
		t.Fatal("达到上限后应锁定")
	}
	l.reset(key)
	if locked, _ := l.locked(key); locked {
		t.Fatal("重置后不该还锁着")
	}
	// 不同 key 的预算互相独立
	if locked, _ := l.locked("u:2|127.0.0.1"); locked {
		t.Fatal("另一个账号不该被连带锁定")
	}
}

func TestTokenStable(t *testing.T) {
	plain, hash, err := NewToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if plain == hash || len(hash) != 64 {
		t.Fatalf("哈希不正确: plain=%q hash=%q", plain, hash)
	}
	if HashToken(plain) != hash {
		t.Fatal("同一明文必须得到同一哈希")
	}
	other, _, _ := NewToken()
	if other == plain {
		t.Fatal("令牌应随机")
	}
}
