// Package rank 实现可解释排序：
//
//	S = w1·BM25 + w2·精确匹配 + w3·繁荣度 + w4·时效性 + w5·质量分 - p1·采集站惩罚 - p2·重复惩罚
//
// 当前实现覆盖 w1/w2/w3/w4/w5 与「同域重复惩罚」（p2 的域名多样性部分），
// 并输出每条结果的中文排序理由；p1 采集站指纹库需要先积累样本，暂未启用。
//
// 红线：ai_suspect 只做过滤开关，不参与任何降权项。
package rank

import (
	"math"
	"strings"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/urlkey"
)

// Weights 排序权重（实验参数，后续用 Recall@20 / NDCG@10 调参）。
type Weights struct {
	BM25       float64 // w1
	Exact      float64 // w2
	Prosperity float64 // w3
	Freshness  float64 // w4
	Quality    float64 // w5
	// HalfLifeDays 时效性半衰期（天）
	HalfLifeDays float64
	// DomainDecay 同域第 n+1 条结果的乘数（p2 域名多样性）
	DomainDecay float64
}

// DefaultWeights 默认权重，与规划书第四节公式对应。
func DefaultWeights() Weights {
	return Weights{
		BM25:         1.0,
		Exact:        2.0,
		Prosperity:   0.5,
		Freshness:    0.6,
		Quality:      0.4,
		HalfLifeDays: 30,
		// DomainDecay 同域后续结果的衰减系数。
		// sese-engine 的 /8 是针对"同一 URL 树节点的重复内容"去重；
		// 直接套到"同域不同页面"会把真正相关的同站文档（Learn Rust / Install Rust）
		// 压到边缘提及之下（真实语料实测），这里放宽到 0.5。
		DomainDecay: 0.5,
	}
}

// Breakdown 各分值分量（用于调试与可解释展示）。
type Breakdown struct {
	BM25       float64 `json:"bm25"`
	Exact      float64 `json:"exact"`
	Prosperity float64 `json:"prosperity"`
	Freshness  float64 `json:"freshness"`
	Quality    float64 `json:"quality"`
	Diversity  float64 `json:"diversity"`
}

// Result 排序结果。
type Result struct {
	Hit       index.Hit `json:"hit"`
	Score     float64   `json:"score"`
	Breakdown Breakdown `json:"breakdown"`
	Reasons   []string  `json:"reasons"`
}

// Ranker 排序器。
type Ranker struct {
	w Weights
}

// New 构造排序器；零值权重回退默认值。
func New(w Weights) *Ranker {
	if w.BM25 == 0 && w.Exact == 0 && w.Quality == 0 {
		w = DefaultWeights()
	}
	if w.HalfLifeDays <= 0 {
		w.HalfLifeDays = 30
	}
	if w.DomainDecay <= 0 || w.DomainDecay >= 1 {
		w.DomainDecay = 0.5
	}
	return &Ranker{w: w}
}

// Signals 排序所需的外部信号。
type Signals struct {
	// Categories 域名 -> 白名单分类（official/opensource/blog/platform/media）
	Categories map[string]string
	// Prosperity 繁荣度：key 为域名或 host/path（urlkey.Key），由反向链接统计器产出
	Prosperity map[string]float64
}

// Rank 对候选集打分排序。
func (r *Ranker) Rank(query string, hits []index.Hit, sig Signals, now time.Time) []Result {
	if len(hits) == 0 {
		return nil
	}
	maxBM25 := 0.0
	maxProsperity := 0.0
	for _, h := range hits {
		if h.Score > maxBM25 {
			maxBM25 = h.Score
		}
		if p := prosperityOf(h, sig); p > maxProsperity {
			maxProsperity = p
		}
	}

	q := normalize(query)
	qTokens := strings.Fields(q)

	results := make([]Result, 0, len(hits))
	for _, h := range hits {
		b := Breakdown{Diversity: 1}

		if maxBM25 > 0 {
			b.BM25 = h.Score / maxBM25
		}
		b.Exact = exactMatch(q, qTokens, h.Title, h.Description)
		if maxProsperity > 0 {
			b.Prosperity = math.Log1p(prosperityOf(h, sig)) / math.Log1p(maxProsperity)
		}
		b.Freshness = freshness(h.SortTime, r.w.HalfLifeDays, now)
		b.Quality = quality(sig.Categories[h.Domain], h.Description)

		base := r.w.BM25*b.BM25 + r.w.Exact*b.Exact + r.w.Prosperity*b.Prosperity +
			r.w.Freshness*b.Freshness + r.w.Quality*b.Quality
		// 相关性闸门：时效与质量是加性分量，若不加限制，一个只顺带提过关键词的页面
		// 也能靠"新鲜 + 官方站"拿到 1.0 分（真实语料：PostgreSQL About 只提到一次 Rust，
		// BM25=0.005，却排到第二名）。用相关性系数整体缩放，弱相关结果自然沉底。
		rel := math.Max(b.BM25, b.Exact)
		base *= 0.25 + 0.75*rel

		reasons := explain(q, h, b, sig.Categories, now)
		results = append(results, Result{Hit: h, Score: base, Breakdown: b, Reasons: reasons})
	}

	// 同域多样性：按基础分贪心挑选，第 n+1 条同域结果乘 DomainDecay^n
	order := make([]Result, 0, len(results))
	picked := make([]bool, len(results))
	domainCount := map[string]int{}
	for range results {
		best := -1
		bestScore := -1.0
		bestDiv := 1.0
		for i, res := range results {
			if picked[i] {
				continue
			}
			div := math.Pow(r.w.DomainDecay, float64(domainCount[res.Hit.Domain]))
			s := res.Score * div
			if s > bestScore {
				best, bestScore, bestDiv = i, s, div
			}
		}
		if best < 0 {
			break
		}
		picked[best] = true
		res := results[best]
		res.Breakdown.Diversity = bestDiv
		res.Score = bestScore
		if bestDiv < 1 {
			res.Reasons = append(res.Reasons, "同域已有多条结果，按多样性下调")
		}
		domainCount[res.Hit.Domain]++
		order = append(order, res)
	}
	return order
}

