// AI 站点评定的语料构造：把「这个站有什么」变成模型能读的一小段文本。
//
// 语料设计的三条原则（也是踩过坑之后的结论）：
//  1. 先给「站方自述」（站名 + 首页描述），再看内容。站方自述是最便宜的强信号。
//  2. 标题按发布时间取，不看入库顺序——最近入库的常是导航页/站务页，
//     真实数据里 k8s 的 Community、PostgreSQL 的 Press 就排在最前面。
//  3. 正文只抽 3 段（篇幅最长的代表页），并同时给程序统计出来的结构性信号：
//     「个人站 vs 平台」靠标题几乎判不出来，URL 形态与规则标签占比才是硬证据。
package api

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"voidxo/internal/classify"
	"voidxo/internal/kv"
	"voidxo/internal/processor"
	"voidxo/internal/store"
	"voidxo/internal/urlkey"
)

const (
	// corpusTitles 送给模型的标题条数：够看出主题分布，再多只是重复。
	corpusTitles = 10
	// corpusExcerpts 正文摘录条数。3 段已足够看出文风与广告密度。
	corpusExcerpts = 3
	// corpusMinTitleRunes 过短的标题是导航词（"Home"、"博客"），对判站点性质没有信息量。
	corpusMinTitleRunes = 6
)

// 用户主页/作者页的 URL 形态。命中率高说明「内容由大量不同用户发」。
var userPathMarkers = []string{
	"/u/", "/user/", "/users/", "/people/", "/member/", "/members/",
	"/profile/", "/author/", "/authors/", "/space/", "/uid/", "/@",
	"/user.php", "/home.php",
}

// 文章详情页的 URL 形态。用于判断「这站是文章站还是索引页/目录页」。
var articlePathMarkers = []string{
	"/p/", "/post/", "/posts/", "/article/", "/articles/", "/blog/",
	"/news/", "/archives/", "/questions/", "/docs/", "/a/", ".html", ".htm",
}

// /2024/、/2026/10/ 这类按日期分目录的路径，博客与新闻站最典型。
var datePath = regexp.MustCompile(`/(19|20)\d{2}/`)

// siteInput 构造一个站点的判定输入。
//
// 任何一路信号取不到都不算失败：模型看到「（无）」会退回其他信号判断，
// 而整个判定因为一次快照缺失就失败，会让批量操作出现一堆无意义的红条。
func (s *Server) siteInput(ctx context.Context, site store.WhitelistSite) classify.Input {
	in := classify.Input{
		Domain:      site.Domain,
		Description: site.SiteDesc,
	}
	names, _ := s.st.SiteNames(ctx, []string{site.Domain})
	if n := names[site.Domain]; n != "" {
		in.Name = n
	} else {
		in.Name = site.SiteName
	}

	// 语料一：标题（按发布时间倒序，过滤站务页与过短标题）
	titles, _ := s.st.DomainTitles(ctx, site.Domain, 40)
	in.Titles = pickTitles(titles, corpusTitles)

	// 语料二：正文摘录（按篇幅倒序取，篇幅长的更像该站的代表内容）
	samples, _ := s.st.DomainDocSample(ctx, site.Domain, 200)
	in.Excerpts = s.excerpts(ctx, samples, corpusExcerpts)

	// 站点描述只认首页 meta description（site_desc），必要时从首页快照现补。
	// 刻意不拿「最近一篇文档的摘要」顶替：那是某一页的内容简介，挂到「站点描述」这个位置
	// 就是误导模型——nodejs.org 会变成「node:assert 模块提供了一组断言函数」。
	// 宁可说（无），让模型去看标题与摘录。
	in.Description = s.siteDesc(ctx, site)

	// 结构性信号：全部是程序数出来的，不让模型猜
	docs := site.DocCount
	if docs == 0 {
		docs, _ = s.st.DomainDocCount(ctx, site.Domain)
	}
	ad, ai, tracker, _ := s.st.DomainLabelStats(ctx, site.Domain)
	in.Signals = classify.Signals{
		Docs:          docs,
		MedianWords:   medianWords(samples),
		UserPathPages: countPaths(samples, userPathMarkers, false),
		ArticlePages:  countPaths(samples, articlePathMarkers, true),
		AdTagged:      ad,
		AITagged:      ai,
		TrackerTagged: tracker,
	}
	return in
}

