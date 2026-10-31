package api

import (
	"time"
)

// 原文快照的读取缓存与频率限制。
//
// 为什么必须缓存：后台可以把快照设成「只留云端」（storage.backup.prune_local），
// 这时 /api/page 每次都要跨洋去对象存储回源——实测到 B2 美西 RTT 约 0.5 秒。
// 用户把结果点开又关掉、或者来回翻同一页，都会被重复计费，交互也拖到一秒以上。
//
// 快照是不可变的（存的是抓取那一刻的页面），所以缓存没有一致性风险；
// 代价只是「刚补抓的页面 10 分钟内仍看到旧的」——补抓本来就改变了这一页的内容，
// 那种情况下用户重开一次搜索即可。
const (
	pageCacheTTL      = 10 * time.Minute
	pageCacheMaxItems = 64
	// pageCacheMaxBytes 单页超过这个大小就不缓存：正常文章 20~200KB，
	// 512KB 以上的基本是异常页（整站导出、base64 图片），缓存它不划算。
	pageCacheMaxBytes = 512 << 10
	// pageLimitPerMinute 单个 IP 每分钟最多读几次快照。
	// 正常用户一分钟点不到 30 次；再高基本是脚本在刷。
	pageLimitPerMinute = 30
)

type pageCacheEntry struct {
	body []byte
	at   time.Time
}

// pageCacheGet 取缓存；过期即删。
func (s *Server) pageCacheGet(key string) ([]byte, bool) {
	s.pageMu.Lock()
	defer s.pageMu.Unlock()
	e, ok := s.pageCache[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) > pageCacheTTL {
		delete(s.pageCache, key)
		return nil, false
	}
	return e.body, true
}

// pageCachePut 写缓存；超出条目上限时先清过期项，再淘汰最旧的一条。
func (s *Server) pageCachePut(key string, body []byte) {
	if len(body) > pageCacheMaxBytes {
		return
	}
	s.pageMu.Lock()
	defer s.pageMu.Unlock()
	if s.pageCache == nil {
		s.pageCache = make(map[string]pageCacheEntry, pageCacheMaxItems)
	}
	if len(s.pageCache) >= pageCacheMaxItems {
		now := time.Now()
		for k, v := range s.pageCache {
			if now.Sub(v.at) > pageCacheTTL {
				delete(s.pageCache, k)
			}
		}
		// 还是满的：淘汰最旧的一条（快照大小接近，按时间淘汰足够）。
		for len(s.pageCache) >= pageCacheMaxItems {
			var oldestKey string
			var oldestAt time.Time
			for k, v := range s.pageCache {
				if oldestKey == "" || v.at.Before(oldestAt) {
					oldestKey, oldestAt = k, v.at
				}
			}
			delete(s.pageCache, oldestKey)
		}
	}
	s.pageCache[key] = pageCacheEntry{body: body, at: time.Now()}
}
