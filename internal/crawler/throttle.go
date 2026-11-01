// 域内请求节奏的自适应限速，思想抄自 Scrapy 的 AutoThrottle。
//
// 固定间隔两头不讨好：对快站是浪费（响应 100ms 也傻等满 2 秒），
// 对慢站是伤害（响应 8 秒还每 2 秒顶一个请求，等于在对方喘气时推门）。
// 按响应延迟的滑动平均调整下一次请求前的等待：快站保持礼貌下限，
// 慢站自动放慢。判据只有延迟与状态码，没有站点专有配置——换站、改版、
// 换语言都不失效。
package crawler

import (
	"context"
	"net/http"
	"time"
)

const (
	// throttleCeil 慢站的等待上限：响应再慢，也不把一轮抓取拖到分钟级。
	throttleCeil = 8 * time.Second
	// throttleHardMax robots.txt 的 Crawl-delay 可以写得很夸张（3600 秒），
	// 原样尊重等于放弃整站；超过这个值按上限抓——站点想完全免抓该写 Disallow，
	// Crawl-delay 表达的是「慢一点」，不是「别来」。
	throttleHardMax = 60 * time.Second
)

// throttle 域内请求节奏。
//
// 下限永远不低于 DomainDelay（调用方会把 robots.txt 的 Crawl-delay 并进来，
// 取大者——Heritrix 与 Nutch 都尊重这条指令）：自适应只会更礼貌，不会更激进。
type throttle struct {
	floor time.Duration
	ceil  time.Duration
	ema   time.Duration // 响应延迟的指数滑动平均（α=0.3）
}

func newThrottle(floor time.Duration) *throttle {
	return &throttle{floor: floor, ceil: throttleCeil}
}

// delay 下一次请求前的等待：滑动平均夹进 [floor, ceil]，再被 robots 的
// Crawl-delay（已在 floor 里）和硬上限约束。
func (t *throttle) delay() time.Duration {
	d := t.ema
	if d > t.ceil {
		d = t.ceil
	}
	if d < t.floor {
		d = t.floor
	}
	if d > throttleHardMax {
		d = throttleHardMax
	}
	return d
}

// wait 睡到下一次请求该发的时间；ctx 取消时提前返回。
func (t *throttle) wait(ctx context.Context) error {
	return sleepCtx(ctx, t.delay())
}

// observe 用一次请求的结果更新滑动平均。status 为 0 表示传输层失败。
//
// 出错（连不上、429、5xx）说明对方吃紧或链路抖动：延迟按 1.5 倍抬，
// 且至少抬到下限的 1.5 倍——快速失败不等于可以紧接着再撞一次。
// 成功则向本次延迟收敛；下限保证快站不会比配置的礼貌间隔更急。
func (t *throttle) observe(latency time.Duration, status int) {
	if status == 0 || status == http.StatusTooManyRequests || status >= 500 {
		t.ema = max(t.ema*3/2, t.floor*3/2)
		return
	}
	if t.ema == 0 {
		t.ema = latency
		return
	}
	t.ema = t.ema*7/10 + latency*3/10
}
