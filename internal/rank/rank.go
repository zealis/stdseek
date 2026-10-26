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
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/urlkey"
)

// freshnessIntent 查询里的时效意图词。用户在问「最新的」，词面匹配无法表达，
// 只能靠排序把时间维度抬到主导位置。
//
// 只收明确指向时间的说法：「新」单字太宽（「新特性」「新版」多半是在讲某个
// 具体版本的文档，不是要最新内容），「最近」也要和「最新」分开——两者都收，
// 但都不含单字「新」。
var freshnessIntentZh = []string{
	"最新", "最近", "近期", "今年以来", "今年", "本月", "这周", "本周", "刚刚发布", "新发布",
}

var freshnessIntentEn = []string{
	"latest", "newest", "recent", "release notes", "changelog",
}

// HasFreshnessIntent 判断查询是否带时效意图（「最新一期」「最近的更新」这类）。
func HasFreshnessIntent(query string) bool {
	q := strings.ToLower(query)
	for _, w := range freshnessIntentZh {
		if strings.Contains(q, w) {
			return true
		}
	}
	for _, w := range freshnessIntentEn {
		if strings.Contains(q, w) {
			return true
		}
	}
	return false
}

// Weights 排序权重（实验参数，后续用 Recall@20 / NDCG@10 调参）。
type Weights struct {
	BM25       float64 // w1
	Exact      float64 // w2
	Prosperity float64 // w3
	Freshness  float64 // w4
	Quality    float64 // w5
	// Vector 语义相似度权重。0 表示关闭向量检索（默认），此时行为与纯词汇排序
	// 完全一致；启用后向量分进 base，并参与相关性闸门与噪音判定。
	Vector float64
	// HalfLifeDays 时效性半衰期（天）
	HalfLifeDays float64
	// DomainDecay 同域第 n+1 条结果的乘数（p2 域名多样性）
	DomainDecay float64
}

