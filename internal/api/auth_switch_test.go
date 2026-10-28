package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"voidxo/internal/auth"
	"voidxo/internal/store"
	"voidxo/internal/usage"
)

// 「普通用户注册与登录」开关：关掉只关普通用户，管理员必须始终能进。
func TestUserAuthSwitch(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	e.srv.Auth = auth.New(e.st)
	ctx := context.Background()

	hash, err := auth.HashPassword("strongpass1")
	if err != nil {
		t.Fatal(err)
	}
	// 第一个账号自动成为管理员，第二个是普通用户
	admin, err := e.st.CreateUser(ctx, "admin@example.com", hash, "member", true, "127.0.0.1", true)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Role != "admin" {
		t.Fatalf("first user should be admin, got %q", admin.Role)
	}
	if _, err := e.st.CreateUser(ctx, "user@example.com", hash, "member", true, "127.0.0.1", false); err != nil {
		t.Fatal(err)
	}

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}

	// 默认是开的：普通用户能登录
	if rec := post("/api/auth/login", `{"email":"user@example.com","password":"strongpass1"}`); rec.Code != http.StatusOK {
		t.Fatalf("member login should work by default, got %d %s", rec.Code, rec.Body.String())
	}

	if err := e.st.SetSetting(ctx, "feature.allow_user_auth", "false"); err != nil {
		t.Fatal(err)
	}

	// 注册：直接 403，且不应建号
	rec := post("/api/auth/register", `{"email":"new@example.com","password":"strongpass1"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("register should be forbidden when switch off, got %d %s", rec.Code, rec.Body.String())
	}
	if _, _, err := e.st.UserByEmail(ctx, "new@example.com"); err == nil {
		t.Fatal("blocked register must not create the account")
	}

	// 普通用户登录：403，并且不能留下会话 cookie（会话已建好，必须撤掉）
	rec = post("/api/auth/login", `{"email":"user@example.com","password":"strongpass1"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member login should be forbidden when switch off, got %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			t.Fatal("blocked login must not leave a session cookie")
		}
	}

	// 管理员不受影响
	if rec := post("/api/auth/login", `{"email":"admin@example.com","password":"strongpass1"}`); rec.Code != http.StatusOK {
		t.Fatalf("admin login must stay open, got %d %s", rec.Code, rec.Body.String())
	}
}

// 统计接口的总计口径：累计抓取/入库 + 账号数 + 运行起点。
func TestUsageTotalsAPI(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	e.srv.Usage = usage.New(e.st)
	ctx := context.Background()

	hash, err := auth.HashPassword("strongpass1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateUser(ctx, "admin@example.com", hash, "member", true, "127.0.0.1", true); err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddUsage(ctx, store.UsageRow{
		Day: "2026-10-20", Searches: 5, Crawls: 120, Indexed: 7, Summaries: 2,
	}); err != nil {
		t.Fatal(err)
	}
	// 测试环境只往 Bleve 写了文档，SQLite 的 documents 是空的；
	// 「总计入库」读的是 SQLite（去重折叠后仍可计数），这里补一条验证口径。
	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://go.dev/doc/error", Domain: "go.dev", Title: "error handling",
		Labels: "[]", WordCount: 10, FetchedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?days=30", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Totals store.UsageTotals `json:"totals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Totals.Crawls != 120 || out.Totals.Indexed != 7 || out.Totals.Searches != 5 {
		t.Fatalf("unexpected usage totals: %+v", out.Totals)
	}
	// 测试环境里预置了 2 篇 Bleve 文档，SQLite 只有刚补的这 1 条
	if out.Totals.Documents != 1 {
		t.Fatalf("documents total=%d want 1", out.Totals.Documents)
	}
	if out.Totals.Users != 1 || out.Totals.UsersToday != 1 {
		t.Fatalf("users total=%d today=%d want 1/1", out.Totals.Users, out.Totals.UsersToday)
	}
	if out.Totals.Since <= 0 {
		t.Fatalf("since should be the earliest admin creation time, got %d", out.Totals.Since)
	}
}
