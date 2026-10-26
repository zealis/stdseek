// Package usage 按天累计运行期用量（检索 / 抓取 / 入库 / 原文摘要）。
//
// 为什么不在请求路径上直接写 SQLite：检索热路径约 8ms，而 SQLite 是单连接
// （MaxOpenConns=1），每次搜索写一次库会和检索、抓取抢同一条连接。这里在内存
// 累加，每 30 秒与日切时批量落盘——最多丢一个 flush 周期的计数。
// 用量只服务于「看趋势」，不做账务，所以这个精度足够。
package usage

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"voidxo/internal/store"
)

// Tracker 内存累加器 + 周期落盘。
type Tracker struct {
	st *store.Store

	mu  sync.Mutex
	cur store.UsageRow // 今日尚未落盘的增量
	// dims 当日维度计数：kind（origin/country）→ name → 次数。
	// 与 cur 同生命周期，flush 成功后一起清空。
	dims map[string]map[string]int64
}

// New 构造累加器。st 为 nil 时所有方法都是空操作（测试或未启用数据库时）。
func New(st *store.Store) *Tracker {
	return &Tracker{st: st}
}

// today 返回本地时区的日期键。用本地时区而不是 UTC：运营看的是「今天」，
// 与服务器所在时区的自然日一致才不会出现半夜换日的困惑。
func today() string {
	return time.Now().Format("2006-01-02")
}

// AddSearch 记一次检索请求。
//
// ok 表示这次命中了至少一条结果（区别于"HTTP 200 但零结果"）；
// isAPI 表示调用方不是网页端——网页端会带 X-Voidxo-Client: web。
func (t *Tracker) AddSearch(hits int, ok, isAPI bool) {
	t.add(func(r *store.UsageRow) {
		r.Searches++
		if ok {
			r.Succeeded++
		}
		if isAPI {
			r.API++
		}
		r.Hits += int64(hits)
	})
}

// AddSummary 记一次原文摘要生成（成功产出了结论才计）。
func (t *Tracker) AddSummary() {
	t.add(func(r *store.UsageRow) { r.Summaries++ })
}

// AddCrawl 记一轮抓取：pages 为实际抓取页数，indexed 为入索引篇数。
func (t *Tracker) AddCrawl(pages, indexed int) {
	if pages <= 0 && indexed <= 0 {
		return
	}
	t.add(func(r *store.UsageRow) {
		r.Crawls += int64(pages)
		r.Indexed += int64(indexed)
	})
}

// add 在锁内累加，并在跨日时把前一天的余量先落盘。
func (t *Tracker) add(fn func(*store.UsageRow)) {
	if t == nil || t.st == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureDayLocked()
	fn(&t.cur)
}

// AddOrigin 记一次来自某个域名的 API 调用（"" 表示没有来源头，即直连）。
func (t *Tracker) AddOrigin(host string) {
	t.addDim("origin", host)
}

// AddCountry 记一次来自某个地区的请求（地区码由部署方配置的 CDN 头提供）。
func (t *Tracker) AddCountry(code string) {
	t.addDim("country", code)
}

// addDim 累加维度计数。空名字归一成占位符，避免统计里出现一堆看不见的空行。
func (t *Tracker) addDim(kind, name string) {
	if t == nil || t.st == nil {
		return
	}
	if name == "" {
		name = "（未知）"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureDayLocked()
	if t.dims == nil {
		t.dims = make(map[string]map[string]int64)
	}
	if t.dims[kind] == nil {
		t.dims[kind] = make(map[string]int64)
	}
	t.dims[kind][name]++
}

// ensureDayLocked 保证 cur/dims 指向今天；跨日时先把昨天的余量落盘。要求持有 t.mu。
func (t *Tracker) ensureDayLocked() {
	day := today()
	if t.cur.Day == day {
		return
	}
	if t.cur.Day != "" {
		_ = t.flushLocked()
	}
	t.cur = store.UsageRow{Day: day}
	t.dims = make(map[string]map[string]int64)
}

// Flush 把内存里的增量写进 SQLite。
func (t *Tracker) Flush(ctx context.Context) error {
	if t == nil || t.st == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.flushLocked()
}

// flushLocked 要求调用方已持锁。
//
// 写成功后把 cur 清零但保留 Day：同一天后续的累加继续往这一格加，
// 不会因为 flush 而把日期重置掉。
func (t *Tracker) flushLocked() error {
	r := t.cur
	if r.Day == "" {
		return nil
	}
	hasDaily := r.Searches != 0 || r.Succeeded != 0 || r.API != 0 || r.Hits != 0 ||
		r.Crawls != 0 || r.Indexed != 0 || r.Summaries != 0
	if !hasDaily && len(t.dims) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if hasDaily {
		if err := t.st.AddUsage(ctx, r); err != nil {
			// 失败保留数据，等下一个周期再试——丢一天的量比晚 30 秒更糟
			slog.Warn("usage flush failed", "day", r.Day, "err", err)
			return err
		}
	}
	for kind, counts := range t.dims {
		if err := t.st.AddUsageDims(ctx, r.Day, kind, counts); err != nil {
			slog.Warn("usage dims flush failed", "day", r.Day, "kind", kind, "err", err)
			return err
		}
	}
	t.cur = store.UsageRow{Day: r.Day}
	t.dims = make(map[string]map[string]int64)
	return nil
}

// Run 周期落盘，直到 ctx 取消。退出时会做最后一次 flush。
func (t *Tracker) Run(ctx context.Context, every time.Duration) {
	if t == nil || t.st == nil {
		return
	}
	if every <= 0 {
		every = 30 * time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = t.Flush(context.Background())
			return
		case <-ticker.C:
			_ = t.Flush(ctx)
		}
	}
}

// TopDims 返回某个维度在最近 days 天内的前 limit 名（先 flush 再查）。
func (t *Tracker) TopDims(ctx context.Context, days int, kind string, limit int) ([]store.DimRow, error) {
	if t == nil || t.st == nil {
		return nil, nil
	}
	if days <= 0 {
		days = 30
	}
	_ = t.Flush(ctx)
	since := time.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	return t.st.TopUsageDims(ctx, since, kind, limit)
}

// Snapshot 返回最近 days 天的用量（含今日未落盘部分），按日期升序。
//
// 先 flush 再读库：这样调用方不必自己合并内存值，也不会有「刚搜完却看不到」
// 的困惑。一次 flush 就是一条 UPSERT，代价可以忽略。
func (t *Tracker) Snapshot(ctx context.Context, days int) ([]store.UsageRow, error) {
	if t == nil || t.st == nil {
		return nil, nil
	}
	if days <= 0 {
		days = 30
	}
	_ = t.Flush(ctx)
	since := time.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	return t.st.ListUsage(ctx, since)
}
