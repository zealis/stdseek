package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestDueSitesRespectsIntervalAndEnabled(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "due.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.AddWhitelist(ctx, "fresh.example.com", "blog", 3600); err != nil {
		t.Fatal(err)
	}
	if err := st.AddWhitelist(ctx, "crawled.example.com", "blog", 3600); err != nil {
		t.Fatal(err)
	}
	if err := st.AddWhitelist(ctx, "disabled.example.com", "blog", 3600); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchCrawlResult(ctx, "crawled.example.com", "ok", ""); err != nil {
		t.Fatal(err)
	}
	items, _, err := st.ListWhitelist(ctx, "disabled.example.com", 10, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %v %v", err, items)
	}
	if err := st.SetWhitelistEnabled(ctx, items[0].ID, false); err != nil {
		t.Fatal(err)
	}

	now := time.Now().Unix()
	due, err := st.DueSites(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Domain != "fresh.example.com" {
		t.Fatalf("due=%+v, only never-crawled enabled site should be due", due)
	}

	// 时间推进超过 interval 后，两个启用站点都到期
	later, err := st.DueSites(ctx, now+3601)
	if err != nil {
		t.Fatal(err)
	}
	if len(later) != 2 {
		t.Fatalf("later due=%d want 2", len(later))
	}
	for _, d := range later {
		if d.Domain == "disabled.example.com" {
			t.Fatal("disabled site must never be due")
		}
	}
}

func TestSiteCategoryParentMatch(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.AddWhitelist(ctx, "go.dev", "official", 3600); err != nil {
		t.Fatal(err)
	}
	cat, err := st.SiteCategory(ctx, "go.dev")
	if err != nil {
		t.Fatalf("SiteCategory err: %v", err)
	}
	if cat != "official" {
		t.Fatalf("cat=%q", cat)
	}
	sub, err := st.SiteCategory(ctx, "pkg.go.dev")
	if err != nil {
		t.Fatalf("sub err: %v", err)
	}
	if sub != "official" {
		t.Fatalf("sub cat=%q", sub)
	}
	cats, err := st.DomainCategories(ctx, []string{"go.dev", "pkg.go.dev", "unknown.example"})
	if err != nil {
		t.Fatalf("batch err: %v", err)
	}
	if cats["go.dev"] != "official" || cats["pkg.go.dev"] != "official" {
		t.Fatalf("batch cats=%v", cats)
	}
	if _, ok := cats["unknown.example"]; ok {
		t.Fatalf("unknown domain should be absent: %v", cats)
	}
}

