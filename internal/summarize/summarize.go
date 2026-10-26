// Package summarize 生成「证据链摘要」：每条结论必须挂可核验的原文引用。
//
// 对应规划书功能 D 的四条硬要求：
//  1. 摘要是索引之上的可选视图，默认折叠 → 由后台 ai_summary 开关控制
//  2. 每条结论强制挂引用编号，点击跳转原文段落 → Citation/Passage
//  3. 无引用来源的结论标记为「未验证」→ Conclusion.Citations 为空即未验证
//  4. 检索层找不到正确原文时宁可显示「未找到可靠来源」→ Result.Empty
//
// 设计上的关键取舍：**先抽编号段落、再让模型从编号里选**。
// 反过来的做法（让模型自由生成、再事后比对原文）有两个问题：
// 事后比对只能验证「这句话在原文里出现过」，挡不住「原文讲 A、模型说 B」；
// 而限定编号范围让模型无法引用不存在的来源，校验退化成一次边界检查。
package summarize

import (
	"context"
	"fmt"
	"strings"
)

// Passage 候选原文段落。编号从 1 开始（前端展示 [1][2]）。
type Passage struct {
	ID    int     `json:"id"`
	Text  string  `json:"text"`
	Score float64 `json:"score"`
}

// Citation 结论引用的原文片段。Text 与 Passage.Text 一致，
// 前端用它做原文快照里的高亮定位。
type Citation struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

// Conclusion 一条摘要结论。
// Citations 为空 = 无法核验 → 前端必须标「未验证」，不能当成普通结论展示。
type Conclusion struct {
	Text      string     `json:"text"`
	Citations []Citation `json:"citations"`
}

// Result 摘要结果。
type Result struct {
	Conclusions []Conclusion `json:"conclusions"`
	Passages    []Passage    `json:"passages"`
	Provider    string       `json:"provider"`
	// Empty 为 true 表示没有找到可靠来源，前端应显示「未找到可靠来源」而不是空白。
	Empty bool `json:"empty"`
	// Reason 说明为什么为空，或为什么降级（可解释性要求）。
	Reason string `json:"reason,omitempty"`
	// DroppedCitations 被校验剔除的引用数（模型编造编号/越界时 >0，用于观测 Prompt 质量）。
	DroppedCitations int `json:"dropped_citations,omitempty"`
}

// Document 摘要的输入文档。Body 是纯文本（调用方负责从 HTML 剥出来）。
//
// 与 Input 分成两个类型是刻意的：Provider 只拿得到编号段落，
// 拿不到全文——拿到全文的模型会想"总结整篇"，而我们要的是"回答这个查询"。
type Document struct {
	Query string
	Title string
	URL   string
	Body  string
}

// Input 交给 Provider 的输入。Provider 只能引用 Passages 里的编号。
type Input struct {
	Query    string
	Title    string
	URL      string
	Passages []Passage
}

// Provider 摘要生成器。
type Provider interface {
	Name() string
	Summarize(ctx context.Context, in Input) ([]Conclusion, error)
}

// Options 摘要参数。
type Options struct {
	// MaxPassages 送给 Provider 的候选段落上限（也是可引用编号的上限）。
	MaxPassages int
	// ExtractiveTop 抽取式 Provider 输出几条结论。
	ExtractiveTop int
	// Expand 可选的术语变体扩展（中→英）。跨语言页面能不能命中全靠它：
	// 「Rust 社区」打 rust-lang.org/community 时，正文里只有 Community，
	// 不扩展的话覆盖率只有一半，段落全被门槛挡掉。
	Expand func(tok string) []string
}

func (o *Options) norm() {
	if o.MaxPassages <= 0 {
		o.MaxPassages = 8
	}
	if o.ExtractiveTop <= 0 {
		o.ExtractiveTop = 3
	}
}

// Summarize 主流程：抽段落 → Provider 生成 → 引用校验。
//
// Provider 失败时降级到抽取式（纯本地、零成本、不可能幻觉），
// 并在 Reason 里写明降级原因——静默降级会让用户以为模型本来就这么答的。
func Summarize(ctx context.Context, doc Document, p Provider, opts Options) Result {
	opts.norm()
	passages := SelectPassages(doc.Query, doc.Body, opts.MaxPassages, opts.Expand)
	if len(passages) == 0 {
		return Result{
			Provider: providerName(p),
			Empty:    true,
			Reason:   "正文中没有与查询相关的段落，未生成摘要",
		}
	}
	in := Input{Query: doc.Query, Title: doc.Title, URL: doc.URL, Passages: passages}

	var (
		conclusions []Conclusion
		reason      string
		used        = p
	)
	if p != nil {
		got, err := p.Summarize(ctx, in)
		if err != nil {
			// 降级：模型超时/额度耗尽/输出不是合法 JSON 都可能走到这里
			fallback := &Extractive{Top: opts.ExtractiveTop}
			conclusions, _ = fallback.Summarize(ctx, in)
			reason = "模型不可用（" + err.Error() + "），已降级为抽取式摘要"
			used = fallback
		} else {
			conclusions = got
		}
	} else {
		fallback := &Extractive{Top: opts.ExtractiveTop}
		conclusions, _ = fallback.Summarize(ctx, in)
		used = fallback
	}

	if len(conclusions) == 0 {
		return Result{
			Passages: passages, Provider: providerName(used),
			Empty: true, Reason: reason + "；原文段落不足以支撑结论",
		}
	}

	kept, dropped, unverified := verify(conclusions, passages)
	if len(kept) == 0 {
		return Result{
			Passages: passages, Provider: providerName(used), Empty: true,
			Reason:           "生成的结论都没有可核验的引用，已全部丢弃",
			DroppedCitations: dropped,
		}
	}
	if unverified > 0 && reason == "" {
		reason = fmt.Sprintf("其中 %d 条结论缺少可核验引用，已标记为未验证", unverified)
	}
	return Result{
		Conclusions: kept, Passages: passages, Provider: providerName(used),
		Reason: reason, DroppedCitations: dropped,
	}
}

// verify 引用校验：编号必须落在 passages 范围内。
//
// 合法引用补全 Citation.Text（Provider 只需要给编号，文本由服务端填，
// 这样模型无法伪造「引用原文」的措辞）；越界编号直接剔除并计数。
//
// 两种情况要区别对待，不能都算「未验证」：
//   - 模型没给任何引用 → 保留，标「未验证」（它只是没找到出处）
//   - 模型给了引用但**全部无效** → 丢弃整条结论。这不是"没找到出处"，
//     是"伪造了出处"——比没有出处更危险，因为它伪装成有证据。
func verify(cs []Conclusion, ps []Passage) (kept []Conclusion, dropped, unverified int) {
	byID := make(map[int]Passage, len(ps))
	for _, p := range ps {
		byID[p.ID] = p
	}
	for _, c := range cs {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		var cites []Citation
		for _, old := range c.Citations {
			p, ok := byID[old.ID]
			if !ok {
				dropped++
				continue
			}
			cites = append(cites, Citation{ID: p.ID, Text: p.Text})
		}
		if len(cites) == 0 {
			if len(c.Citations) > 0 {
				continue // 伪造出处：声称有引用但一条都对不上
			}
			unverified++
		}
		kept = append(kept, Conclusion{Text: text, Citations: cites})
	}
	return kept, dropped, unverified
}

func providerName(p Provider) string {
	if p == nil {
		return "none"
	}
	return p.Name()
}
