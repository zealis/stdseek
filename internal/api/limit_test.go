package api

import (
	"testing"
	"time"
)

func TestRateLimiterTokenBucket(t *testing.T) {
	l := newRateLimiter(2) // 2 次/分钟
	now := time.Now()
	if !l.allow("1.2.3.4", now) || !l.allow("1.2.3.4", now) {
		t.Fatal("first two requests should pass")
	}
	if l.allow("1.2.3.4", now) {
		t.Fatal("third request should be limited")
	}
	// 不同 IP 互不影响
	if !l.allow("5.6.7.8", now) {
		t.Fatal("other ip should have its own bucket")
	}
	// 30 秒补充 1 个令牌
	if !l.allow("1.2.3.4", now.Add(31*time.Second)) {
		t.Fatal("token should refill after 31s")
	}
}
