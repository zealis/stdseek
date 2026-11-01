package crawler

import (
	"testing"
	"time"
)

// 下限与上限：快站不该比配置间隔更急，慢站最多等 ceil。
func TestThrottleFloorsAndCeilings(t *testing.T) {
	th := newThrottle(2 * time.Second)
	if th.delay() != 2*time.Second {
		t.Fatalf("ema 为 0 时应按下限等，实际 %v", th.delay())
	}
	th.observe(50*time.Millisecond, 200)
	if th.delay() != 2*time.Second {
		t.Fatalf("快站不该比配置间隔更急，实际 %v", th.delay())
	}
	for i := 0; i < 5; i++ {
		th.observe(20*time.Second, 200)
	}
	if th.delay() != throttleCeil {
		t.Fatalf("慢站最多等 %v，实际 %v", throttleCeil, th.delay())
	}
	// 夸张的 Crawl-delay（比如 3600 秒）按硬上限处理：
	// 它表达的是「慢一点」，不是「别来」，真原样尊重等于放弃整站。
	hard := &throttle{floor: time.Hour, ceil: throttleCeil}
	if hard.delay() != throttleHardMax {
		t.Fatalf("超过硬上限的 Crawl-delay 应夹到 %v，实际 %v", throttleHardMax, hard.delay())
	}
}

// 持续的同级延迟会让滑动平均收敛过去：这就是「按服务器快慢自适应」。
func TestThrottleConvergesToLatency(t *testing.T) {
	th := newThrottle(100 * time.Millisecond)
	for i := 0; i < 50; i++ {
		th.observe(1*time.Second, 200)
	}
	if th.delay() != 1*time.Second {
		t.Fatalf("持续 1s 的响应延迟应收敛到 1s，实际 %v", th.delay())
	}
}

// 失败（连不上、429、5xx）说明对方吃紧：至少抬到下限的 1.5 倍，
// 且逐次再乘 1.5——快速失败不等于可以紧接着再撞一次。
func TestThrottleBacksOffOnError(t *testing.T) {
	th := newThrottle(2 * time.Second)
	th.observe(10*time.Millisecond, 0)
	if th.delay() != 3*time.Second {
		t.Fatalf("传输层失败后应抬到下限的 1.5 倍，实际 %v", th.delay())
	}
	th.observe(10*time.Millisecond, 503)
	if th.delay() != 4500*time.Millisecond {
		t.Fatalf("再次失败应继续抬 1.5 倍，实际 %v", th.delay())
	}
	// 成功后向真实延迟收敛，限速会松下来
	for i := 0; i < 30; i++ {
		th.observe(50*time.Millisecond, 200)
	}
	if th.delay() != 2*time.Second {
		t.Fatalf("恢复后应回到下限，实际 %v", th.delay())
	}
}
