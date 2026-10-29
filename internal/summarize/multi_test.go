package summarize

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func goodModel(t *testing.T, text string, hits *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"conclusions\":[{\"text\":\"` + text + `\",\"citations\":[1]}]}"}}]}`))
	}))
}

func badModel(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failed", http.StatusBadGateway)
	}))
}

// 摘要接口顺序由调用方决定：管理员设了「优先用备用」就不能偷偷先撞主接口。
func TestMultiProviderHonoursConfiguredOrder(t *testing.T) {
	primaryHits, backupHits := 0, 0
	primary := goodModel(t, "主接口结论", &primaryHits)
	defer primary.Close()
	backup := goodModel(t, "备用结论", &backupHits)
	defer backup.Close()

	p := NewMultiProvider([]Endpoint{
		{Name: "backup", BaseURL: backup.URL, Model: "backup-model"},
		{Name: "primary", BaseURL: primary.URL, Model: "primary-model"},
	}, 0)
	cs, err := p.Summarize(context.Background(), Input{Passages: []Passage{{ID: 1, Text: "原文"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Text != "备用结论" {
		t.Fatalf("应使用第一个接口，实际 %+v", cs)
	}
	if backupHits != 1 || primaryHits != 0 {
		t.Fatalf("调用顺序不对：backup=%d primary=%d", backupHits, primaryHits)
	}
	if p.Name() != "openai-compat:backup-model" {
		t.Fatalf("provider 应反映实际使用的模型，实际 %q", p.Name())
	}
	if p.UsedEndpoint() != "backup" {
		t.Fatalf("used endpoint 应回传 backup，实际 %q", p.UsedEndpoint())
	}
}

// 第一个接口 5xx 时要自动往后试，而不是直接让整条摘要降级成抽取式。
func TestMultiProviderFallsBackToNextEndpoint(t *testing.T) {
	primary := badModel(t)
	defer primary.Close()
	backupHits := 0
	backup := goodModel(t, "备用结论", &backupHits)
	defer backup.Close()

	p := NewMultiProvider([]Endpoint{
		{Name: "primary", BaseURL: primary.URL, Model: "primary-model"},
		{Name: "backup", BaseURL: backup.URL, Model: "backup-model"},
	}, 0)
	cs, err := p.Summarize(context.Background(), Input{Passages: []Passage{{ID: 1, Text: "原文"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || backupHits != 1 {
		t.Fatalf("应回退到备用接口，实际 %+v hits=%d", cs, backupHits)
	}
	if p.UsedEndpoint() != "backup" {
		t.Fatalf("used endpoint 应为 backup，实际 %q", p.UsedEndpoint())
	}
}

// 全部接口都失败时仍要降级并说明「试过哪些接口」，不能只说一句模型不可用。
func TestMultiProviderAllFailReasonNamesEndpoints(t *testing.T) {
	a := badModel(t)
	defer a.Close()
	b := badModel(t)
	defer b.Close()

	p := NewMultiProvider([]Endpoint{
		{Name: "primary", BaseURL: a.URL, Model: "m1"},
		{Name: "backup", BaseURL: b.URL, Model: "m2"},
	}, 0)
	doc := Document{
		Query: "单元测试",
		Body:  "单元测试能够独立运行、快速验证结果，并且可以无限次重复执行。",
	}
	res := Summarize(context.Background(), doc, p, Options{})
	if res.Provider != "extractive" {
		t.Fatalf("全部失败应降级为抽取式，实际 %s", res.Provider)
	}
	if !strings.Contains(res.Reason, "primary") || !strings.Contains(res.Reason, "backup") {
		t.Fatalf("降级原因应写明试过哪些接口，实际 %q", res.Reason)
	}
}

// 未填地址/模型的接口要被跳过，不能把空接口当成「主接口」去请求。
func TestNewMultiProviderSkipsIncompleteEndpoints(t *testing.T) {
	if p := NewMultiProvider([]Endpoint{{Name: "primary"}}, 0); p != nil {
		t.Fatalf("全是空接口时应返回 nil，实际 %T", p)
	}
}
