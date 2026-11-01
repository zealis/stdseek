package prosperity

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zealis/stdseek/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestURLKeyAndPrefixes(t *testing.T) {
	if got := Key("https://Go.dev/doc/?x=1#frag"); got != "go.dev/doc" {
		t.Fatalf("key=%q", got)
	}
	keys := Keys("https://go.dev/doc/x")
	want := []string{"go.dev", "go.dev/doc", "go.dev/doc/x"}
	if len(keys) != len(want) {
		t.Fatalf("keys=%v", keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys=%v want %v", keys, want)
		}
	}
	if k := Key("/relative/path"); k != "" {
		t.Fatalf("relative url should be empty, got %q", k)
	}
}

func TestComputeCountsDistinctDomainsAndPropagates(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// 三个不同站点都链到 go.dev/doc/x；其中 a.com 重复链两次，只应算一个域名
	links := []struct {
		from, domain string
		targets      []string
	}{
		{"https://a.com/1", "a.com", []string{"https://go.dev/doc/x", "https://go.dev/doc/x"}},
		{"https://b.com/1", "b.com", []string{"https://go.dev/doc/x"}},
		{"https://c.com/1", "c.com", []string{"https://go.dev/doc/x"}},
		{"https://a.com/2", "a.com", []string{"https://rare.example/only"}},
	}
	for _, l := range links {
		if err := st.ReplaceOutlinks(ctx, l.from, l.domain, l.targets); err != nil {
			t.Fatal(err)
		}
	}

	rows, stat, err := Compute(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	// a.com 重复链同一目标被 UNIQUE(from_url,to_url) 去重，因此是 4 条
	if stat.Pairs != 4 || stat.MaxRef != 3 {
		t.Fatalf("stat=%+v", stat)
	}

	byKey := map[string]store.ProsperityRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	// 前缀传播：域名、目录、页面拿到同样的引用域名数
	for _, k := range []string{"go.dev", "go.dev/doc", "go.dev/doc/x"} {
		if byKey[k].RefDomains != 3 {
			t.Fatalf("%s ref_domains=%d want 3 (rows=%+v)", k, byKey[k].RefDomains, byKey)
		}
	}
	if byKey["go.dev"].Score != Base {
		t.Fatalf("max key should score Base: %v", byKey["go.dev"].Score)
	}
	if !(byKey["rare.example"].Score > 0 && byKey["rare.example"].Score < Base) {
		t.Fatalf("rare.example score=%v", byKey["rare.example"].Score)
	}
}

func TestRefreshPersistsScores(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	if err := st.ReplaceOutlinks(ctx, "https://a.com/1", "a.com", []string{"https://go.dev/doc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Refresh(ctx, st); err != nil {
		t.Fatal(err)
	}
	scores, err := st.ProsperityScores(ctx, []string{"go.dev", "go.dev/doc", "unknown.example"})
	if err != nil {
		t.Fatal(err)
	}
	if scores["go.dev"] != Base {
		t.Fatalf("go.dev score=%v", scores["go.dev"])
	}
	if _, ok := scores["unknown.example"]; ok {
		t.Fatalf("unknown key should be absent: %v", scores)
	}
}

func TestEmptyCorpus(t *testing.T) {
	rows, stat, err := Compute(context.Background(), newStore(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || stat.Pairs != 0 {
		t.Fatalf("rows=%v stat=%+v", rows, stat)
	}
}
