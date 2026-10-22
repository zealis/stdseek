package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// 简易令牌桶限流：按客户端 IP 限制 /api/* 请求速率（内存实现，单进程）。
type rateLimiter struct {
	mu     sync.Mutex
	bucket map[string]*tokenBucket
	rate   float64 // 每秒补充令牌
	burst  float64
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMinute float64) *rateLimiter {
	return &rateLimiter{
		bucket: map[string]*tokenBucket{},
		rate:   perMinute / 60,
		burst:  perMinute,
	}
}

func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.bucket[key]
	if !ok {
		// 简单清理，避免长期运行后 map 无限增长
		if len(l.bucket) > 10000 {
			for k, v := range l.bucket {
				if now.Sub(v.last) > 10*time.Minute {
					delete(l.bucket, k)
				}
			}
		}
		b = &tokenBucket{tokens: l.burst, last: now}
		l.bucket[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// middleware 对超限请求返回 429。
func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r), time.Now()) {
			writeErr(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// maxBody 限制请求体大小（1MB），防止无鉴权接口被大包拖垮。
func maxBody(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	}
}
