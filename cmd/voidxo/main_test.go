package main

import (
	"strings"
	"testing"
)

// 这段逻辑写错的症状是「站点莫名 403」，现场很难定位，所以把分支都钉住。
func TestCrawlUserAgentFrom(t *testing.T) {
	cases := []struct {
		name       string
		publicURL  string
		configured string
		want       string
	}{
		{
			name: "两个地址都没有：只报名字",
			want: "voidxo-spider/0.1",
		},
		{
			name:      "启动参数给的地址优先",
			publicURL: "https://stdseek.com",
			want:      "voidxo-spider/0.1 (+https://stdseek.com/bot)",
		},
		{
			name:       "回退到后台「站点地址」",
			configured: "https://stdseek.com",
			want:       "voidxo-spider/0.1 (+https://stdseek.com/bot)",
		},
		{
			name:       "启动参数为空时不该覆盖后台配置",
			publicURL:  "   ",
			configured: "https://a.example.org/",
			want:       "voidxo-spider/0.1 (+https://a.example.org/bot)",
		},
		{
			name:      "去掉尾部斜杠，避免拼出 //bot",
			publicURL: "https://a.example.org/",
			want:      "voidxo-spider/0.1 (+https://a.example.org/bot)",
		},
		{
			name:       "两边都带空白也能用",
			publicURL:  "  https://a.example.org  ",
			configured: "https://b.example.org",
			want:       "voidxo-spider/0.1 (+https://a.example.org/bot)",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := crawlUserAgentFrom(c.publicURL, c.configured); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

// 回归：UA 里不能再出现 voidxo.example 这种占位域名。
// 它导致过真实故障——站点把「自称 bot 但联系地址不存在」的请求直接 403。
func TestCrawlUserAgentHasNoPlaceholderDomain(t *testing.T) {
	for _, ua := range []string{
		crawlUserAgentFrom("", ""),
		crawlUserAgentFrom("https://stdseek.com", ""),
		crawlUserAgentFrom("", "https://stdseek.com"),
	} {
		if strings.Contains(ua, "voidxo.example") {
			t.Fatalf("UA 里还有占位域名：%q", ua)
		}
	}
	// token 必须保留：robots.txt 的 User-agent 匹配靠它，改了等于换了一个爬虫。
	if !strings.HasPrefix(crawlUserAgentFrom("https://stdseek.com", ""), "voidxo-spider/") {
		t.Fatal("UA 丢了 voidxo-spider 标识")
	}
}
