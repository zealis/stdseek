// Package labels 是页级标签的唯一词表。
//
// 为什么单独拿出来：标签有两个来源——索引期的规则判定（processor/labels.go）
// 和 AI 站点分类时的顺带判定（classify）。两边各写一份 key 就会漂移，
// 症状是「AI 打了标却筛不出来」，很难查。
//
// 注意标签与分类是两种东西：分类是「这站是干嘛的」（站点级、正选），
// 标签是「这页怎么样」（页级、只用于排除）。所以标签词表不放在 sitecat 里。
package labels

const (
	AISuspect   = "ai_suspect"   // 疑似 AI 生成
	AdHeavy     = "ad_heavy"     // 广告较多
	Personal    = "personal"     // 独立网站
	UGCPlatform = "ugc_platform" // UGC 平台
)

// Label 一条标签说明。Hint 只给模型看，界面不展示。
type Label struct {
	Key  string
	Text string
	Hint string
}

// All 全部标签，顺序即展示顺序。
var All = []Label{
	{AISuspect, "AI 生成", "整站内容像是模板化批量生成：套话多、无具体案例与出处"},
	{AdHeavy, "广告较多", "正文里广告位、推广链接、联盟链接占比明显偏高"},
	{Personal, "独立网站", "个人博客、个人主页这类由个人维护的站点"},
	{UGCPlatform, "UGC 平台", "内容主要由用户发帖产生：知乎、CSDN、掘金这类平台"},
}

var byKey = func() map[string]Label {
	m := make(map[string]Label, len(All))
	for _, l := range All {
		m[l.Key] = l
	}
	return m
}()

// Valid 是否是已知标签。
func Valid(key string) bool {
	_, ok := byKey[key]
	return ok
}

// Text 中文名；未知 key 原样返回。
func Text(key string) string {
	if l, ok := byKey[key]; ok {
		return l.Text
	}
	return key
}

// Prompt 给模型的标签清单：一行一个 key，带上判定说明。
func Prompt() string {
	out := ""
	for _, l := range All {
		out += "- " + l.Key + "：" + l.Text + "（" + l.Hint + "）\n"
	}
	return out
}

// SiteReason 站点级标签没有内容侧的理由，统一给一句可解释的说明。
const SiteReason = "整站判定：该站点整体属于这一类"

// Merge 把站点级标签并进页级标签。
//
// 页级标签（processor 按这一页的内容判）在前，站点级（AI/人工按整站判）在后，去重。
// 站点级标签要补一条理由：界面上每个标签都能解释「为什么挂它」，缺理由的那条
// tooltip 会显得像 bug。返回的 reasons 是新副本，不改调用方原 map。
func Merge(page []string, pageReasons map[string]string, site []string) ([]string, map[string]string) {
	if len(site) == 0 {
		return page, pageReasons
	}
	out := make([]string, 0, len(page)+len(site))
	have := make(map[string]bool, len(page)+len(site))
	for _, l := range page {
		if have[l] {
			continue
		}
		have[l] = true
		out = append(out, l)
	}
	reasons := make(map[string]string, len(pageReasons)+len(site))
	for k, v := range pageReasons {
		reasons[k] = v
	}
	for _, l := range site {
		if l == "" || have[l] {
			continue
		}
		have[l] = true
		out = append(out, l)
		if reasons[l] == "" {
			reasons[l] = SiteReason
		}
	}
	return out, reasons
}
