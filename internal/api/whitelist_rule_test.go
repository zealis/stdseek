package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// 抓取规则（入口路径 + 文章 URL 正则）是后台可配置项：
// 以前 store 里有两个 setter，但没有任何调用方，界面也没有入口——
// 于是「平台站首页导航链接吃光配额」这类问题只能靠改代码解决。
func TestSetWhitelistRule(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	ctx := context.Background()
	if err := e.st.AddWhitelistWithPath(ctx, "juejin.cn", "platform", 3600, "/"); err != nil {
		t.Fatal(err)
	}
	sites, _, err := e.st.ListWhitelist(ctx, "juejin", 10, 0)
	if err != nil || len(sites) != 1 {
		t.Fatalf("白名单读取失败：%v sites=%d", err, len(sites))
	}
	path := "/admin/whitelist/" + strconv.FormatInt(sites[0].ID, 10) + "/rule"

	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec.Code
	}

	// 正则写错必须当场报错：静默存下来的后果是「规则看起来配了，实际匹配不到任何页」。
	if code := post(`{"seed_path":"/","article_pattern":"^/post/[0-9"}`); code != http.StatusBadRequest {
		t.Fatalf("非法正则 status=%d want 400", code)
	}
	if code := post(`{"seed_path":"https://evil.com/","article_pattern":""}`); code != http.StatusBadRequest {
		t.Fatalf("站外入口 status=%d want 400", code)
	}

	if code := post(`{"seed_path":"/","article_pattern":"^/post/[0-9]+$"}`); code != http.StatusOK {
		t.Fatalf("合法规则 status=%d want 200", code)
	}
	got, err := e.st.SiteArticlePattern(ctx, "juejin.cn")
	if err != nil {
		t.Fatal(err)
	}
	if got != `^/post/[0-9]+$` {
		t.Fatalf("落库的正则=%q", got)
	}

	// 清空 = 回到「不限制」，这条路径也要能用，否则配错了没法退回去。
	if code := post(`{"seed_path":"/","article_pattern":""}`); code != http.StatusOK {
		t.Fatalf("清空规则 status=%d want 200", code)
	}
	if got, _ := e.st.SiteArticlePattern(ctx, "juejin.cn"); got != "" {
		t.Fatalf("清空后仍有限制：%q", got)
	}
}