func TestReviewPrecheckPersistence(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// 1) 提交时带预检：JSON 原样保留，并记录检查时间
	id, _, err := st.CreateSubmission(ctx, ReviewItem{
		URL: "https://example.com/post", Domain: "example.com", Category: "blog",
		Precheck: `{"reachable":true,"status_code":200,"is_html":true,"robots_allowed":true}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := st.GetReview(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.PrecheckAt == nil {
		t.Fatal("precheck_at should be set")
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(item.Precheck), &m); err != nil {
		t.Fatal(err)
	}
	if m["reachable"] != true || m["status_code"].(float64) != 200 {
		t.Fatalf("precheck payload lost: %v", m)
	}
	if _, ok := m["blacklist_history"]; ok {
		t.Fatalf("blacklist_history should be absent for clean domain: %v", m)
	}

	// 2) 命中黑名单：补标记而不是覆盖已有预检
	if err := st.AddBlacklist(ctx, "bad.example.com", "spam"); err != nil {
		t.Fatal(err)
	}
	id2, _, err := st.CreateSubmission(ctx, ReviewItem{
		URL: "https://bad.example.com/", Domain: "bad.example.com",
		Precheck: `{"reachable":false,"status_code":500}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	item2, err := st.GetReview(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	m = map[string]interface{}{}
	if err := json.Unmarshal([]byte(item2.Precheck), &m); err != nil {
		t.Fatal(err)
	}
	if m["blacklist_history"] != true {
		t.Fatalf("blacklist_history missing: %v", m)
	}
	if m["status_code"].(float64) != 500 {
		t.Fatalf("existing precheck fields must survive merge: %v", m)
	}

	// 3) 重新预检覆盖旧结果
	if err := st.SetReviewPrecheck(ctx, id, `{"reachable":true,"status_code":204}`); err != nil {
		t.Fatal(err)
	}
	item3, err := st.GetReview(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	m = map[string]interface{}{}
	if err := json.Unmarshal([]byte(item3.Precheck), &m); err != nil {
		t.Fatal(err)
	}
	if m["status_code"].(float64) != 204 {
		t.Fatalf("precheck not refreshed: %v", m)
	}

	// 4) 不存在的条目 → ErrNoRows（API 层据此返回 404）
	if err := st.SetReviewPrecheck(ctx, 9999, "{}"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("want ErrNoRows, got %v", err)
	}
}

func TestDueSitesBackoffAfterFailure(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "backoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const domain = "flaky.example.com"
	if err := st.AddWhitelist(ctx, domain, "blog", 3600); err != nil {
		t.Fatal(err)
	}

	has := func(list []WhitelistSite) bool {
		for _, s := range list {
			if s.Domain == domain {
				return true
			}
		}
		return false
	}

	// 第一次抓取没产出文档 → 失败一次，退避到 2 倍间隔
	if err := st.TouchCrawlResult(ctx, domain, "no-document", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if due, err := st.DueSites(ctx, now+3600); err != nil {
		t.Fatal(err)
	} else if has(due) {
		t.Fatalf("failed site must back off: due at 1x interval = %+v", due)
	}
	if due, err := st.DueSites(ctx, now+7201); err != nil {
		t.Fatal(err)
	} else if !has(due) {
		t.Fatal("site should be due after 2x interval")
	}

	// 失败累加：两次失败 → 4 倍
	if err := st.TouchCrawlResult(ctx, domain, "error", "connection refused"); err != nil {
		t.Fatal(err)
	}
	now = time.Now().Unix()
	if due, _ := st.DueSites(ctx, now+7201); has(due) {
		t.Fatal("2 failures should back off to 4x interval")
	}
	if due, _ := st.DueSites(ctx, now+14401); !has(due) {
		t.Fatal("should be due after 4x interval")
	}

	// 成功 → 计数清零，恢复基础间隔
	if err := st.TouchCrawlResult(ctx, domain, "ok", ""); err != nil {
		t.Fatal(err)
	}
	items, _, err := st.ListWhitelist(ctx, domain, 10, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %v %v", err, items)
	}
	if items[0].FailCount != 0 {
		t.Fatalf("success should reset fail_count, got %d", items[0].FailCount)
	}
	if due, _ := st.DueSites(ctx, time.Now().Unix()+3601); !has(due) {
		t.Fatal("after success the base interval should apply again")
	}
}

// simhash 是 64 位无符号哈希，database/sql 不接受高位为 1 的 uint64；
// 必须按位模式存成 int64，否则真实文档约一半概率写入失败（合成数据 simhash=1 曾掩盖此问题）。
func TestUpsertDocumentHighBitSimhash(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "simhash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const high = uint64(0xFFFFFFFFFFFFFFFF)
	if _, err := st.UpsertDocument(ctx, Document{
		URL: "https://a.example/1", Domain: "a.example", Title: "t", Simhash: high, FetchedAt: 1,
	}); err != nil {
		t.Fatalf("high-bit simhash should persist: %v", err)
	}
	doc, found, err := st.GetDocument(ctx, "https://a.example/1")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if doc.Simhash != high {
		t.Fatalf("simhash round-trip mismatch: got %#x want %#x", doc.Simhash, high)
	}
}

func TestDedupeDocumentsCollapsesAnchors(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "dedupe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	urls := []string{
		"https://www.infoq.cn/content-center/",
		"https://www.infoq.cn/content-center/#about",
		"https://www.infoq.cn/content-center/#intro",
		"https://example.com/a",
	}
	for _, u := range urls {
		if _, err := st.UpsertDocument(ctx, Document{URL: u, Domain: "x", Title: "t", FetchedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := st.DedupeDocuments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed=%d, want 2", removed)
	}
	total, err := st.CountDocuments(ctx)
	if err != nil || total != 2 {
		t.Fatalf("total=%d err=%v, want 2", total, err)
	}
	// 保留 id 最大的一条（最后写入的锚点变体）
	if _, found, err := st.GetDocument(ctx, "https://www.infoq.cn/content-center/#intro"); err != nil || !found {
		t.Fatalf("latest anchor row should survive: found=%v err=%v", found, err)
	}
	// 幂等：再跑一次不应删除任何行
	if removed, err := st.DedupeDocuments(ctx); err != nil || removed != 0 {
		t.Fatalf("second run removed=%d err=%v", removed, err)
	}
}

// TestInboundCountsMatchesDistinct COUNT(*) 与 COUNT(DISTINCT from_url) 必须相等。
//
// 性能改动的前提：表上有 UNIQUE(from_url, to_url)，同一 (from, to) 只有一行，
// 所以对固定 to_url 来说 from_url 不可能重复。这个测试钉住那个前提——
// 万一将来有人去掉 UNIQUE 或改成允许多行，这条会立刻失败，
// 而不是让入站数悄悄变大（深网发现的冷热信号失真）。
func TestInboundCountsMatchesDistinct(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "inb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// 三篇文章链向同一个目标，其中两篇来自同一域名
	targets := []string{"https://a.example/hot"}
	if err := st.ReplaceOutlinks(ctx, "https://a.example/1", "a.example", targets); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceOutlinks(ctx, "https://a.example/2", "a.example", targets); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceOutlinks(ctx, "https://b.example/3", "b.example", targets); err != nil {
		t.Fatal(err)
	}
	// 同一来源重复写入不应让计数翻倍（ReplaceOutlinks 语义是替换）
	if err := st.ReplaceOutlinks(ctx, "https://a.example/1", "a.example", targets); err != nil {
		t.Fatal(err)
	}

	got, err := st.InboundCounts(ctx, []string{"https://a.example/hot"})
	if err != nil {
		t.Fatal(err)
	}
	if got["https://a.example/hot"] != 3 {
		t.Fatalf("入站数应为 3（三个不同来源），实际 %d", got["https://a.example/hot"])
	}

	// 直接与 DISTINCT 口径对照：这条查询不走生产代码，用来证明两者相等
	var distinct int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT from_url) FROM outlinks WHERE to_url = ?`,
		"https://a.example/hot").Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct != got["https://a.example/hot"] {
		t.Fatalf("COUNT(*) 与 COUNT(DISTINCT) 不一致：%d vs %d",
			got["https://a.example/hot"], distinct)
	}
}
