package eval

import (
	"math"
	"testing"
)

func TestRecallAtK(t *testing.T) {
	rel := map[string]int{"a": 2, "b": 1}
	if got := RecallAtK([]string{"x", "a", "y", "b"}, rel, 4); math.Abs(got-1) > 1e-9 {
		t.Fatalf("recall=%v want 1", got)
	}
	if got := RecallAtK([]string{"x", "a"}, rel, 2); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("recall=%v want 0.5", got)
	}
	if got := RecallAtK([]string{"a"}, rel, 1); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("k=1 recall=%v want 0.5", got)
	}
	if got := RecallAtK(nil, map[string]int{}, 10); got != 0 {
		t.Fatalf("empty relevant should be 0, got %v", got)
	}
}

func TestNDCGAtK(t *testing.T) {
	rel := map[string]int{"a": 2, "b": 1}
	if got := NDCGAtK([]string{"a", "b"}, rel, 10); math.Abs(got-1) > 1e-9 {
		t.Fatalf("perfect ranking ndcg=%v want 1", got)
	}
	reversed := NDCGAtK([]string{"b", "a"}, rel, 10)
	if !(reversed > 0 && reversed < 1) {
		t.Fatalf("reversed ndcg=%v should be in (0,1)", reversed)
	}
	if got := NDCGAtK([]string{"x", "y"}, rel, 10); got != 0 {
		t.Fatalf("no relevant hit ndcg=%v want 0", got)
	}
}

func TestMRRAtK(t *testing.T) {
	rel := map[string]int{"a": 1}
	mrr, rank := MRRAtK([]string{"x", "a"}, rel, 10)
	if rank != 2 || math.Abs(mrr-0.5) > 1e-9 {
		t.Fatalf("mrr=%v rank=%d", mrr, rank)
	}
	mrr, rank = MRRAtK([]string{"x", "y"}, rel, 10)
	if rank != 0 || mrr != 0 {
		t.Fatalf("no hit should be 0/0, got %v/%d", mrr, rank)
	}
}

func TestRunOnTinyCorpus(t *testing.T) {
	corpus := []CorpusDoc{
		{URL: "https://go.dev/sched", Domain: "go.dev", Category: "official",
			Title: "Go 调度器", Description: "GMP", Body: "go 调度器 gmp goroutine", PublishedDaysAgo: 1},
		{URL: "https://blog.example/sched", Domain: "blog.example", Category: "blog",
			Title: "调度器笔记", Description: "GMP 源码", Body: "go 调度器 gmp 源码 笔记", PublishedDaysAgo: 2},
		{URL: "https://x.example/other", Domain: "x.example", Category: "blog",
			Title: "无关内容", Description: "别的主题", Body: "rust 生命周期", PublishedDaysAgo: 3},
	}
	queries := []Query{{Q: "go 调度器", Relevant: map[string]int{
		"https://go.dev/sched": 2, "https://blog.example/sched": 1,
	}}}
	rep, err := Run(corpus, queries)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Queries != 1 || len(rep.Details) != 1 {
		t.Fatalf("report=%+v", rep)
	}
	if rep.Recall20 < 1 {
		t.Fatalf("recall=%v want 1", rep.Recall20)
	}
	if rep.NDCG10 <= 0 || rep.NDCG10 > 1 {
		t.Fatalf("ndcg=%v", rep.NDCG10)
	}
	if rep.Details[0].Hits > 3 {
		t.Fatalf("hits=%d", rep.Details[0].Hits)
	}
}
