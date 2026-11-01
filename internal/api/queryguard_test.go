package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zealis/stdseek/internal/index"
)

// 不可见字符必须在进索引之前就没了：控制字符会把日志行拆坏，
// 零宽与双向覆盖字符能做文字伪装（显示成正常中文、字节顺序不同）。
func TestNormalizeQueryStripsInvisible(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"控制字符", "go\x00\x01\x1flang", "golang"},
		{"C1 控制字符", "go\u0085lang", "golang"},
		{"零宽空格", "go\u200blang", "golang"},
		{"零宽连接符与 BOM", "\ufeffgo\u200d\u200clang", "golang"},
		{"双向覆盖", "go\u202elang\u202c", "golang"},
		{"折叠空白", "  go \t\n\n 语言  ", "go 语言"},
		{"全角空格也是空白", "go\u3000语言", "go 语言"},
	}
	for _, c := range cases {
		got, err := normalizeQuery(c.in)
		if err != nil {
			t.Fatalf("%s：不该报错，got %v", c.name, err)
		}
		if got != c.want {
			t.Fatalf("%s：want %q got %q", c.name, c.want, got)
		}
	}
}

func TestNormalizeQueryRejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"空", ""},
		{"只有空白", "   \t\n "},
		{"只有不可见字符", "\u200b\u200c\u200d\ufeff"},
		{"只有符号", "...!!!"},
		{"超长", strings.Repeat("中", maxQueryRunes+1)},
		{"词太多", strings.Repeat("a ", maxQueryTerms+1)},
	}
	for _, c := range cases {
		if _, err := normalizeQuery(c.in); err == nil {
			t.Fatalf("%s：应该被拒", c.name)
		}
	}
	// 边界：正好到上限的必须放行（差一位就拒会误伤，也说明不了任何事）
	if _, err := normalizeQuery(strings.Repeat("中", maxQueryRunes)); err != nil {
		t.Fatalf("正好 %d 字符应该放行：%v", maxQueryRunes, err)
	}
	// 符号不能一概拒：C++ / C# / .NET 是真实查询
	for _, q := range []string{"C++", "C#", ".NET", "a+b"} {
		if _, err := normalizeQuery(q); err != nil {
			t.Fatalf("%q 应该放行：%v", q, err)
		}
	}
}

// 800KB 的 URL 不该进得来：Go 只按 MaxHeaderBytes（默认 1MB）限整个头部。
func TestSearchLongURIReturns414(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	long := "/api/search?q=x&pad=" + strings.Repeat("a", maxURIBytes+100)
	req := httptest.NewRequest(http.MethodGet, long, nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestURITooLong {
		t.Fatalf("status=%d want 414 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSearchRejectsTooManyListArgs(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	many := strings.TrimSuffix(strings.Repeat("ai,", maxListArg+1), ",")
	for _, param := range []string{"exclude", "cat", "fields"} {
		req := httptest.NewRequest(http.MethodGet, "/api/search?q=error&"+param+"="+many, nil)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 项数超限应 400，got %d", param, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), param) {
			t.Fatalf("%s 的报错要点名是哪个参数：%s", param, rec.Body.String())
		}
	}
}

// 深翻页每翻一页都要跑一次完整排序，第 100 页之后直接拒。
func TestSearchRejectsPageBeyondCap(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=error&page=99999", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "100") {
		t.Fatalf("应说明上限：%s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/search?q=error&page=100", nil)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("第 100 页应该放行，got %d", rec.Code)
	}
}

// 搜索结果页不索引：noindex，且**不能**有 canonical 指向 /search。
func TestSearchPageNoIndex(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	req := httptest.NewRequest(http.MethodGet, "/search?q=error", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, `<meta name="robots" content="noindex, nofollow">`) {
		t.Fatalf("搜索结果页缺 noindex：%s", body)
	}
	if strings.Contains(body, `rel="canonical"`) {
		t.Fatalf("搜索结果页不该有 canonical：%s", body)
	}
	if got := rec.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Fatalf("X-Robots-Tag=%q", got)
	}

	// 首页该有的照旧（别把整站都标成 noindex）
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	body = rec.Body.String()
	if strings.Contains(body, `name="robots"`) {
		t.Fatalf("首页不该有 noindex：%s", body)
	}
	if !strings.Contains(body, `rel="canonical"`) {
		t.Fatal("首页应该有 canonical")
	}
}

// JSON 接口不该被索引：响应头是最硬的一层，robots.txt 只是请求。
func TestAPISearchHasXRobotsTag(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=error", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Fatalf("X-Robots-Tag=%q", got)
	}
}

// 缓存准入：第一次见到只登记，第二次才占位——否则 512 个随机串就能把表填满。
func TestSearchCacheAdmissionNeedsTwoHits(t *testing.T) {
	c := newSearchCache()
	hits := []index.Hit{{URL: "https://go.dev/", Domain: "go.dev"}}
	key := "k1"

	c.put(0, key, hits, 1)
	if _, ok := c.get(0, key); ok {
		t.Fatal("第一次出现不该进缓存")
	}
	c.put(0, key, hits, 1)
	// get 会先比对索引代数，两处传同一个代数
	if _, ok := c.get(0, key); !ok {
		t.Fatal("第二次出现应该进缓存")
	}
}

func TestCacheableQuery(t *testing.T) {
	if cacheableQuery(index.Query{Q: "go"}) != true {
		t.Fatal("普通查询应可缓存")
	}
	// 超长：真实查询不会这么长，缓存它只会挤掉热点
	if cacheableQuery(index.Query{Q: strings.Repeat("中", cacheableRunes+1)}) {
		t.Fatal("超长查询不该进缓存")
	}
	// 词太多
	if cacheableQuery(index.Query{Q: strings.Repeat("a ", cacheableTerms+1)}) {
		t.Fatal("词太多不该进缓存")
	}
}

// 搜索有独立的一档限流：全局 120/min 不能全被最贵的接口吃掉。
func TestSearchLimiterIsStricterThanGlobal(t *testing.T) {
	l := newRateLimiter(60)
	now := time.Now()
	allowed := 0
	for i := 0; i < 61; i++ {
		if l.allow("1.2.3.4", now) {
			allowed++
		}
	}
	if allowed != 60 {
		t.Fatalf("应放行 60 次，实际 %d", allowed)
	}
}

// logSafe 的二次兜底：即便上游没洗干净，日志里也不能出现换行。
func TestLogSafeStripsNewlines(t *testing.T) {
	got := logSafe("a\nb\rc\x00d")
	if strings.ContainsAny(got, "\n\r\x00") {
		t.Fatalf("日志里仍有控制字符：%q", got)
	}
	if n := len([]rune(logSafe(strings.Repeat("中", 200)))); n > 66 {
		t.Fatalf("应截断，实际 %d 字符", n)
	}
}
