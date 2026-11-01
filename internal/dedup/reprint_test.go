package dedup

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/zealis/stdseek/internal/processor"
	"github.com/zealis/stdseek/internal/store"
)

// 采集站识别一直没有真实样本可验：白名单 23 站全是官方文档站与 UGC 平台
// （cnblogs/aliyun/juejin/segmentfault 是「用户投稿」，不是批量搬运的采集站），
// CSDN / 简书 / 51CTO 这类一个都不在库里。所以用合成的搬运对照集来量化
// 跨站指纹的能力边界：从真实语料取原创文档，按采集站的常见手法生成变体，
// 看聚类能否把它们折回同一簇（召回），同时确认不会误折无关文档（误判）。
//
// 这是「能力就位但无样本」期间的替代品，不是真实分布的证据——
// 真实样本接入后应以实测为准，本测试用于防止实现退化。

type reprintCase struct {
	name string
	// apply 返回搬运后的 (标题, 正文)
	apply func(title, body string) (string, string)
	// mustRecall 该手法是否必须召回（采集站的最低要求）
	mustRecall bool
}

var reprintCases = []reprintCase{
	{"原样搬运", func(t, b string) (string, string) { return t, b }, true},
	{"改标题", func(t, b string) (string, string) {
		// 采集站必改标题：加前后缀。正因如此指纹才不能吃标题（shingle 的设计前提）。
		return "【转载】" + t + " - 某某技术博客", b
	}, true},
	{"加页脚推广", func(t, b string) (string, string) {
		return t, b + "\n\n本文由技术搬运工整理发布，转载请注明出处。" +
			"更多精彩内容请关注公众号「某某技术」。相关推荐：深入理解 JVM、Kubernetes 实战、" +
			"Go 语言进阶、分布式系统设计实战、MySQL 调优指南。"
	}, true},
	{"中间插广告", func(t, b string) (string, string) {
		r := []rune(b)
		mid := len(r) / 2
		ad := "\n\n【广告】云服务器限时特惠，新用户首年 99 元，点击了解详情。" +
			"企业级数据库、CDN 加速、对象存储全线优惠中，欢迎咨询。\n\n"
		return t, string(r[:mid]) + ad + string(r[mid:])
	}, false}, // shingle 保序，中间插入会切断约 2 个 3-gram；能否召回取决于插入长度
	{"截断末尾", func(t, b string) (string, string) {
		r := []rune(b)
		return t, string(r[:len(r)*8/10])
	}, false},
	{"段落重排", func(t, b string) (string, string) {
		// corpus 的 body 是分词后的词序列（空格分隔，无句号无换行），
		// 所以按词切块而不是按句号切句——第一版按「。」切分，
		// 结果切不出 8 段直接返回原文，距离恒为 0，等于没测。
		toks := strings.Fields(b)
		if len(toks) < 40 {
			return t, b
		}
		const block = 20
		var blocks [][]string
		for i := 0; i+block <= len(toks); i += block {
			blocks = append(blocks, toks[i:i+block])
		}
		// 保留首尾块，中间块两两交换——模拟洗稿搬运
		for i := 1; i+1 < len(blocks)-1; i += 2 {
			blocks[i], blocks[i+1] = blocks[i+1], blocks[i]
		}
		out := make([]string, 0, len(toks))
		for _, bl := range blocks {
			out = append(out, bl...)
		}
		return t, strings.Join(out, " ")
	}, false},
}

