package rank

import (
	"strings"
	"testing"
	"time"

	"voidxo/internal/index"
)

func fixedNow() time.Time { return time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC) }

func daysAgo(d int) int64 { return fixedNow().Add(-time.Duration(d) * 24 * time.Hour).Unix() }

func reasonContains(rs []string, sub string) bool {
	for _, r := range rs {
		if strings.Contains(r, sub) {
			return true
		}
	}
	return false
}

func TestExactMatchBeatsHigherBM25(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://a.example/1", Domain: "a.example", Title: "容器编排实践", Score: 3.0,
			SortTime: daysAgo(3)},
		{URL: "https://b.example/2", Domain: "b.example", Title: "Kubernetes CrashLoopBackOff 排查", Score: 2.0,
			SortTime: daysAgo(3)},
	}
	out := r.Rank("crashloopbackoff", hits, map[string]string{}, fixedNow())
	if out[0].Hit.URL != "https://b.example/2" {
		t.Fatalf("exact title match should rank first, got %s", out[0].Hit.URL)
	}
	if !reasonContains(out[0].Reasons, "标题精确匹配") {
		t.Fatalf("missing exact-match reason: %v", out[0].Reasons)
	}
}

func TestFreshnessOrdering(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://old.example/1", Domain: "old.example", Title: "go 调度器", Score: 2.0,
			SortTime: daysAgo(365)},
		{URL: "https://new.example/1", Domain: "new.example", Title: "go 调度器", Score: 2.0,
			SortTime: daysAgo(1)},
	}
	out := r.Rank("go 调度器", hits, map[string]string{}, fixedNow())
	if out[0].Hit.URL != "https://new.example/1" {
		t.Fatalf("newer doc should rank first: %s", out[0].Hit.URL)
	}
	if !reasonContains(out[0].Reasons, "发布时间较新") {
		t.Fatalf("missing freshness reason: %v", out[0].Reasons)
	}
}

func TestAISuspectDoesNotPenalize(t *testing.T) {
	r := New(DefaultWeights())
	base := index.Hit{URL: "https://x.example/1", Domain: "x.example", Title: "缓存设计",
		Score: 2.0, SortTime: daysAgo(5)}
	tagged := base
	tagged.URL = "https://y.example/2"
	tagged.Domain = "y.example"
	tagged.Labels = []string{"ai_suspect"}

	out := r.Rank("缓存设计", []index.Hit{base, tagged}, map[string]string{}, fixedNow())
	if len(out) != 2 {
		t.Fatalf("want 2 results, got %d", len(out))
	}
	// 两个文档除标签外完全一致：分数必须相同（ai_suspect 不降权）
	if out[0].Score != out[1].Score {
		t.Fatalf("ai_suspect must not affect score: %v vs %v", out[0].Score, out[1].Score)
	}
	var taggedRes Result
	for _, res := range out {
		if res.Hit.URL == "https://y.example/2" {
			taggedRes = res
		}
	}
	if !reasonContains(taggedRes.Reasons, "不参与降权") {
		t.Fatalf("ai_suspect must be explained as filter-only: %v", taggedRes.Reasons)
	}
}

func TestDomainDiversityDecay(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://same.example/1", Domain: "same.example", Title: "go 并发 a", Score: 3.0, SortTime: daysAgo(2)},
		{URL: "https://same.example/2", Domain: "same.example", Title: "go 并发 b", Score: 2.9, SortTime: daysAgo(2)},
		{URL: "https://other.example/1", Domain: "other.example", Title: "go 并发 c", Score: 1.5, SortTime: daysAgo(2)},
	}
	out := r.Rank("go 并发", hits, map[string]string{}, fixedNow())
	if out[1].Hit.Domain != "other.example" {
		t.Fatalf("second same-domain result should be pushed down, got %s", out[1].Hit.Domain)
	}
	if !reasonContains(out[2].Reasons, "多样性下调") {
		t.Fatalf("missing diversity reason on third result: %v", out[2].Reasons)
	}
}

func TestTimeByFetchIsExplained(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://n.example/1", Domain: "n.example", Title: "无发布时间", Score: 1.0,
			SortTime: daysAgo(2), TimeByFetch: true},
	}
	out := r.Rank("无发布时间", hits, map[string]string{}, fixedNow())
	if !reasonContains(out[0].Reasons, "按收录时间") {
		t.Fatalf("time_by_fetch must be disclosed: %v", out[0].Reasons)
	}
}

func TestQualityByCategory(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://blog.example/1", Domain: "blog.example", Title: "go 教程", Score: 1.0, SortTime: daysAgo(1)},
		{URL: "https://go.dev/1", Domain: "go.dev", Title: "go 教程", Score: 1.0, SortTime: daysAgo(1)},
	}
	out := r.Rank("go 教程", hits, map[string]string{"go.dev": "official", "blog.example": "blog"}, fixedNow())
	if out[0].Hit.Domain != "go.dev" {
		t.Fatalf("official category should outrank blog: %s", out[0].Hit.Domain)
	}
	if !reasonContains(out[0].Reasons, "官方文档") {
		t.Fatalf("missing official-doc reason: %v", out[0].Reasons)
	}
}
