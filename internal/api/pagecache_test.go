package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"voidxo/internal/kv"
	"voidxo/internal/urlkey"
)

// 快照接口的进程内缓存：同一页第二次读取不再落到存储层。
// 云端为主模式下这一跳是跨洋回源（实测 0.5s 上下），用户来回点同一页会很难受。
func TestPageSnapshotCacheServesRepeats(t *testing.T) {
	h, snap := newTestServer(t)
	const raw = "https://go.dev/doc/error"
	key := urlkey.Key(raw)
	if err := snap.Put(kv.Key(key), []byte("<html>snapshot</html>")); err != nil {
		t.Fatal(err)
	}

	get := func() (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/page?url="+url.QueryEscape(raw), nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out struct {
			HTML string `json:"html"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.HTML
	}

	code, html := get()
	if code != http.StatusOK || html != "<html>snapshot</html>" {
		t.Fatalf("首次读取失败 code=%d html=%q", code, html)
	}

	// 把底层那份删掉：第二次还能拿到内容，说明走的是进程内缓存。
	if err := snap.Delete(kv.Key(key)); err != nil {
		t.Fatal(err)
	}
	code, html = get()
	if code != http.StatusOK || html != "<html>snapshot</html>" {
		t.Fatalf("第二次应命中缓存 code=%d html=%q", code, html)
	}
}

// 单个 IP 每分钟读取快照的次数上限：挡住「按着按钮不放」和脚本刷。
func TestPageSnapshotRateLimitedPerIP(t *testing.T) {
	h, _ := newTestServer(t)

	call := func(i int) int {
		u := "https://nope.example/page-" + strconv.Itoa(i)
		req := httptest.NewRequest(http.MethodGet, "/api/page?url="+url.QueryEscape(u), nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// 前 30 次是正常的「没有快照」（404），第 31 次起被限流。
	for i := 0; i < pageLimitPerMinute; i++ {
		if code := call(i); code != http.StatusNotFound {
			t.Fatalf("第 %d 次应 404，got %d", i+1, code)
		}
	}
	if code := call(pageLimitPerMinute); code != http.StatusTooManyRequests {
		t.Fatalf("超过每分钟上限应 429，got %d", code)
	}
}