func TestReprintRecall(t *testing.T) {
	corpus := reprintLoadCorpus(t, 8)
	if len(corpus) < 4 {
		t.Skip("样本不足")
	}
	p, err := processor.New()
	if err != nil {
		t.Fatal(err)
	}

	fmt.Printf("%-12s %8s %8s %10s %10s  %s\n", "搬运手法", "召回", "误折", "平均距离", "最大距离", "逐篇")
	for _, rc := range reprintCases {
		fps, dists := reprintBuild(t, p, corpus, rc)
		clusters := Cluster(fps)

		recall, wrong := 0, 0
		for i := range corpus {
			rootOrig := clusterRoot(clusters, i*2)
			rootCopy := clusterRoot(clusters, i*2+1)
			if rootOrig == rootCopy && rootOrig >= 0 {
				recall++
			}
			// 误折：变体 i 被并进了别的原创 j 的簇
			for j := range corpus {
				if j == i {
					continue
				}
				if clusterRoot(clusters, j*2) == rootCopy && rootCopy >= 0 {
					wrong++
					break
				}
			}
		}
		sum, max := 0, 0
		for _, d := range dists {
			sum += d
			if d > max {
				max = d
			}
		}
		fmt.Printf("%-12s %5d/%-2d %8d %10.1f %10d  %v\n",
			rc.name, recall, len(corpus), wrong, float64(sum)/float64(len(dists)), max, dists)

		if rc.mustRecall && recall != len(corpus) {
			t.Errorf("%s：只召回 %d/%d，跨站指纹认不出这种搬运", rc.name, recall, len(corpus))
		}
		if wrong > 0 {
			t.Errorf("%s：误折 %d 篇无关文档", rc.name, wrong)
		}
	}
	fmt.Println("\n（召回=变体与原创同簇；误折=变体被并进别的原创簇）")
}

// reprintBuild 构造「原创 + 搬运变体」的指纹序列，顺序为
// [orig0, copy0, orig1, copy1, ...]。每个原创一个独立域名，
// 保证所有配对都是跨站——本测试只考察跨站 shingle 指纹。
func reprintBuild(t *testing.T, p *processor.Processor, corpus []reprintDoc, rc reprintCase) ([]store.Fingerprint, []int) {
	t.Helper()
	var fps []store.Fingerprint
	var dists []int
	for i, d := range corpus {
		origURL := fmt.Sprintf("https://origin%d.example/article", i)
		origHost := fmt.Sprintf("origin%d.example", i)
		res, err := p.Process(origURL, reprintHTML(d.Title, d.Body), processor.Options{})
		if err != nil {
			t.Fatalf("Process 原创 %d: %v", i, err)
		}
		fps = append(fps, store.Fingerprint{
			URL: origURL, Domain: origHost, Simhash: res.Simhash,
			ShingleHash: res.ShingleHash, WordCount: res.WordCount,
		})

		nt, nb := rc.apply(d.Title, d.Body)
		copyURL := fmt.Sprintf("https://csdn.example/p%d", i)
		res2, err := p.Process(copyURL, reprintHTML(nt, nb), processor.Options{})
		if err != nil {
			t.Fatalf("Process 变体 %d: %v", i, err)
		}
		fps = append(fps, store.Fingerprint{
			URL: copyURL, Domain: "csdn.example", Simhash: res2.Simhash,
			ShingleHash: res2.ShingleHash, WordCount: res2.WordCount,
		})
		dists = append(dists, Distance(res.ShingleHash, res2.ShingleHash))
	}
	return fps, dists
}

func clusterRoot(clusters map[int][]int, idx int) int {
	for root, members := range clusters {
		for _, m := range members {
			if m == idx {
				return root
			}
		}
	}
	return -1
}

func reprintHTML(title, body string) []byte {
	return []byte("<!doctype html><html><head><title>" + title +
		"</title></head><body><article><p>" + body + "</p></article></body></html>")
}

type reprintDoc struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

func reprintLoadCorpus(t *testing.T, n int) []reprintDoc {
	t.Helper()
	f, err := os.Open("../../eval/corpus-real.jsonl")
	if err != nil {
		t.Skip("无 corpus:", err)
	}
	defer f.Close()
	var out []reprintDoc
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var d reprintDoc
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			continue
		}
		// 太短的正文做不出有区分度的 shingle，也搬不出什么内容
		if len([]rune(d.Body)) < 800 {
			continue
		}
		out = append(out, d)
		if len(out) >= n {
			break
		}
	}
	return out
}