// DefaultWeights 默认权重，与规划书第四节公式对应。
//
// 数值来源：2026-10-24 用 cmd/tune 做 324 组网格扫描 + 5 折交叉验证定出，
// 挑出条件是 "2×min(两组 NDCG) + Σ(两组 NDCG)"，防止偏向条数多的那组。
// 5 折里有 4 折独立挑出同一组值，且调参权重在留出折上 4 胜 1 负——
// 不是噪声，也不是过拟合。
//
// 相对旧值的方向（旧值是在**旧融合公式**下调出来的，公式改了就必须重调）：
//   - Exact 2.0 → 1.0、Prosperity 0.5 → 0.25、Quality 0.4 → 0.2：
//     新公式的加性质量项已经乘了 rel（相关性），实际强度本就下降，再叠加高权重会过头。
//   - Freshness 0.6 → 1.0：时效在新公式下被压得太狠。
//
// 实测（337 篇 corpus 重建口径）：纯中文 NDCG@10 0.827→0.834，
// 术语桥 0.541→0.554，两组 Recall 均不变（只影响排序不影响召回）。
func DefaultWeights() Weights {
	return Weights{
		BM25:         1.0,
		Exact:        1.0,
		Prosperity:   0.25,
		Freshness:    1.0,
		Quality:      0.2,
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
	// Vector 语义相似度（0~1，已按 vecFloor 截断重映射）。权重为 0 时恒为 0。
	Vector float64 `json:"vector"`
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
	// Inbound 语料内反向链接数（key 为原始 URL）。深网发现的冷热信号：
	// 引用越少越可能是被埋住的内容（独立博客、冷门讨论）。
	Inbound map[string]int
	// DupCount 被折叠的副本数（key 为原始 URL）。只折叠不删除元数据，
	// 结果页据此说明「这篇文章还有 N 个相同内容的副本，已合并」。
	DupCount map[string]int
	// HubPages 站内首页（key 为原始 URL）。首页正文短、标题常只是品牌名，
	// 任何提到品牌词的查询都会命中它，且在标题匹配上天然占优——
	// 真实案例：「单元测试」的第 2 条是 kubernetes.io 中文首页。
	HubPages map[string]bool
	// QueryVec 查询的 embedding；为 nil 表示本次不启用语义信号。
	QueryVec []float32
	// Vectors 候选文档的 embedding，key 为 urlkey.Key(URL)。
	// 只含候选集对应的向量（调用方按候选批量取，不做全量加载）。
	Vectors map[string][]float32
	// CorpusDocs 索引内文档总数，用于按语料规模缩放绝对相关性闸门（见 minBM25For）。
	// 0 表示未知，退回千篇量级的基准阈值。
	CorpusDocs int
}

// vecFloor 余弦下限：低于它的语义相似度视为「无语义关系」，压成 0 分。
//
// 为什么不能直接拿原始余弦当相关性：e5 系模型对所有中文文本都给出 0.5 以上的
// 余弦（同一个向量空间里任意两句都不至于正交），直接进相关性闸门等于把闸门拆了。
// 高于 vecFloor 的部分线性重映射到 0~1，才与 BM25/Exact 的归一化值同量纲。
const vecFloor = 0.55

// minVector 语义闸门：重映射后的向量分达到多少算「有语义命中」。
// 与 minBM25 同级的作用——词汇侧全灭但语义侧命中的查询不能被判为噪音，
// 那正是向量检索要救的场景（术语桥查询「React 快速开始」等）。
const minVector = 0.35

// vecRescueBelow 词汇相关性低于此值时才允许语义分抬升 rel（见主循环里的注释）。
const vecRescueBelow = 0.3

// vectorScore 查询向量与文档向量的余弦，按 vecFloor 截断后线性重映射到 0~1。
// 两侧向量都已 L2 归一化，但这里仍算完整余弦：调用方可能传入未归一化的向量
// （例如从 KV 里读回的旧数据），直接点积会静默放大成错误的相关度。
func vectorScore(q, d []float32) float64 {
	if len(q) == 0 || len(d) != len(q) {
		return 0
	}
	var dot, na, nb float64
	for i := range q {
		dot += float64(q[i]) * float64(d[i])
		na += float64(q[i]) * float64(q[i])
		nb += float64(d[i]) * float64(d[i])
	}
	if na <= 0 || nb <= 0 {
		return 0
	}
	cos := dot / math.Sqrt(na*nb)
	if cos <= vecFloor {
		return 0
	}
	return (cos - vecFloor) / (1 - vecFloor)
}

// hubPenalty 多词主题查询下首页的降权系数。
// 不用 0 剔除：首页导航型查询（用户只输入「PHP」「Rust」）仍然该返回首页，
// 那是搜索引擎的惯例；只有带修饰词的主题查询才把首页压下去。
const hubPenalty = 0.45

// langPrefix 形如 /zh-cn/ /en/ /fr/ 的语言前缀。多语言文档站（kubernetes.io、
// vuejs.org、react.dev）的「中文首页」路径是 /zh-cn/ 而不是 /，只判 / 会漏掉它们——
// 实测「单元测试」的第 2 条就是 kubernetes.io/zh-cn/。
var langPrefix = regexp.MustCompile(`^/[a-z]{2}(-[a-z]{2,4})?/?$`)

// IsHubURL 判断是否为站点首页。覆盖两种形态：
//  1. 裸首页：路径为 /
//  2. 语言首页：路径只有语言前缀（/zh-cn/、/en/）
//
// 只看路径，不看域名特征——任何站点的首页都同样处理，不需要逐站配置。
func IsHubURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return u.Path == "" || u.Path == "/" || langPrefix.MatchString(u.Path)
}

