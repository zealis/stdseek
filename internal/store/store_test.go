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

// 建表回填：把「上次抓取失败」的站点直接放进名单，且只做一次
// （否则管理员清空名单后，一重启旧账又回来了）。
func TestBackfillCrawlIssuesOnce(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "backfill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	for _, d := range []string{"down.example", "empty.example", "fine.example"} {
		if err := st.AddWhitelist(ctx, d, "blog", 3600); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.TouchCrawlResult(ctx, "down.example", "error", "Get https://down.example/: EOF"); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchCrawlResult(ctx, "empty.example", "no-document", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchCrawlResult(ctx, "fine.example", "ok", ""); err != nil {
		t.Fatal(err)
	}

	n, err := st.BackfillCrawlIssuesOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 backfilled (ok 站点不该进名单), got %d", n)
	}
	issues, total, err := st.ListCrawlIssues(ctx, "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("want 2 issues, got %d", total)
	}
	kinds := map[string]string{}
	for _, it := range issues {
		kinds[it.Domain] = it.Kind
	}
	if kinds["down.example"] != CrawlIssueUnreachable {
		t.Fatalf("error 应归为无法访问，got %q", kinds["down.example"])
	}
	if kinds["empty.example"] != CrawlIssueStructure {
		t.Fatalf("no-document 应归为结构变化，got %q", kinds["empty.example"])
	}

	// 第二次必须是空操作
	if n, err := st.BackfillCrawlIssuesOnce(ctx); err != nil || n != 0 {
		t.Fatalf("second backfill should be a no-op, got n=%d err=%v", n, err)
	}

	// 管理员清空名单后重启也不该复活（标记已落库）
	for _, d := range []string{"down.example", "empty.example"} {
		if err := st.ClearCrawlIssue(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := st.BackfillCrawlIssuesOnce(ctx); err != nil || n != 0 {
		t.Fatalf("cleared issues must not come back, got n=%d err=%v", n, err)
	}
	if _, total, _ := st.ListCrawlIssues(ctx, "", "", 10, 0); total != 0 {
		t.Fatalf("want empty list, got %d", total)
	}
}

// 抓取间隔按分类差异化 + 重复提交更新分类与间隔（不抹掉已有配置）。
func TestAddWhitelistIntervalByCategory(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "interval.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	load := func(domain string) WhitelistSite {
		sites, _, err := st.ListWhitelist(ctx, domain, 1, 0)
		if err != nil || len(sites) != 1 {
			t.Fatalf("load %s: %v %d", domain, err, len(sites))
		}
		return sites[0]
	}

	// 不填间隔 → 按分类默认：新闻 30 分钟、官方站 24 小时
	if err := st.AddWhitelist(ctx, "news.example.com", "media", 0); err != nil {
		t.Fatal(err)
	}
	if got := load("news.example.com").CrawlInterval; got != 1800 {
		t.Fatalf("新闻站默认间隔应为 1800 秒，got %d", got)
	}
	if err := st.AddWhitelist(ctx, "docs2.example.com", "official", 0); err != nil {
		t.Fatal(err)
	}
	if got := load("docs2.example.com").CrawlInterval; got != 86400 {
		t.Fatalf("官方站默认间隔应为 86400 秒，got %d", got)
	}

	// 自定义间隔优先于分类默认值
	if err := st.AddWhitelist(ctx, "slow.example.com", "blog", 600); err != nil {
		t.Fatal(err)
	}
	if got := load("slow.example.com").CrawlInterval; got != 600 {
		t.Fatalf("自定义间隔应保留，got %d", got)
	}

	// 重复提交：分类与间隔被更新，来源回到 seed（不再算 AI 判的）
	if err := st.AddWhitelistWithPath(ctx, "news.example.com", "blog", 7200, "/pick/"); err != nil {
		t.Fatal(err)
	}
	w := load("news.example.com")
	if w.Category != "blog" || w.CrawlInterval != 7200 {
		t.Fatalf("重复提交应更新分类与间隔，got %+v", w)
	}
	if w.CategorySource != CategorySourceSeed {
		t.Fatalf("分类来源应回到 seed，got %q", w.CategorySource)
	}
	if w.SeedPath != "/pick/" {
		t.Fatalf("入口路径应写入，got %q", w.SeedPath)
	}

	// 再提交一次但不带路径：不能把已配好的入口抹掉
	if err := st.AddWhitelist(ctx, "news.example.com", "blog", 7200); err != nil {
		t.Fatal(err)
	}
	if got := load("news.example.com").SeedPath; got != "/pick/" {
		t.Fatalf("重复提交不带路径时不应清空入口，got %q", got)
	}
	// 站点名/描述是爬虫抓出来的，提交动作不该清掉
	if err := st.UpdateSiteName(ctx, "news.example.com", "示例新闻"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddWhitelist(ctx, "news.example.com", "blog", 7200); err != nil {
		t.Fatal(err)
	}
	names, err := st.SiteNames(ctx, []string{"news.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if names["news.example.com"] != "示例新闻" {
		t.Fatalf("站点名不该被提交动作清空，got %q", names["news.example.com"])
	}
}

// 审核通过也走同一条入库语义：提交时选的分类要落到白名单，
// 抓取间隔按分类给（新闻站 30 分钟），不是写死的一天。
func TestApproveReviewUsesCategoryInterval(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "approve.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, _, err := st.CreateSubmission(ctx, ReviewItem{
		URL: "https://daily.example.com/", Domain: "daily.example.com", Category: "media", Reason: "新闻站",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApproveReview(ctx, id); err != nil {
		t.Fatal(err)
	}
	sites, _, err := st.ListWhitelist(ctx, "daily.example.com", 1, 0)
	if err != nil || len(sites) != 1 {
		t.Fatalf("approve 应写入白名单：%v %d", err, len(sites))
	}
	if sites[0].Category != "media" {
		t.Fatalf("分类应来自提交，got %q", sites[0].Category)
	}
	if sites[0].CrawlInterval != 1800 {
		t.Fatalf("抓取间隔应按分类给 1800 秒，got %d", sites[0].CrawlInterval)
	}
}

// 蒸馏结果按站点记：失败累加并记原因，成功清零；子域要能对上父域白名单。
func TestRecordDistillBySite(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "distill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.AddWhitelist(ctx, "example.com", "blog", 3600); err != nil {
		t.Fatal(err)
	}
	read := func() WhitelistSite {
		t.Helper()
		items, _, err := st.ListWhitelist(ctx, "example.com", 10, 0)
		if err != nil || len(items) != 1 {
			t.Fatalf("list: %v %v", err, items)
		}
		return items[0]
	}

	// 抓的是子域 blog.example.com，白名单里登记的是父域
	if err := st.RecordDistill(ctx, nil, map[string]string{"blog.example.com": "拿回来的是拦截页（站点拦截页）"}); err != nil {
		t.Fatal(err)
	}
	if w := read(); w.DistillFails != 1 || w.DistillLastErr == "" || w.DistillCheckedAt == 0 {
		t.Fatalf("失败没记上：%+v", w)
	}

	// 再失败一次要累加（连续失败次数，不是总次数）
	if err := st.RecordDistill(ctx, nil, map[string]string{"example.com": "阅读器返回 429"}); err != nil {
		t.Fatal(err)
	}
	if w := read(); w.DistillFails != 2 {
		t.Fatalf("应该累加到 2，实际 %d", w.DistillFails)
	}

	// 成功一次就清零，并把原因清空
	if err := st.RecordDistill(ctx, []string{"example.com"}, nil); err != nil {
		t.Fatal(err)
	}
	if w := read(); w.DistillFails != 0 || w.DistillLastErr != "" {
		t.Fatalf("成功后没清零：%+v", w)
	}
}
