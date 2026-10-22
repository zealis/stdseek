package processor

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// 标签常量：与规划书功能 G 的四个排除开关一一对应。
const (
	LabelAISuspect   = "ai_suspect"
	LabelAdHeavy     = "ad_heavy"
	LabelPersonal    = "personal"
	LabelUGCPlatform = "ugc_platform"
)

// 平台域名清单（功能 G `no 平台`），与采集站指纹库共用数据；一期用内置清单，
// 后续由后台维护（规划书：平台域名清单 + 采集站指纹库）。
var ugcPlatforms = []string{
	"csdn.net", "zhihu.com", "jianshu.com", "juejin.cn", "cnblogs.com",
	"segmentfault.com", "v2ex.com", "51cto.com", "oschina.net", "gitee.com",
	"sohu.com", "163.com", "medium.com", "dev.to", "stackoverflow.com",
}

// adPattern 广告位 DOM 特征：class/id 命中即计一个广告节点。
var adPattern = regexp.MustCompile(`(?i)(^|[-_])(ad|ads|advert|adsbygoogle|sponsor|promo|guanggao)([-_]|$)`)

// affiliateHosts 联盟/推广链接域名（广告判定的第二信号）。
var affiliateHosts = []string{"s.click.taobao.com", "union-click.jd.com", "u.jd.com", "affiliate."}

// aiMarkers AI 生成文本的自我指涉与套话特征（可解释信号之一）。
var aiMarkers = []string{
	"作为ai", "作为一个ai", "作为人工智能", "以下是", "综上所述", "总的来说",
	"希望对你有帮助", "需要注意的是", "首先，", "其次，", "最后，", "总而言之",
}

// labelInput 标签判定输入。
type labelInput struct {
	URL          *url.URL
	Title        string
	Body         string
	RawHTML      string
	SiteCategory string // 站长/管理员提交时的分类：official/opensource/blog/platform/media
}

// labels 返回命中标签与可解释理由。
// 红线：ai_suspect 只做过滤开关、不做排序降权；每个标签都必须给出理由（可解释 + 误判反馈依据）。
func computeLabels(in labelInput) ([]string, map[string]string) {
	labels := []string{}
	reasons := map[string]string{}

	host := strings.ToLower(in.URL.Hostname())

	// 1) ugc_platform：域名命中平台清单
	for _, p := range ugcPlatforms {
		if host == p || strings.HasSuffix(host, "."+p) {
			labels = append(labels, LabelUGCPlatform)
			reasons[LabelUGCPlatform] = fmt.Sprintf("域名属于 UGC 平台清单：%s", p)
			break
		}
	}

	// 2) personal：站长分类为个人博客，或域名特征
	var personalSignals []string
	if in.SiteCategory == "blog" {
		personalSignals = append(personalSignals, "站长提交分类为个人博客")
	}
	if strings.HasPrefix(host, "blog.") || strings.HasSuffix(host, ".me") || strings.HasSuffix(host, ".blog") {
		personalSignals = append(personalSignals, "域名特征（blog./.me/.blog）")
	}
	if len(personalSignals) > 0 {
		labels = append(labels, LabelPersonal)
		reasons[LabelPersonal] = strings.Join(personalSignals, "；")
	}

	// 3) ad_heavy：广告位 DOM 占比 + 联盟链接计数
	if in.RawHTML != "" {
		if n, total, affiliate := adStats(in.RawHTML); n > 0 || affiliate > 0 {
			ratio := 0.0
			if total > 0 {
				ratio = float64(n) / float64(total)
			}
			// 阈值：广告节点 ≥3 且占比 ≥2%，或联盟链接 ≥2 条
			if (n >= 3 && ratio >= 0.02) || affiliate >= 2 {
				labels = append(labels, LabelAdHeavy)
				reasons[LabelAdHeavy] = fmt.Sprintf("广告位节点 %d/%d（%.1f%%），联盟链接 %d 条",
					n, total, ratio*100, affiliate)
			}
		}
	}

	// 4) ai_suspect：多信号才判定（保守策略，降低误伤独立作者）
	var aiSignals []string
	if regexp.MustCompile(`(?i)<meta[^>]+name=["']generator["'][^>]+content=["'][^"']*(gpt|ai|llm|claude)[^"']*`).MatchString(in.RawHTML) {
		aiSignals = append(aiSignals, "meta generator 声明 AI 生成")
	}
	text := strings.ToLower(in.Title + " " + in.Body)
	hits := 0
	for _, m := range aiMarkers {
		if strings.Contains(text, m) {
			hits++
		}
	}
	if hits >= 2 {
		aiSignals = append(aiSignals, fmt.Sprintf("套话特征命中 %d 处", hits))
	}
	if burst := burstiness(in.Body); burst > 0 && burst < 0.35 {
		aiSignals = append(aiSignals, fmt.Sprintf("句长方差比过低（%.2f）", burst))
	}
	if len(aiSignals) >= 2 {
		labels = append(labels, LabelAISuspect)
		reasons[LabelAISuspect] = strings.Join(aiSignals, "；")
	}

	return labels, reasons
}

// adStats 统计广告位节点数、总节点数、联盟链接数。
func adStats(rawHTML string) (adNodes, totalNodes, affiliate int) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rawHTML))
	if err != nil {
		return 0, 0, 0
	}
	doc.Find("*").Each(func(_ int, s *goquery.Selection) {
		totalNodes++
		cls, _ := s.Attr("class")
		id, _ := s.Attr("id")
		if adPattern.MatchString(cls) || adPattern.MatchString(id) {
			adNodes++
		}
		if s.Is("a[href]") {
			href, _ := s.Attr("href")
			for _, h := range affiliateHosts {
				if strings.Contains(href, h) {
					affiliate++
					break
				}
			}
		}
	})
	return adNodes, totalNodes, affiliate
}

// burstiness 句长方差比（标准差/均值）。AI 文本句长更均匀，值偏低。
func burstiness(text string) float64 {
	sentences := splitSentences(text)
	if len(sentences) < 20 {
		return 0 // 样本不足，不作判定
	}
	var sum float64
	for _, s := range sentences {
		sum += float64(len([]rune(s)))
	}
	mean := sum / float64(len(sentences))
	if mean == 0 {
		return 0
	}
	var sq float64
	for _, s := range sentences {
		d := float64(len([]rune(s))) - mean
		sq += d * d
	}
	return math.Sqrt(sq/float64(len(sentences))) / mean
}

var sentenceEnd = regexp.MustCompile(`[。！？!?；;\n]+`)

func splitSentences(text string) []string {
	parts := sentenceEnd.Split(text, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}
