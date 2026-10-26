// 证据片段缓存。
//
// 为什么值得缓存：片段生成是当前 HTTP 链路的第二大头（10 篇约 16~35ms，
// 每篇要从 KV 读快照再剥标签）。热门查询反复打开时，同样的 (url, query)
// 会重复算一遍——而这两件事都不常变。
//
// 为什么必须绑定 kv.Version()：快照会被重新抓取覆盖。只按 TTL 失效的话，
// 用户会在"页面已更新"后的窗口内继续看到旧正文的片段——那是拿过期内容
// 当证据，比慢几毫秒严重得多。与检索缓存（searchcache.go）同一套机制。
package api

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"voidxo/internal/kv"
)

const (
	// snippetCacheMax 条目上限。一条 = 一个 (url, query) 的片段组。
	// 512 条约几 MB 量级（每条最多 2 段 ×240 字）。
	snippetCacheMax = 512
	// snippetCacheTTL 兜底过期。快照版本只能感知「快照被改写」，
	// 感知不到查询词以外的因素——其实没有别的因素了，留 TTL 是为了
	// 与检索缓存保持一致的心智，以及极端情况下的人工排障空间。
	snippetCacheTTL = 10 * time.Minute
)

type snippetEntry struct {
	snips []string
	at    time.Time
}

type snippetCache struct {
	mu  sync.Mutex
	m   map[string]snippetEntry
	gen int64 // 当前表对应的 KV 代数

	hits   atomic.Int64
	misses atomic.Int64
}

func newSnippetCache() *snippetCache {
	return &snippetCache{m: make(map[string]snippetEntry, snippetCacheMax)}
}

// key 归一化查询词：buildSnippets 内部会 ToLower，所以大小写不同的查询
// 应共用同一份缓存（"Go" 与 "go" 的片段完全相同）。
func (c *snippetCache) key(urlKey, q string) string {
	return urlKey + "\x00" + strings.ToLower(strings.TrimSpace(q))
}

func (c *snippetCache) get(gen int64, key string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		// 快照变了：整表作废。不逐个判断条目代数——旧代条目会长期占位，
		// 把有效缓存挤出去（与 searchCache 同样的取舍）。
		c.m = make(map[string]snippetEntry, snippetCacheMax)
		c.gen = gen
		c.misses.Add(1)
		return nil, false
	}
	e, ok := c.m[key]
	if !ok {
		c.misses.Add(1)
		return nil, false
	}
	if time.Since(e.at) > snippetCacheTTL {
		delete(c.m, key)
		c.misses.Add(1)
		return nil, false
	}
	c.hits.Add(1)
	// 拷贝：调用方随后可能排序或改写片段列表
	out := make([]string, len(e.snips))
	copy(out, e.snips)
	return out, true
}

func (c *snippetCache) put(gen int64, key string, snips []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		c.m = make(map[string]snippetEntry, snippetCacheMax)
		c.gen = gen
	}
	if len(c.m) >= snippetCacheMax {
		// 满了淘汰最早的一条：缓存的价值是"常见页面别重复算"，
		// 不为了 LRU 的精确性引入链表。
		var oldestKey string
		var oldestAt time.Time
		for k, v := range c.m {
			if oldestKey == "" || v.at.Before(oldestAt) {
				oldestKey, oldestAt = k, v.at
			}
		}
		delete(c.m, oldestKey)
	}
	cp := make([]string, len(snips))
	copy(cp, snips)
	c.m[key] = snippetEntry{snips: cp, at: time.Now()}
}

// Stats 命中率快照（/api/status 暴露）。
func (c *snippetCache) Stats() map[string]int64 {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	n := len(c.m)
	c.mu.Unlock()
	return map[string]int64{
		"hits":    c.hits.Load(),
		"misses":  c.misses.Load(),
		"entries": int64(n),
	}
}

// cachedSnippets 带缓存的片段生成。命中返回副本，调用方可随意改。
func (s *Server) cachedSnippets(snap *kv.Store, urlKey, q, body string) []string {
	if s.snippet == nil || snap == nil {
		return buildSnippets(body, q, snippetMaxPerDoc)
	}
	gen := snap.Version()
	key := s.snippet.key(urlKey, q)
	if got, ok := s.snippet.get(gen, key); ok {
		return got
	}
	snips := buildSnippets(body, q, snippetMaxPerDoc)
	// 空结果也缓存：命中不到查询词的页面反复出现时，
	// 不该每次都白跑一遍剥标签+扫描才知道"没有"。
	s.snippet.put(gen, key, snips)
	return snips
}

// lookup 只查缓存，不生成。命中返回 (片段, true)。
//
// 片段为 nil 表示「这篇没有快照」——调用方据此跳过 KV 读。
// 这个前置是为了省掉读存储的成本：命中时片段是现成的，
// 没必要为了算一个 key 去读 80KB 的快照（实测 KV 读 2.6~3.7ms/10 篇）。
func (c *snippetCache) lookup(gen int64, key string) ([]string, bool) {
	if c == nil {
		return nil, false
	}
	return c.get(gen, key)
}

// rememberNoSnapshot 缓存「这篇没有快照」这个事实。
// 否则每次搜索都要读一次 KV 才知道它不存在——没有快照的老文档
// 会一直付这个代价。
func (c *snippetCache) rememberNoSnapshot(gen int64, key string) {
	if c == nil {
		return
	}
	c.put(gen, key, nil)
}
