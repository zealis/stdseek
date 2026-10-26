package urlkey

import "testing"

func TestKeyNormalization(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://Go.dev/doc/?x=1", "go.dev/doc"},
		{"https://go.dev:443/doc", "go.dev/doc"},
		{"http://go.dev:80/doc/", "go.dev/doc"},
		{"http://go.dev:8080/doc", "go.dev:8080/doc"},
		{"https://go.dev/", "go.dev"},
		{"https://go.dev", "go.dev"},
		{"https://www.infoq.cn/content-center/#about", "www.infoq.cn/content-center"},
		{"https://www.infoq.cn/content-center/", "www.infoq.cn/content-center"},
		{"http://127.0.0.1:8001/", "127.0.0.1:8001"},
		{"http://127.0.0.1:8002/", "127.0.0.1:8002"},
		{"", ""},
		{"not a url", ""},
	}
	for _, c := range cases {
		if got := Key(c.in); got != c.want {
			t.Errorf("Key(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 同一主机的不同端口必须是不同 key，否则两个服务在索引里互相覆盖。
func TestKeyKeepsDistinctPorts(t *testing.T) {
	a := Key("http://127.0.0.1:8001/")
	b := Key("http://127.0.0.1:8002/")
	if a == b {
		t.Fatalf("different ports must not collide: %q == %q", a, b)
	}
}

func TestKeysPrefixes(t *testing.T) {
	got := Keys("https://go.dev/doc/x/")
	want := []string{"go.dev", "go.dev/doc", "go.dev/doc/x"}
	if len(got) != len(want) {
		t.Fatalf("Keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Keys[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if Keys("") != nil {
		t.Fatal("empty url should return nil")
	}
}

func TestCanonical(t *testing.T) {
	cases := []struct{ in, want string }{
		// 锚点是页内定位，不是不同页面
		{"https://www.cnblogs.com/a/p/123#commentform", "https://www.cnblogs.com/a/p/123"},
		{"https://nodejs.org/api/http.html#http_response_addtrailers_headers",
			"https://nodejs.org/api/http.html"},
		// 查询串保留：有些站点用 ?p=123 标识文章，抹掉会指向错误页面
		{"https://example.com/post?id=7#top", "https://example.com/post?id=7"},
		// 投放追踪参数清掉：结果链接里带 utm_* 既难看又与「不追踪」的定位相悖
		{"https://www.infoq.cn/article/abc?utm_source=infoq&utm_medium=header_graybar",
			"https://www.infoq.cn/article/abc"},
		{"https://example.com/a?p=2&utm_source=x&spm=y", "https://example.com/a?p=2"},
		{"https://example.com/a?utm_source=x&id=9", "https://example.com/a?id=9"},
		// 主机小写、默认端口归一
		{"HTTPS://Example.COM:443/Path#x", "https://example.com/Path"},
		// 空路径补斜杠，保证链接可直接点开
		{"https://example.com", "https://example.com/"},
		{"https://example.com/#frag", "https://example.com/"},
	}
	for _, c := range cases {
		if got := Canonical(c.in); got != c.want {
			t.Errorf("Canonical(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// Canonical 与 Key 必须指向同一页面：Key 是索引 ID，Canonical 是入库 URL，
// 两者不一致就会出现「索引里一个 ID、documents 里两个 URL」的漂移。
func TestCanonicalAndKeyAgree(t *testing.T) {
	urls := []string{
		"https://example.com/a/b#x",
		"https://example.com/a/b?q=1",
		"https://example.com/a/b/",
	}
	for _, u := range urls {
		if Key(Canonical(u)) != Key(u) {
			t.Errorf("key mismatch for %q: %q vs %q", u, Key(Canonical(u)), Key(u))
		}
	}
}
