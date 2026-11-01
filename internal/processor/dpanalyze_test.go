package processor

// 临时分析（跑完即删）：新旧指纹在 337 篇真实语料上的跨站距离分布。
// 判断「正文 3-gram shingle + 去停用词 + 去掉标题权重」是否真能
// 推开无关页面、同时保住真重复——这是改 dedup 之前必须有的证据。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/zealis/stdseek/internal/kv"
	"github.com/zealis/stdseek/internal/store"
	"github.com/zealis/stdseek/internal/urlkey"
)

var dpStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`的 了 是 在 和 与 或 也 就 都 而 及 其 这 那 有 我 你 他 它
		一个 一些 我们 他们 可以 如果 因为 所以 但是 然后 这个 那个 什么 怎么 如何
		the a an of to in for on with and or is are was were be been it this that
		as at by from not no you we they he she can will would should if then than
		use used using when which what how all any but its their our`) {
		dpStop[w] = true
	}
}

type dpDoc struct {
	URL    string `json:"url"`
	Domain string `json:"domain"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// bothFp 一次分词算出两套指纹——分词是这里唯一的开销来源，
// 每篇切三遍（标题、正文、正文再来一次）会把 337 篇的分析拖到两分钟以上。
func bothFp(p *Processor, title, body string) (old, new uint64) {
	tb := p.cut(title)
	bb := p.cut(body)
	all := append(append([]string{}, tb...), bb...)
	old = Simhash(all, weightsFor(len(tb), len(all)))

	clean := make([]string, 0, len(bb))
	for _, t := range bb {
		if !dpStop[t] {
			clean = append(clean, t)
		}
	}
	if len(clean) < 3 {
		return old, Simhash(clean, nil)
	}
	grams := make([]string, 0, len(clean)-2)
	for i := 0; i+3 <= len(clean); i++ {
		grams = append(grams, clean[i]+"\x1f"+clean[i+1]+"\x1f"+clean[i+2])
	}
	return old, Simhash(grams, nil)
}

func TestDPAnalyze(t *testing.T) {
	f, err := os.Open("../../eval/corpus-real.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var docs []dpDoc
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var d dpDoc
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			t.Fatal(err)
		}
		if len([]rune(d.Body)) < 200 {
			continue
		}
		docs = append(docs, d)
	}
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}

	type fp struct{ old, new uint64 }
	fps := make([]fp, len(docs))
	for i, d := range docs {
		o, n := bothFp(p, d.Title, d.Body)
		fps[i] = fp{old: o, new: n}
	}

	type pair struct {
		i, j       int
		oldD, newD int
	}
	var pairs []pair
	for i := 0; i < len(docs); i++ {
		for j := i + 1; j < len(docs); j++ {
			if docs[i].Domain == docs[j].Domain {
				continue
			}
			pairs = append(pairs, pair{i, j, Hamming(fps[i].old, fps[j].old), Hamming(fps[i].new, fps[j].new)})
		}
	}
	fmt.Printf("语料 %d 篇，跨站对 %d 个\n", len(docs), len(pairs))

	cntOld, cntNew := 0, 0
	for _, pr := range pairs {
		if pr.oldD <= 1 {
			cntOld++
		}
		if pr.newD <= 1 {
			cntNew++
		}
	}
	fmt.Printf("距离≤1（现阈值）：旧 %d 对 / 新 %d 对\n", cntOld, cntNew)

	sortedNew := append([]pair(nil), pairs...)
	sort.Slice(sortedNew, func(a, b int) bool { return sortedNew[a].newD < sortedNew[b].newD })
	fmt.Println("\n新指纹最接近的 10 对：")
	for k, pr := range sortedNew {
		if k >= 10 {
			break
		}
		fmt.Printf("  新d=%-2d 旧d=%-2d %s\n               %s\n",
			pr.newD, pr.oldD, trunc(docs[pr.i].URL, 72), trunc(docs[pr.j].URL, 72))
	}

	sortedOld := append([]pair(nil), pairs...)
	sort.Slice(sortedOld, func(a, b int) bool { return sortedOld[a].oldD < sortedOld[b].oldD })
	fmt.Println("\n旧指纹最近、新指纹推开的 8 对：")
	shown := 0
	for _, pr := range sortedOld {
		if pr.newD < 8 {
			continue
		}
		fmt.Printf("  旧d=%-2d 新d=%-2d %s\n               %s\n",
			pr.oldD, pr.newD, trunc(docs[pr.i].URL, 72), trunc(docs[pr.j].URL, 72))
		shown++
		if shown >= 8 {
			break
		}
	}

	fmt.Println("\n距离分布（跨站）：")
	fmt.Printf("  %-8s %10s %10s\n", "区间", "旧指纹", "新指纹")
	for _, b := range [][2]int{{0, 1}, {2, 3}, {4, 6}, {7, 10}, {11, 16}, {17, 64}} {
		o, n := 0, 0
		for _, pr := range pairs {
			if pr.oldD >= b[0] && pr.oldD <= b[1] {
				o++
			}
			if pr.newD >= b[0] && pr.newD <= b[1] {
				n++
			}
		}
		fmt.Printf("  %-8s %10d %10d\n", fmt.Sprintf("%d-%d", b[0], b[1]), o, n)
	}
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// TestDPReprintSimulation 模拟采集站搬运的两种典型手法，看新旧指纹谁能认出来。
//
// 手法一：改标题（「XX 原题」→「【转载】原题 - 某某的博客」）。标题权重高，
// 旧指纹会被拉偏；新指纹不看标题，应当纹丝不动。
// 手法二：正文尾部加推广位/版权声明。shingle 只受末尾片段影响，距离应当仍然很小。
func TestDPReprintSimulation(t *testing.T) {
	f, err := os.Open("../../eval/corpus-real.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var docs []dpDoc
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var d dpDoc
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			t.Fatal(err)
		}
		if len([]rune(d.Body)) < 600 {
			continue
		}
		docs = append(docs, d)
		if len(docs) >= 6 {
			break
		}
	}
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}

	footer := "\n\n本文由技术搬运工整理发布，转载请注明出处。更多精彩内容请关注公众号「某某技术」。" +
		"相关推荐：深入理解 JVM、Kubernetes 实战、Go 语言进阶、分布式系统设计。"
	fmt.Printf("%-46s %10s %10s %10s %10s\n", "文档", "旧-改标题", "新-改标题", "旧-加页脚", "新-加页脚")
	for _, d := range docs {
		o1, n1 := bothFp(p, d.Title, d.Body)
		o2, n2 := bothFp(p, "【转载】"+d.Title+" - 某某的博客", d.Body)
		o3, n3 := bothFp(p, d.Title, d.Body+footer)
		fmt.Printf("%-46s %10d %10d %10d %10d\n",
			trunc(d.URL, 44),
			Hamming(o1, o2), Hamming(n1, n2),
			Hamming(o1, o3), Hamming(n1, n3))
	}
	fmt.Println("\n（跨站阈值现状 1；旧指纹在改标题后距离明显拉开 → 认不出转载）")
}

// TestDegradedTitleDiagnosis 诊断标题退化的成因：是提取漏了更好的源，
// 还是页面本身就没有。
//
// 结论已记录在 processor.go 的 DegradedTitle 注释里：约 128 篇退化，
// <title> 原文就是域名/导航词，og:title 一篇也没有——兜底链到 <h1> 就断了。
// 这个测试把那个结论变成可复现的数字，避免将来有人重复踩。
func TestDegradedTitleDiagnosis(t *testing.T) {
	db, err := store.Open("../../data/stdseek.db")
	if err != nil {
		t.Skip("无法打开 data/stdseek.db:", err)
	}
	defer db.Close()
	snap, err := kv.Open("../../data/kv")
	if err != nil {
		t.Skip("无法打开 data/kv:", err)
	}
	defer snap.Close()

	titleRe := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	ogRe := regexp.MustCompile(`(?is)<meta[^>]+property=["']og:title["'][^>]+content=["']([^"']+)["']`)

	docs, err := db.Fingerprints(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var degraded, titleIsDomain, noTitle, hasOG int
	for _, f := range docs {
		if f.DupOf != "" {
			continue
		}
		doc, ok, err := db.GetDocument(context.Background(), f.URL)
		if err != nil || !ok {
			continue
		}
		if !DegradedTitle(doc.Title, f.Domain) {
			continue
		}
		degraded++
		raw, found, err := snap.Get(kv.Key(urlkey.Key(f.URL)))
		if err != nil || !found {
			noTitle++
			continue
		}
		if m := titleRe.FindSubmatch(raw); len(m) > 1 {
			t := clean(string(m[1]))
			if t == "" || DegradedTitle(t, f.Domain) {
				titleIsDomain++
			}
		} else {
			noTitle++
		}
		if ogRe.Match(raw) {
			hasOG++
		}
	}
	fmt.Printf("\n标题退化 %d 篇：<title> 就是域名/空 %d、没有 <title> %d、带 og:title 的 %d\n",
		degraded, titleIsDomain, noTitle, hasOG)
	if degraded > 0 && hasOG == 0 {
		t.Log("退化页面连 og:title 都没有——兜底链到头，加 og:title 提取是零收益")
	}
}
