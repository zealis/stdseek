// 搜索结果缓存。
//
// 为什么需要：压测实测（5 万篇索引）命中集过万的查询 P95 约 26ms、单条最慢 47ms，
// 而这类查询恰恰是最常被点的（「go」「安装」这种短词）。Bleve 的开销集中在
// 扫 posting 与 Top-K 排序，同样的查询重复算一遍纯属浪费。
//
// 为什么不能只加 TTL：索引随时会被爬虫写入。如果缓存只按时间失效，
// 用户会在删除某篇噪声页之后的 TTL 窗口内继续看到它——这是"内容已下线但还能搜到"，
// 比慢一点严重得多。所以缓存条目的有效性绑定 index.Version()：
// 任何写入/删除都会让代数变化，旧条目整表作废。
package api

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"voidxo/internal/index"
)

const (
	// searchCacheMax 条目上限。一条 = 一次查询的原始命中（≤100 条 Hit）。
	// 512 条约几 MB 量级，够覆盖一天里的常见查询。
	searchCacheMax = 512
	// searchCacheTTL 兜底过期时间。
	// 索引代数只能感知「文档增删」，感知不到 prosperity/分类的定时回写，
	// 那些信号会改变排序结果——所以即使索引没变，也要定期重算。
	searchCacheTTL = 10 * time.Minute
	// admitMax 「只出现过一次」的待观察查询上限。
	admitMax = 4096
	// cacheableRunes / cacheableTerms 超过这个规模的查询不进缓存。
	// 真实查询没有这么长的；这种形状基本只可能来自脚本，缓存它只会挤掉热点。
	cacheableRunes = 64
	cacheableTerms = 8
)

type searchEntry struct {
	hits  []index.Hit
	total int
	at    time.Time
}

type searchCache struct {
	mu  sync.Mutex
	m   map[string]searchEntry
	gen int64 // 当前表对应的索引代数
	// seen 只出现过一次的查询（准入观察名单）。见 put 里的说明。
	seen map[string]time.Time

	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
}

func newSearchCache() *searchCache {
	return &searchCache{
		m:    make(map[string]searchEntry, searchCacheMax),
		seen: make(map[string]time.Time, admitMax),
	}
}

// cacheableQuery 这次查询值不值得进缓存。
//
// 长尾 / 机器生成的查询没有复用价值：写进去占一个位置，却几乎不会再被命中。
func cacheableQuery(q index.Query) bool {
	if len([]rune(q.Q)) > cacheableRunes {
		return false
	}
	terms := len(strings.Fields(q.Q)) + len(q.Required) + len(q.Excluded) + len(q.Phrases)
	return terms <= cacheableTerms
}

// key 查询标识。exclude 是有序的（来自 URL 参数顺序），但语义上顺序无关，
// 排序后再拼，避免 no=ai,ads 与 no=ads,ai 各存一份。
func (c *searchCache) key(gen int64, q index.Query) string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(q.SinceSec, 10))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(q.Page))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(q.Size))
	b.WriteByte('|')
	b.WriteString(q.Q)
	b.WriteByte('|')
	// 语法约束同样是查询的一部分：不写进键里，「+北京」和「北京」会共用一份缓存
	b.WriteString(strings.Join(q.Required, "\x00"))
	b.WriteByte('|')
	b.WriteString(strings.Join(q.Excluded, "\x00"))
	b.WriteByte('|')
	b.WriteString(strings.Join(q.Phrases, "\x00"))
	b.WriteByte('|')
	b.WriteString(q.Site)
	b.WriteByte('|')
	ex := append([]string(nil), q.Exclude...)
	// 小的选择排序：标签数量个位数，不值得引 sort 包
	for i := 0; i < len(ex); i++ {
		for j := i + 1; j < len(ex); j++ {
			if ex[j] < ex[i] {
				ex[i], ex[j] = ex[j], ex[i]
			}
		}
	}
	b.WriteString(strings.Join(ex, ","))
	// 站点分类同理：不写进键里，「只看设计素材」和「不限分类」会共用一份缓存
	// （同一次搜索先不带分类、再点分类筛选，第二下就会拿到不带分类的结果）。
	b.WriteByte('|')
	cs := append([]string(nil), q.Cats...)
	for i := 0; i < len(cs); i++ {
		for j := i + 1; j < len(cs); j++ {
			if cs[j] < cs[i] {
				cs[i], cs[j] = cs[j], cs[i]
			}
		}
	}
	b.WriteString(strings.Join(cs, ","))
	return b.String()
}

