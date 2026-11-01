package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zealis/stdseek/internal/store"
)

// 异常名单的后台接口：列表/计数/筛选、重抓的白名单校验、移出名单。
func TestCrawlIssuesAdminAPI(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()
	if err := e.st.RecordCrawlIssue(ctx, "go.dev", store.CrawlIssueUnreachable, "种子页返回 HTTP 503"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RecordCrawlIssue(ctx, "blog.example.com", store.CrawlIssueStructure, "抓到 5 页但没有可索引正文"); err != nil {
		t.Fatal(err)
	}

	do := func(method, target, body string) *httptest.ResponseRecorder {
		var rd *strings.Reader
		if body == "" {
			rd = strings.NewReader("")
		} else {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, target, rd)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}

	// 列表：总数、两条记录、各类型计数
	rec := do(http.MethodGet, "/admin/issues", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var list struct {
		Total  int                `json:"total"`
		Items  []store.CrawlIssue `json:"items"`
		Counts map[string]int     `json:"counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 || len(list.Items) != 2 {
		t.Fatalf("want 2 issues, got total=%d items=%d", list.Total, len(list.Items))
	}
	if list.Counts[store.CrawlIssueUnreachable] != 1 || list.Counts["all"] != 2 {
		t.Fatalf("unexpected counts: %+v", list.Counts)
	}

	// 按类别筛选；拼错的类别当「全部」，不能 400
	rec = do(http.MethodGet, "/admin/issues?kind="+store.CrawlIssueStructure, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if list.Total != 1 || list.Items[0].Kind != store.CrawlIssueStructure {
		t.Fatalf("kind filter failed: total=%d %+v", list.Total, list.Items)
	}
	rec = do(http.MethodGet, "/admin/issues?kind=whatever", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if list.Total != 2 {
		t.Fatalf("unknown kind should mean all, got %d", list.Total)
	}

	// 未接爬虫时重抓要明确报未启用，而不是静默成功
	rec = do(http.MethodPost, "/admin/issues/recrawl", `{"domains":["go.dev"]}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("nil recrawler status=%d want 501", rec.Code)
	}

	// 重抓只允许白名单域名：不在白名单的、被禁用的都不进入任务
	var got []string
	e.srv.RecrawlSites = func(domains []string) error {
		got = domains
		return nil
	}
	rec = do(http.MethodPost, "/admin/issues/recrawl", `{"domains":["go.dev","evil.example","GO.DEV"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("recrawl status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(got) != 1 || got[0] != "go.dev" {
		t.Fatalf("recrawl should only keep whitelisted domains, got %v", got)
	}

	// 已有一轮在跑：409，让页面能说清「等上一轮结束」
	e.srv.RecrawlSites = func([]string) error { return ErrCrawlBusy }
	rec = do(http.MethodPost, "/admin/issues/recrawl", `{"domains":["go.dev"]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("busy status=%d want 409", rec.Code)
	}

	// 移出名单：只删指定的那条
	rec = do(http.MethodPost, "/admin/issues/clear", `{"domains":["go.dev"]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cleared":1`) {
		t.Fatalf("clear failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(http.MethodGet, "/admin/issues", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if list.Total != 1 || list.Items[0].Domain != "blog.example.com" {
		t.Fatalf("after clear: total=%d %+v", list.Total, list.Items)
	}
}
