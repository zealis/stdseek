package store

import (
	"context"
	"path/filepath"
	"testing"
)

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
