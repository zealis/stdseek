package store

import (
	"context"
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