// prosperityOf 取繁荣度：优先索引字段，其次按 URL key / 域名查信号表。
func prosperityOf(h index.Hit, sig Signals) float64 {
	if h.Prosperity > 0 {
		return h.Prosperity
	}
	if v, ok := sig.Prosperity[urlkey.Key(h.URL)]; ok {
		return v
	}
	return sig.Prosperity[h.Domain]
}

// exactMatch 精确匹配分：查询词出现在标题 → 1.0；所有词命中标题/摘要 → 0.6。
func exactMatch(q string, qTokens []string, title, desc string) float64 {
	if q == "" {
		return 0
	}
	t := normalize(title)
	d := normalize(desc)
	if strings.Contains(t, q) {
		return 1.0
	}
	if len(qTokens) == 0 {
		return 0
	}
	both := t + " " + d
	for _, tok := range qTokens {
		if len([]rune(tok)) < 2 {
			continue
		}
		if !strings.Contains(both, tok) {
			return 0
		}
	}
	return 0.6
}

// freshness 时效性：半衰期指数衰减；无时间信息给中性分。
func freshness(sortTime int64, halfLifeDays float64, now time.Time) float64 {
	if sortTime <= 0 {
		return 0.3
	}
	ageDays := now.Sub(time.Unix(sortTime, 0)).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	return math.Pow(0.5, ageDays/halfLifeDays)
}

// quality 域名质量分：白名单分类是站长/管理员声明的强信号，摘要完整度做微调。
func quality(category, description string) float64 {
	base := map[string]float64{
		"official":   1.0,
		"opensource": 0.9,
		"blog":       0.75,
		"media":      0.6,
		"platform":   0.5,
	}[category]
	if base == 0 {
		base = 0.5
	}
	if strings.TrimSpace(description) != "" {
		base += 0.15
	}
	if base > 1 {
		base = 1
	}
	return base
}

// explain 生成中文排序理由（结果页直接展示，可解释性核心）。
func explain(q string, h index.Hit, b Breakdown, categories map[string]string, now time.Time) []string {
	var reasons []string
	switch {
	case b.Exact >= 0.99:
		reasons = append(reasons, "标题精确匹配查询词")
	case b.Exact > 0:
		reasons = append(reasons, "关键词全部命中标题/摘要")
	}
	if h.TimeByFetch {
		reasons = append(reasons, "无发布时间，按收录时间计算时效")
	} else if b.Freshness >= 0.5 && h.SortTime > 0 {
		days := int(now.Sub(time.Unix(h.SortTime, 0)).Hours() / 24)
		if days < 0 {
			days = 0
		}
		reasons = append(reasons, "发布时间较新（"+itoa(days)+" 天前）")
	}
	switch categories[h.Domain] {
	case "official":
		reasons = append(reasons, "来自官方文档站点")
	case "opensource":
		reasons = append(reasons, "来自开源项目站点")
	case "platform":
		reasons = append(reasons, "UGC 平台站点（质量分较低）")
	}
	if b.Prosperity > 0 {
		reasons = append(reasons, "反向链接繁荣度较高")
	}
	for _, l := range h.Labels {
		if l == "ai_suspect" {
			// 红线：只提示、不降权，理由必须可解释
			reasons = append(reasons, "含 AI 生成特征（仅过滤开关，不参与降权）")
		}
	}
	return reasons
}

func normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