func (c *searchCache) get(gen int64, key string) (searchEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		// 索引变了：整表作废。
		// 不逐个判断条目代数——旧代条目会长期占位，把有效缓存挤出去。
		c.m = make(map[string]searchEntry, searchCacheMax)
		c.gen = gen
		c.misses.Add(1)
		return searchEntry{}, false
	}
	e, ok := c.m[key]
	if !ok {
		c.misses.Add(1)
		return searchEntry{}, false
	}
	if time.Since(e.at) > searchCacheTTL {
		delete(c.m, key)
		c.misses.Add(1)
		return searchEntry{}, false
	}
	c.hits.Add(1)
	return e, true
}

// put 写缓存。
//
// 两级门槛：
//  1. 调用方先用 cacheableQuery 过滤掉明显机器生成的长尾查询；
//  2. 第一次见到的查询只登记不缓存，第二次才占位。
//
// 为什么要第二级：表只有 512 条，扫描器拿 512 个随机串就能把它填满，之后每个
// 真实查询都 miss——攻击代价极小，正常用户的检索却从 0.2ms 变成 26ms。
// 要求「出现过两次」之后，填满需要两倍的不重复请求，而且这些请求本身也已经被
// searchLimiter 限住。真正的长尾查询本来就不会第二次出现，不缓存毫无损失。
func (c *searchCache) put(gen int64, key string, hits []index.Hit, total int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		c.m = make(map[string]searchEntry, searchCacheMax)
		c.seen = make(map[string]time.Time, admitMax)
		c.gen = gen
	}
	if _, ok := c.seen[key]; !ok {
		c.admit(key)
		return
	}
	delete(c.seen, key)
	if len(c.m) >= searchCacheMax {
		var oldestKey string
		var oldestAt time.Time
		for k, v := range c.m {
			if oldestKey == "" || v.at.Before(oldestAt) {
				oldestKey, oldestAt = k, v.at
			}
		}
		delete(c.m, oldestKey)
		c.evictions.Add(1)
	}
	// 拷贝一份：调用方随后会按 URL 重排、改 Score，共享底层数组会污染缓存。
	cp := make([]index.Hit, len(hits))
	copy(cp, hits)
	c.m[key] = searchEntry{hits: cp, total: total, at: time.Now()}
}

// admit 登记一次「见过」，并按需清理观察名单。调用方已持锁。
func (c *searchCache) admit(key string) {
	now := time.Now()
	if len(c.seen) >= admitMax {
		// 先清掉过期的；还满就整表丢弃。观察名单只是防抖，
		// 丢一次的成本是「少数查询多算一遍」，不值得为它维护淘汰顺序。
		for k, at := range c.seen {
			if now.Sub(at) > searchCacheTTL {
				delete(c.seen, k)
			}
		}
		if len(c.seen) >= admitMax {
			c.seen = make(map[string]time.Time, admitMax)
		}
	}
	c.seen[key] = now
}

// cachedSearch 带缓存的检索。命中时返回副本，调用方可随意改。
func (s *Server) cachedSearch(q index.Query) ([]index.Hit, int, error) {
	if s.scache == nil {
		return s.idx.Search(q)
	}
	gen := s.idx.Version()
	key := s.scache.key(gen, q)
	if e, ok := s.scache.get(gen, key); ok {
		out := make([]index.Hit, len(e.hits))
		copy(out, e.hits)
		return out, e.total, nil
	}
	hits, total, err := s.idx.Search(q)
	if err != nil {
		return nil, 0, err
	}
	if cacheableQuery(q) {
		s.scache.put(gen, key, hits, total)
	}
	return hits, total, nil
}

// CacheStats 命中率快照（/api/status 暴露，便于判断缓存是否真的有用）。
func (c *searchCache) Stats() map[string]int64 {
	if c == nil {
		return nil
	}
	return map[string]int64{
		"hits":      c.hits.Load(),
		"misses":    c.misses.Load(),
		"evictions": c.evictions.Load(),
		"entries":   int64(len(c.m)),
	}
}