// siteDesc 站点描述：优先用抓取时存下的 site_desc；为空时从首页快照现取并落库。
//
// 为什么要现取：site_desc 是后加的列，历史站点要等下一轮抓取才有值。
// 而首页快照本来就在（重建索引靠它），抽一次 meta description 的成本可以忽略，
// 取到就写回，下次直接用。
func (s *Server) siteDesc(ctx context.Context, site store.WhitelistSite) string {
	if site.SiteDesc != "" {
		return site.SiteDesc
	}
	if s.snap == nil {
		return ""
	}
	for _, u := range []string{"https://" + site.Domain + "/", "http://" + site.Domain + "/"} {
		raw, found, err := s.snap.Get(ctx, kv.Key(urlkey.Key(u)))
		if err != nil || !found {
			continue
		}
		d := processor.SiteDescription(raw)
		if d == "" {
			continue
		}
		// 写回失败不影响本次判定（背景任务，下次还会再补一次）。
		_ = s.st.UpdateSiteDesc(context.Background(), site.Domain, d)
		return d
	}
	return ""
}

// pickTitles 过滤站务页、过短标题并去重，最多取 n 条。
func pickTitles(raw []string, n int) []string {
	out := make([]string, 0, n)
	seen := make(map[string]bool, len(raw))
	for _, t := range raw {
		t = strings.TrimSpace(t)
		if len([]rune(t)) < corpusMinTitleRunes || processor.IsBoilerplateTitle(t) {
			continue
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
		if len(out) >= n {
			break
		}
	}
	return out
}

// excerpts 从快照里抽正文摘录。
//
// 从 Pebble 快照现取而不是在 documents 里存一份正文：快照本来就有（重建索引靠它），
// 再存一份等于同一份内容写两遍；一次判定只读 3 篇，开销可忽略。
//
// 两轮取样：先只要「像文章详情页」的 URL，凑不够再用其余页面兜底。
// 原因是有站点的首页/频道页也在 documents 里（历史数据），那些页面的正文是
// 一屏文章导语拼起来，读起来像目录——拿它当代表会让模型以为整站都是列表。
func (s *Server) excerpts(ctx context.Context, samples []store.DocSample, n int) []string {
	if s.snap == nil || s.Proc == nil {
		return nil
	}
	out := make([]string, 0, n)
	for _, pass := range []bool{true, false} {
		for _, d := range samples {
			if len(out) >= n {
				return out
			}
			if pass && !looksLikeArticle(d.URL) {
				continue
			}
			raw, found, err := s.snap.Get(ctx, kv.Key(urlkey.Key(d.URL)))
			if err != nil || !found {
				continue
			}
			res, err := s.Proc.Process(d.URL, raw, processor.Options{})
			if err != nil {
				continue
			}
			// 链接密度高的「正文」其实是列表页/聚合页的文字（真实数据：美团首页的正文是
			// 一屏文章导语拼在一起）。这种摘录会让模型以为整站都是目录页。
			if res.LinkDensity > 0.3 {
				continue
			}
			text := strings.Join(strings.Fields(res.Body), " ")
			if len([]rune(text)) < 120 {
				continue // 太短的正文看不出文风，还会挤占提示词预算
			}
			out = append(out, text)
		}
	}
	return out
}

// looksLikeArticle URL 是否像文章详情页（同一套形态清单，和信号统计共用）。
func looksLikeArticle(raw string) bool {
	u := strings.ToLower(raw)
	for _, m := range articlePathMarkers {
		if strings.Contains(u, m) {
			return true
		}
	}
	return datePath.MatchString(u)
}

// medianWords 中位字数：均值会被少数超长文档带偏。
func medianWords(samples []store.DocSample) int {
	if len(samples) == 0 {
		return 0
	}
	w := make([]int, 0, len(samples))
	for _, s := range samples {
		w = append(w, s.WordCount)
	}
	sort.Ints(w)
	return w[len(w)/2]
}

// countPaths 统计 URL 命中某组形态的页数。
// withDate 为真时额外把「按日期分目录」的路径算作文章页。
func countPaths(samples []store.DocSample, markers []string, withDate bool) int {
	n := 0
	for _, s := range samples {
		u := strings.ToLower(s.URL)
		hit := false
		for _, m := range markers {
			if strings.Contains(u, m) {
				hit = true
				break
			}
		}
		if !hit && withDate && datePath.MatchString(u) {
			hit = true
		}
		if hit {
			n++
		}
	}
	return n
}