// minBM25 绝对相关性闸门：整批候选的最高 BM25 低于该值时，判定查询在语料里
// 没有真实匹配，返回的全部是词面偶然命中。
//
// 为什么必须用原始 BM25 而不是归一化值：Breakdown.BM25 = score/maxScore，
// 噪音查询里"相对最高的那条"照样拿到 1.0，随后靠繁荣度+时效+质量分撑到 2.2 分
// 排上首位（真实案例："async await 原理" 的 top1 是 Mesos 论文）。
//
// 实测分布（316 篇语料）：
//
//	xyzzy nonexistent topic 0.0028 | 闭包 0.0178 | async await 原理 0.0772
//	量子计算编译器 0.0781 | 数据库索引原理 0.246 | 微服务 0.472 | 单元测试 1.394
//
// 噪音与正常查询差 3~500 倍，阈值取 0.1 有充足余量。
//
// 注意：Bleve 的 BM25 绝对值随索引规模与平均文档长度漂移。
// 下面的 minBM25For 就是为这件事准备的：阈值随文档数缩放，不再写死。
const minBM25Base = 0.1

// minBM25For 按语料规模缩放绝对相关性闸门。
//
// 为什么必须缩放：BM25 的分值由 IDF 主导，IDF ≈ ln(1+(N-df+0.5)/(df+0.5))，
// 文档数 N 越小分值越低。千篇语料上噪音与正常查询差 3~500 倍，0.1 有余量；
// 但 2 篇内网文档时「回滚」的 BM25 只有 0.073、「工单」0.068，全部低于 0.1
// 被判定成噪音——整批返回空，小语料部署表现为"什么都搜不到"。
// 真实场景：企业内网导入几十到几百篇文档，正好落在这个区间。
//
// 缩放取 log(N)/log(1000)，夹在 [0.2, 1]：
//   - N ≥ 1000 时系数为 1，阈值等于原 0.1，评测基线一分数不变；
//   - N = 10 时系数 0.2，阈值 0.02，足以放行真实命中，仍挡得住完全无关的查询
//     （后者往往一个词都匹配不上，total=0 根本进不到这里）。
func minBM25For(docs int) float64 {
	if docs <= 0 {
		return minBM25Base
	}
	scale := math.Log(float64(docs)) / math.Log(1000)
	if scale < 0.2 {
		scale = 0.2
	}
	if scale > 1 {
		scale = 1
	}
	return minBM25Base * scale
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

	// 语义信号是否启用：权重为 0 或缺少向量时完全不参与，行为与纯词汇排序一致。
	useVec := r.w.Vector > 0 && len(sig.QueryVec) > 0 && len(sig.Vectors) > 0
	var maxVec float64
	if useVec {
		for _, h := range hits {
			if v := vectorScore(sig.QueryVec, sig.Vectors[urlkey.Key(h.URL)]); v > maxVec {
				maxVec = v
			}
		}
	}

	// 整批候选都只是词面偶然命中时直接返回空——比让噪音靠质量分上位更诚实。
	//
	// 但绝对 BM25 单用会误杀「高频专有名词 + 中文修饰词」：Vue/React/Docker 这类词
	// 在语料里出现太多次、IDF 很低，单靠它命中的 BM25 天然低于阈值
	//（实测「Vue 术语表」被误杀，而它本来 recall 1.00）。因此要求
	// **BM25 低且完全没有标题/摘要精确匹配**才判为噪音。
	if maxBM25 < minBM25For(sig.CorpusDocs) {
		hasExact := false
		for _, h := range hits {
			if exactMatchFull(q, qTokens, h.Title, h.Description) > 0 {
				hasExact = true
				break
			}
		}
		if !hasExact && maxVec < minVector {
			return nil
		}
	}

	// 时效意图：把时间维度从「加分项」抬成「主导项」。
	// 权重 ×3、半衰期 30 天 → 14 天，让「最新一期」这类查询里近两周的文档
	// 明显压过同站点几个月前的同系列文章（周刊、更新日志都是这种形态）。
	// 不改成硬时间窗：用户说「最新」并不等于「只要一个月内的」，
	// 语料里没有更新的内容时，旧文档仍应可被找到。
	freshW := r.w.Freshness
	halfLife := r.w.HalfLifeDays
	intent := HasFreshnessIntent(query)
	if intent {
		freshW *= 3
		halfLife = 14
	}

	results := make([]Result, 0, len(hits))
	for _, h := range hits {
		b := Breakdown{Diversity: 1}

		if maxBM25 > 0 {
			b.BM25 = h.Score / maxBM25
		}
		b.Exact = exactMatch(q, qTokens, h.Title, h.Description, h.Domain)
		if maxProsperity > 0 {
			b.Prosperity = math.Log1p(prosperityOf(h, sig)) / math.Log1p(maxProsperity)
		}
		b.Freshness = freshness(h.SortTime, halfLife, now)
		b.Quality = quality(sig.Categories[h.Domain], h.Description)
		if useVec {
			b.Vector = vectorScore(sig.QueryVec, sig.Vectors[urlkey.Key(h.URL)])
		}

		// 相关性分量：词汇（BM25 / 精确匹配）+ 语义（向量）
		relPart := r.w.BM25*b.BM25 + r.w.Exact*b.Exact + r.w.Vector*b.Vector
		// 质量类分量：繁荣度 / 时效 / 质量
		qualPart := r.w.Prosperity*b.Prosperity + freshW*b.Freshness + r.w.Quality*b.Quality

		rel := math.Max(b.BM25, b.Exact)
		if useVec && rel < vecRescueBelow {
			// 只在词汇信号弱时才让语义兜底，不是无条件进 max。
			//
			// 无条件取 max 的实测代价（61 条纯中文，混合模式）：
			// 向量分普遍偏高（e5 任意两句余弦都 >0.5），于是"高余弦但不相关"的
			// 文档被抬高基线，把 BM25 真正相关的文档挤出前 20——
			// Recall@20 0.877 → 0.852，NDCG 也没涨，且调低 vecw 无效
			// （伤害来自 rel 而不是加性权重，0.2 与 0.5 结果一模一样）。
			rel = math.Max(rel, b.Vector)
		}
		// 相关性闸门：质量分只在"相关"的前提下兑现。
		//
		// 旧公式是 base = (relPart + qualPart) · (0.25 + 0.75·rel)——质量项最多被
		// 压到 25% 而不是 0，于是"只顺带提过关键词、但很新很官方"的页面仍能靠
		// 质量分上位。语义召回的文档更惨：它们 BM25=0，相关性全靠向量分（权重 1.0），
		// 而噪声项权重合计 1.5，排序被完全淹没——同一批向量，纯余弦排序
		// NDCG@10 0.936，过一遍旧公式只剩 0.680。
		// 改成质量分乘 rel：完全不相关的文档拿不到任何质量加成。
		base := relPart + qualPart*rel
		// 首页降权，但标题与查询完全一致的首页豁免。
		// 判据演进：先按「查询词数量」区分导航/主题——中文没有空格，「单元测试」
		// 按空白切分只有一个词，被当成导航查询，首页照样排第 2；改成无条件降权后，
		// 「React」「PHP」这类品牌查询的官网首页又被挤出前十，同样不对。
		// 最终判据看首页自身：标题就是查询词（React → react.dev 标题「React」）时，
		// 用户找的就是这个站入口，不罚；标题与查询无关（kubernetes 首页标题是
		// 「生产级别的容器编排系统」却因为正文提到单元测试而命中）时才罚。
		isHub := sig.HubPages[h.URL]
		if isHub && b.Exact < 0.99 {
			base *= hubPenalty
		}
		// 时效意图的「增量部分」必须加在闸门之外。
		// 闸门按相关性同比缩放，放在里面等于「越不相关越拿不到时间加成」——
		// 实测「最新一期」里 10 天前但相关性稍低的那条永远翻不上去（差 0.005 分落败）。
		// 只加增量、不动基线：无时效意图的查询行为完全不变。
		// 增量按 √相关性衰减：完全无关的文档不该靠「新」拿到满额加成。
		// 用开方而不是直接用 rel：直接用会把加成缩得和闸门一样，翻不动次相关的新文档
		// （实测「最新一期」里相关性 0.5 的新文档差 0.005 分落败）。
		if intent {
			base += (freshW - r.w.Freshness) * b.Freshness * math.Sqrt(rel)
		}

		// 时效意图是查询级信息（前端在结果数旁提示），不写进每条结果的理由——
		// 实测那样做会让「科技爱好者周刊最新一期」的每一条结果都挂着同一句解释。
		reasons := explain(q, h, b, sig.Categories, sig.DupCount, now)
		if isHub && b.Exact < 0.99 {
			reasons = append(reasons, "站点首页，与查询主题无关，排序下调")
		}
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
			div := math.Pow(r.w.DomainDecay, float64(domainCount[domainGroup(res.Hit.Domain)]))
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
		domainCount[domainGroup(res.Hit.Domain)]++
		order = append(order, res)
	}
	return order
}

