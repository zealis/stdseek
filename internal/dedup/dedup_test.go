package dedup

import (
	"testing"

	"voidxo/internal/store"
)

// 分带不能漏掉真实近邻：距离 ≤3 时必然有一段相同，否则粗筛会漏判。
func TestBandsCatchNearNeighbors(t *testing.T) {
	base := uint64(0x0123456789abcdef)
	// 改动 3 个比特，分布在不同段里
	variants := []uint64{
		base ^ 1,
		base ^ (1 << 20),
		base ^ (1 << 40),
	}
	for i, v := range variants {
		if Distance(base, v) > MaxDistance {
			t.Fatalf("variant %d 距离超限", i)
		}
		shared := 0
		for k := 0; k < bandCount; k++ {
			if Bands(v)[k] == Bands(base)[k] {
				shared++
			}
		}
		if shared == 0 {
			t.Fatalf("variant %d 与基准没有任何相同分带，粗筛会漏掉它", i)
		}
	}
}

// 距离足够远时不应被判定为重复（同主题不同文章）。
func TestDistanceThreshold(t *testing.T) {
	a := uint64(0xffff0000ffff0000)
	b := a ^ 0x000000000000ffff // 距离 16
	if Distance(a, b) <= MaxDistance {
		t.Fatal("距离 16 不应算重复")
	}
}

// 正本选择：最早发布优先；都没有发布时间时正文更长者优先。
// 这个函数决定「哪一篇留在索引里」，选错会让镜像页压在原文上面。
func TestPickCanonical(t *testing.T) {
	early := int64(1000)
	late := int64(2000)
	cases := []struct {
		name string
		a, b store.Fingerprint
		want string // 期望胜出的 URL
	}{
		{
			name: "最早发布的胜出，即使正文更短",
			a:    store.Fingerprint{URL: "https://a.example/early", WordCount: 100, PublishedAt: &early},
			b:    store.Fingerprint{URL: "https://b.example/late", WordCount: 900, PublishedAt: &late},
			want: "https://a.example/early",
		},
		{
			name: "都没有发布时间时正文更长的胜出",
			a:    store.Fingerprint{URL: "https://a.example/short", WordCount: 80},
			b:    store.Fingerprint{URL: "https://b.example/long", WordCount: 400},
			want: "https://b.example/long",
		},
		{
			name: "有发布时间的一律优先于没有的",
			a:    store.Fingerprint{URL: "https://a.example/nodate", WordCount: 5000},
			b:    store.Fingerprint{URL: "https://b.example/hasdate", WordCount: 100, PublishedAt: &late},
			want: "https://b.example/hasdate",
		},
	}
	for _, c := range cases {
		if got := better(c.a, c.b); got != (c.a.URL == c.want) {
			t.Errorf("%s: better(a,b)=%v，期望胜出者为 %s", c.name, got, c.want)
		}
	}
}

// TestCrossSiteSkipsUnfilledShingle 零值哨兵必须在比较入口挡掉。
//
// 真实事故：reindex 回填 shingle_hash 到一半时跑 dedupe，未回填的文档
// ShingleHash=0，而 0 与 0 的汉明距离恰好是 0 —— vuejs/react/rust/php/redis
// 共 272 篇被整体折叠到一篇 PostgreSQL 7.1 文档上。
//
// 只在分带构建时跳过 0 是不够的：这些文档仍会通过 Simhash 桶相遇，
// 那时跨站判定读的就是 0 与 0。
func TestCrossSiteSkipsUnfilledShingle(t *testing.T) {
	// 同域不同页：应照常用词袋判定（Simhash 完全相同 → 同站重复）
	same := []store.Fingerprint{
		{URL: "https://a/1", Domain: "a", Simhash: 0xABCD, ShingleHash: 0},
		{URL: "https://a/2", Domain: "a", Simhash: 0xABCD, ShingleHash: 0},
	}
	if distance(same[0], same[1]) > MaxDistance {
		t.Fatal("同站重复不受 shingle 未回填影响")
	}

	// 跨站、两边都没回填：绝不能判重复
	cross := []store.Fingerprint{
		{URL: "https://a/1", Domain: "a", Simhash: 0xABCD, ShingleHash: 0},
		{URL: "https://b/2", Domain: "b", Simhash: 0x1234, ShingleHash: 0},
	}
	if got := distance(cross[0], cross[1]); got <= CrossSiteDistance {
		t.Fatalf("未回填 shingle 的跨站文档被判为重复（距离 %d）", got)
	}

	// 一边回填一边没回填：也不判
	mixed := []store.Fingerprint{
		{URL: "https://a/1", Domain: "a", Simhash: 0xABCD, ShingleHash: 0x1111},
		{URL: "https://b/2", Domain: "b", Simhash: 0x1234, ShingleHash: 0},
	}
	if got := distance(mixed[0], mixed[1]); got <= CrossSiteDistance {
		t.Fatalf("单边未回填也不应判重复（距离 %d）", got)
	}

	// 两边都回填且确实相同：应判重复（证明上面不是一律放行）
	real := []store.Fingerprint{
		{URL: "https://a/1", Domain: "a", ShingleHash: 0xDEADBEEF},
		{URL: "https://b/2", Domain: "b", ShingleHash: 0xDEADBEEF},
	}
	if got := distance(real[0], real[1]); got > CrossSiteDistance {
		t.Fatalf("已回填且相同的跨站文档应判重复，实际距离 %d", got)
	}
}

// TestBandsNCoversThreshold 分带段数必须大于阈值（鸽笼原理）：
// 阈值 6 需要至少 7 段，取 8；否则「距离 ≤6 的一对」可能每段都被改动，粗筛漏掉它。
func TestBandsNCoversThreshold(t *testing.T) {
	if bandCountCross <= CrossSiteDistance {
		t.Fatalf("跨站分带段数 %d 必须大于阈值 %d", bandCountCross, CrossSiteDistance)
	}
	if bandCountSame <= MaxDistance {
		t.Fatalf("同站分带段数 %d 必须大于阈值 %d", bandCountSame, MaxDistance)
	}
	base := uint64(0x0123456789ABCDEF)
	// 翻转 CrossSiteDistance 个随机位，至少要有一段完全相同
	for shift := 0; shift < 64; shift += 7 {
		v := base
		for k := 0; k < CrossSiteDistance; k++ {
			v ^= 1 << uint((shift+k*5)%64)
		}
		if Distance(v, base) != CrossSiteDistance {
			continue // 有比特撞在一起，实际距离更小，也满足要求
		}
		ba, bb := BandsN(base, bandCountCross), BandsN(v, bandCountCross)
		hit := false
		for i := range ba {
			if ba[i] == bb[i] {
				hit = true
			}
		}
		if !hit {
			t.Fatalf("距离 %d 的文档没有任何一段分带相同（base=%x v=%x）",
				Distance(v, base), base, v)
		}
	}
}
