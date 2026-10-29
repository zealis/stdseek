package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLooksLikeQuestion(t *testing.T) {
	cases := []struct {
		q    string
		want bool
	}{
		{"deepseek", false},
		{"deepseek是什么", true},
		{"如何学习 Go", true},
		{"Go 和 Rust 的区别", true},
		{"what is deepseek", true},
		{"this is good", false},
		{"go", false},
		{"deepseek 介绍", true},
	}
	for _, c := range cases {
		if got := looksLikeQuestion(c.q); got != c.want {
			t.Errorf("looksLikeQuestion(%q)=%v want %v", c.q, got, c.want)
		}
	}
}

func TestOverviewRetrievalQueryDropsQuestionWords(t *testing.T) {
	if got := overviewRetrievalQuery("deepseek是什么"); got != "deepseek" {
		t.Fatalf("应去掉中文疑问词，got %q", got)
	}
	if got := overviewRetrievalQuery("what is deepseek"); got != "deepseek" {
		t.Fatalf("应去掉英文疑问词，got %q", got)
	}
}

func TestOverviewRequiresFeature(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	req := httptest.NewRequest(http.MethodPost, "/api/overview", strings.NewReader(`{"q":"deepseek是什么"}`))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("功能关闭应 403，got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOverviewSkipsNonQuestionWithoutCallingModel(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()
	_ = e.st.SetSetting(ctx, "feature.ai_overview", "true")
	var calls int32
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"answer\":\"不该被调用\",\"citations\":[1]}"}}]}`))
	}))
	defer svc.Close()
	_ = e.st.SetSetting(ctx, "ai.base_url", svc.URL)
	_ = e.st.SetSetting(ctx, "ai.model", "m")

	req := httptest.NewRequest(http.MethodPost, "/api/overview", strings.NewReader(`{"q":"error handling"}`))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out overviewResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Question {
		t.Fatal("普通关键词不应生成概览")
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("普通关键词不应调用模型")
	}
}

func TestOverviewGeneratesWithCitedSources(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()
	_ = e.st.SetSetting(ctx, "feature.ai_overview", "true")
	var calls int32
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"answer\":\"错误处理主要靠包装与判断。[1]\",\"citations\":[1]}"}}]}`))
	}))
	defer svc.Close()
	_ = e.st.SetSetting(ctx, "ai.base_url", svc.URL)
	_ = e.st.SetSetting(ctx, "ai.model", "m")

	req := httptest.NewRequest(http.MethodPost, "/api/overview", strings.NewReader(`{"q":"error handling是什么"}`))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out overviewResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Question || out.Answer == "" || len(out.Sources) == 0 {
		t.Fatalf("应生成带来源的概览，got %+v", out)
	}
	if out.Sources[0].ID != 1 || out.Provider != "openai-compat:m" {
		t.Fatalf("来源/Provider 不对：%+v", out)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("应只调用一次模型，实际 %d", calls)
	}

	// 清掉进程内缓存后，仍应命中 SQLite 持久缓存（热门问题跨重启不重复调模型）。
	e.srv.clearOverviewCache()
	req = httptest.NewRequest(http.MethodPost, "/api/overview", strings.NewReader(`{"q":"error handling是什么"}`))
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("持久缓存命中不应再调用模型，实际 %d", calls)
	}
}

// 「AI 总结优先接口」要真的改变概览的尝试顺序：选了第二备用就先打它。
func TestOverviewUsesConfiguredPreferredEndpoint(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()
	_ = e.st.SetSetting(ctx, "feature.ai_overview", "true")
	_ = e.st.SetSetting(ctx, "ai.summary_endpoint", "backup2")

	var primaryCalls, backupCalls, backup2Calls int32
	bad := func(calls *int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(calls, 1)
			http.Error(w, "down", http.StatusBadGateway)
		}))
	}
	p := bad(&primaryCalls)
	defer p.Close()
	b := bad(&backupCalls)
	defer b.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&backup2Calls, 1)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"answer\":\"来自第二备用的回答\",\"citations\":[1]}"}}]}`))
	}))
	defer good.Close()

	_ = e.st.SetSetting(ctx, "ai.base_url", p.URL)
	_ = e.st.SetSetting(ctx, "ai.model", "p")
	_ = e.st.SetSetting(ctx, "ai.backup_base_url", b.URL)
	_ = e.st.SetSetting(ctx, "ai.backup_model", "b")
	_ = e.st.SetSetting(ctx, "ai.backup2_base_url", good.URL)
	_ = e.st.SetSetting(ctx, "ai.backup2_model", "b2")

	req := httptest.NewRequest(http.MethodPost, "/api/overview", strings.NewReader(`{"q":"error handling是什么"}`))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out overviewResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Answer != "来自第二备用的回答" {
		t.Fatalf("应使用选中的第二备用，got %+v", out)
	}
	if backup2Calls != 1 || primaryCalls != 0 || backupCalls != 0 {
		t.Fatalf("应先打第二备用：p=%d b=%d b2=%d", primaryCalls, backupCalls, backup2Calls)
	}
}

func TestOverviewWithoutEndpointsReturnsReason(t *testing.T) {	e := newTestEnv(t, testOpts{})
	_ = e.st.SetSetting(context.Background(), "feature.ai_overview", "true")
	req := httptest.NewRequest(http.MethodPost, "/api/overview", strings.NewReader(`{"q":"error handling是什么"}`))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var out overviewResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Question != true || out.Answer != "" || out.Reason == "" {
		t.Fatalf("没配接口时应返回原因，got %+v", out)
	}
}

func TestOverviewHidesAnswerWithoutCitations(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()
	_ = e.st.SetSetting(ctx, "feature.ai_overview", "true")
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"answer\":\"没有出处的回答\",\"citations\":[]}"}}]}`))
	}))
	defer svc.Close()
	_ = e.st.SetSetting(ctx, "ai.base_url", svc.URL)
	_ = e.st.SetSetting(ctx, "ai.model", "m")

	req := httptest.NewRequest(http.MethodPost, "/api/overview", strings.NewReader(`{"q":"error handling是什么"}`))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var out overviewResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Answer != "" || out.Reason == "" {
		t.Fatalf("没有有效引用时不应展示回答，got %+v", out)
	}
}