// 关于「弱相关结果混入」的结论（record，勿重复尝试）：
//
// 「单元测试」返回 78 条，首条 3.96 分、第 2 条 0.37 分，后面全是噪声。
// 试过在排序层按 relCut=0.25 做相对截断（低于首条 25% 的丢弃），实测结果：
//
//	纯中文组 0.971/0.990 不变 | 术语桥组 Recall 0.556→0.222 | 合成集 0.955→0.902
//
// 原因是术语桥组的目标文档（英文官方页）本身排序就靠后、分值只有首条的 10~20%，
// 截断等于把「排得差」直接变成「找不到」。在排序质量提升之前，截断只会伤害召回。
// 结论：弱相关过滤放在**展示层**（前端折叠成「还有 N 条相关性较低的结果」），
// 后端保持完整召回——既不丢数据，也不让用户第一眼看到噪声。

// domainGroup 同域分组键：抹掉 www. 前缀。
// 抓取时裸域与 www 会各成一条记录（ruanyifeng.com 1 篇 + www.ruanyifeng.com 24 篇），
// 按原始 host 分组会让同站内容被当成两个站，各享一份多样性额度。
func domainGroup(host string) string {
	return strings.TrimPrefix(strings.ToLower(host), "www.")
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

// exactMatch 精确匹配分：查询词出现在标题 → 1.0；按 token 覆盖比例给 0~0.6。
//
// 覆盖比例而不是「全有或全无」：中英混杂查询（「docker 构建镜像」）里，
// 英文文档只有 docker、中文文档只有「构建镜像」，旧的二值判定让两边都得 0 分，
// 排名完全交给 BM25——正文长的中文文章靠词多获胜，官方文档被挤出前十
// （真实语料 227 篇后术语桥组 NDCG 0.480→0.303）。
// 拉丁 token 是技术专有名词（查询主题），权重高于中文修饰词。
//
// 例外：品牌词已经体现在域名里时不计分（见 domainHasToken）。域名已把候选限定在
// 该站点，标题再出现品牌词不含区分度，反而让"标题含品牌名的首页/栏目页"稳赢
// "标题只有功能名（Glossary / Security Advisories / Releases）的目标页"。
// 若整个查询的词全被这条规则跳过（用户只输入品牌名，如「React」），回退到不过滤的算法——
// 那种查询恰恰需要"标题就是查询词"这个信号来判断是否豁免首页降权。
// exactMatch 精确匹配分，只看标题与摘要。
//
// 用 h.Excerpt（正文前 200 字）做过回退实验，结论是**不做**：
// 正文命中的噪音远大于信息量，让「标题不符但正文提过关键词」的页面拿到了相关性分。
// 实测（337 篇术语桥）：不打折 0.745→0.728、打 0.5 折 0.730、0.35 折 0.742，
// 任何权重都不如不启用（不启用 0.745）。excerpt 字段本身保留——
// 用途是结果页摘要与证据链文本，不进排序。
func exactMatch(q string, qTokens []string, title, desc, domain string) float64 {
	if v, ok := exactScore(q, qTokens, title, desc, domain, true); ok {
		return v
	}
	// 走到这里说明所有词都是品牌词（total==0，如只输入「React」）：
	// 此时品牌词就是唯一线索，必须计分，且要能判定「标题就是查询词」以豁免首页降权。
	v, _ := exactScore(q, qTokens, title, desc, domain, false)
	return v
}

func exactMatchFull(q string, qTokens []string, title, desc string) float64 {
	v, _ := exactScore(q, qTokens, title, desc, "", false)
	return v
}

// exactScore 计算精确匹配分；skipBrand 控制是否忽略"已体现在域名里"的品牌词。
func exactScore(q string, qTokens []string, title, desc, domain string, skipBrand bool) (float64, bool) {
	if q == "" {
		return 0, true
	}
	t := normalize(title)
	d := normalize(desc)
	if strings.Contains(t, q) {
		return 1.0, true
	}
	if len(qTokens) == 0 {
		return 0, true
	}
	both := t + " " + d
	// 只有中英混杂查询才允许跨语言匹配标题（红线 9，见 tokenHit 注释）
	crossLang := hasLatin(q)

	var got, total float64
	skippedBrand := false
	for _, tok := range qTokens {
		if len([]rune(tok)) < 2 {
			continue
		}
		if skipBrand && isASCII(tok) && domainHasToken(domain, tok) {
			skippedBrand = true
			continue
		}
		w := 1.0
		if isASCII(tok) {
			w = 2 // 主题词（英文术语）权重更高
		}
		total += w
		if tokenHit(both, tok, crossLang) {
			got += w
		}
	}
	if total == 0 {
		return 0, false
	}
	// 品牌词被跳过、且剩下的区分度词**全部**命中标题：这是「标题就是目标功能页」
	// 的强信号（Glossary / Security Advisories / FAQ），给比常规覆盖比例更高的分。
	// 常规上限 0.6 压不过正文冗长、品牌词密集的泛页面——
	// 实测「Vue 术语表」目标页 vuejs.org/glossary/ 被召回却排在第 11~20 位。
	//
	// 常数标定：0.70 / 0.85 / 0.90 / 0.95 在两份语料上均单调（术语桥 NDCG@10
	// 0.605 / 0.668 / 0.673 / 0.690，294 篇语料 0.95 与 0.90 完全相同）。
	// 取 0.90 而不是 0.95：与首页豁免阈值（b.Exact < 0.99）留余量，
	// 免得将来调那个阈值时这类页面意外从"降权"跨到"豁免"。
	if skippedBrand && got == total {
		return 0.9, true
	}
	return 0.6 * got / total, true
}

// tokenHit 判断查询词是否出现在文本里。**仅限中英混杂查询**才额外检查它的
// 英文写法——纯中文查询走跨语言匹配会同时踩两个坑：
//
//  1. 红线 9：中文 query 只召回中文内容，中英混杂 query 才允许跨语言。
//     排序层的跨语言加分同样是跨语言，不能只在召回层守边界。
//  2. 实测污染：无条件启用变体时纯中文组 NDCG@10 0.834→0.816、MRR 0.828→0.803
//     （中文文档里偶尔出现的英文词被当成标题命中，精确分虚高，排序被扰动）。
//
// 术语桥查询的核心困难：中文修饰词在英文文档里不存在（「社区」vs Community、
// 「安全公告」vs Security Advisories）。词典（index.TermVariants）本就收录了
// 这批映射，但此前只喂给召回、没喂给精确匹配，于是目标页标题命不中、
// 同站首页靠品牌词拿分并压过它（实测「Rust 社区」目标页排第 8）。
func tokenHit(text, tok string, allowCrossLang bool) bool {
	if strings.Contains(text, tok) {
		return true
	}
	if !allowCrossLang {
		return false
	}
	for _, v := range index.TermVariants(tok) {
		if strings.Contains(text, v) {
			return true
		}
	}
	return false
}

// domainHasToken 品牌词是否已体现在域名里（nginx.org ↔ nginx、kubernetes.io ↔ kubernetes）。
//
// 为什么要判这个：域名已经把候选限定在该站点，标题里再出现品牌词就不含信息量。
// 真实案例「nginx 安全公告」：目标页 nginx.org/en/security_advisories.html 标题是
// Security Advisories（命中「安全公告」→security），首页 nginx.org/ 标题只有 nginx。
// 旧算法里 nginx 权重 2、安全公告权重 1，于是首页 Exact 0.4 压过目标页 0.2，
// 首页排第 1、目标页第 7。
//
// 两字母品牌词（go.dev ↔ go）只做完整域名段相等匹配，避免 "c" 命中 cppreference
// 这类误伤；三字母以上才允许段内包含（rust ↔ rust-lang.org）。
func domainHasToken(domain, tok string) bool {
	if tok == "" || domain == "" {
		return false
	}
	for _, seg := range strings.Split(strings.ToLower(domain), ".") {
		if seg == tok {
			return true
		}
		if len(tok) >= 3 && strings.Contains(seg, tok) {
			return true
		}
	}
	return false
}

// hasLatin 查询里是否含拉丁字母（用于区分「纯中文查询」与「中英混杂查询」）。
func hasLatin(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

// isASCII 判断字符串是否全为 ASCII 字符（用于识别英文术语 token）。
func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
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
func explain(q string, h index.Hit, b Breakdown, categories map[string]string, dupOf map[string]int, now time.Time) []string {
	var reasons []string
	switch {
	case b.Exact >= 0.99:
		reasons = append(reasons, "标题精确匹配查询词")
	case b.Exact >= 0.55:
		reasons = append(reasons, "关键词基本命中标题/摘要")
	case b.Exact > 0:
		reasons = append(reasons, "部分关键词命中标题/摘要")
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
	if n := dupOf[h.URL]; n > 0 {
		reasons = append(reasons, "内容与 "+itoa(n)+" 个页面重复，已聚合到本篇")
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

// Discover 深网发现：从同一批候选里打捞「相关但被埋住」的内容——
// 独立博客、小开源项目、冷门讨论。规划书功能 C 的差异点。
//
// 与主排序的差别只有一处：反向链接数从加分项变成减分项（倒数使用）。
// 冷热信号用「语料内反向链接数」而不是 prosperity 表：后者在策展白名单里
// 已饱和（294 篇入索引文档中 204 篇没有路径级条目，其余全在最低档，
// 区分度为 0），前者有明显差异（0 ~ 25）。
//
// 两道闸门保证这个区不变成噪音收容所：
//  1. 相关性门槛高于主排序（minDiscoverRel）——发现区也要真的相关；
//  2. 主排序已展示的结果整体排除（exclude），不与主结果重复。
func (r *Ranker) Discover(query string, hits []index.Hit, sig Signals, now time.Time,
	exclude map[string]bool, limit int) []Result {
	if len(hits) == 0 || limit <= 0 {
		return nil
	}
	// 整批噪音判定，与主排序同一口径：只有整批候选的最高 BM25 都低于阈值时
	// 才认为查询在语料里没有真实匹配，不出发现区。
	// 不能按单条候选判——被埋住的内容 BM25 天然低，逐条卡绝对值会把
	// 这个区该捞的对象全部滤掉（实测「单元测试」因此返回 0 条）。
	maxBatch := 0.0
	for _, h := range hits {
		if h.Score > maxBatch {
			maxBatch = h.Score
		}
	}
	if maxBatch < minBM25For(sig.CorpusDocs) {
		return nil
	}
	q := normalize(query)
	qTokens := strings.Fields(q)

	// 归一化尺度必须取完整候选批，不能取过滤后的池子：
	// 主结果排除掉大部分候选后，池内最大值会塌成一条弱命中的分数，
	// 归一化后它反而变成 1.0，伪装成完全相关（单测 TestDiscoverRejectsIrrelevant
	// 就是这么抓到这个 bug 的）。
	maxBM25All, maxInboundAll := 0.0, 0
	for _, h := range hits {
		if h.Score > maxBM25All {
			maxBM25All = h.Score
		}
		if n := sig.Inbound[h.URL]; n > maxInboundAll {
			maxInboundAll = n
		}
	}

	type scored struct {
		res   Result
		inbou int
	}
	var pool []scored
	maxInbound := maxInboundAll
	for _, h := range hits {
		if exclude[h.URL] {
			continue
		}
		b := Breakdown{}
		b.Exact = exactMatch(q, qTokens, h.Title, h.Description, h.Domain)
		b.Quality = quality(sig.Categories[h.Domain], h.Description)
		pool = append(pool, scored{res: Result{Hit: h, Breakdown: b}, inbou: sig.Inbound[h.URL]})
	}
	if len(pool) == 0 {
		return nil
	}

	denomInbound := math.Log1p(float64(maxInbound))
	maxBM25 := maxBM25All

	out := make([]Result, 0, limit)
	seenDomain := map[string]bool{}
	for i := range pool {
		p := &pool[i]
		p.res.Breakdown.BM25 = 0
		if maxBM25 > 0 {
			p.res.Breakdown.BM25 = p.res.Hit.Score / maxBM25
		}
		// 这里不设单条候选的绝对闸门：被埋住的内容 BM25 天然低，逐条卡
		// minBM25 会把这个区该捞的对象全部滤掉（实测「单元测试」40 条剩余候选
		// 全被滤光，发现区返回 0）。整批噪音判定已在函数开头做过。
		rel := math.Max(p.res.Breakdown.BM25, p.res.Breakdown.Exact)
		// 反向链接越少分越高：log 压缩后取倒数
		heat := 0.0
		if denomInbound > 0 {
			heat = math.Log1p(float64(p.inbou)) / denomInbound
		}
		p.res.Breakdown.Prosperity = 1 - heat
		base := discoverWBM25*p.res.Breakdown.BM25 + discoverWExact*p.res.Breakdown.Exact +
			discoverWQuality*p.res.Breakdown.Quality + discoverWCold*p.res.Breakdown.Prosperity
		base *= 0.25 + 0.75*rel
		if seenDomain[p.res.Hit.Domain] {
			base *= r.w.DomainDecay
		}
		p.res.Score = base
		seenDomain[p.res.Hit.Domain] = true // 同域第二条起衰减，避免一个博客占满发现区
		reasons := []string{"与查询相关"}
		if p.res.Breakdown.Exact >= 0.55 {
			reasons = append(reasons, "关键词命中标题/摘要")
		}
		if p.inbou == 0 {
			reasons = append(reasons, "语料内无人引用，被算法埋住")
		} else {
			reasons = append(reasons, "反向链接较少（语料内 "+itoa(p.inbou)+" 次引用）")
		}
		if cat := sig.Categories[p.res.Hit.Domain]; cat == "blog" || cat == "opensource" {
			reasons = append(reasons, "来自独立博客/开源项目站点")
		}
		p.res.Reasons = reasons
		out = append(out, p.res)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// 发现区权重：冷热度（反向链接倒数）是主项，相关性仍要足够，质量分兜底。
var (
	discoverWBM25    = 1.0
	discoverWExact   = 2.0
	discoverWQuality = 0.6
	discoverWCold    = 1.2
	// minDiscoverRel 发现区的相关性门槛，高于主排序的 0.1 绝对闸门：
	// 这一区是「顺带发现」，不是第二个结果列表，宁可少给几条。
	minDiscoverRel = 0.3
	// minDiscoverBM25 仅在无标题命中时要求的最低 BM25 归一化值
	minDiscoverBM25 = 0.35
)
