package summarize

import (
	"context"
	"sort"
	"strings"
)

// Extractive 抽取式摘要：直接拿相关度最高的几句原文当结论，每条结论引用自己。
//
// 它不是"退而求其次的假摘要"，而是这条链路的地基线：
// 零成本、零延迟、不可能幻觉，且天然满足「每条结论都有原文出处」。
// 三个用途：① 没配模型时的默认实现；② 模型不可用时的降级；
// ③ 评测时的对照组——如果 LLM 摘要打不过抽取式，那它只是在浪费钱。
type Extractive struct {
	// Top 输出几条结论。
	Top int
}

func (e *Extractive) Name() string { return "extractive" }

func (e *Extractive) Summarize(_ context.Context, in Input) ([]Conclusion, error) {
	ps := in.Passages
	if len(ps) == 0 {
		return nil, nil
	}
	top := e.Top
	if top <= 0 {
		top = 3
	}
	ranked := make([]Passage, len(ps))
	copy(ranked, ps)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].ID < ranked[j].ID
	})
	if len(ranked) > top {
		ranked = ranked[:top]
	}
	// 结论按原文顺序输出，读起来有脉络；引用编号跟着段落走
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].ID < ranked[j].ID })

	out := make([]Conclusion, 0, len(ranked))
	for _, p := range ranked {
		out = append(out, Conclusion{
			Text:      shorten(p.Text),
			Citations: []Citation{{ID: p.ID, Text: p.Text}},
		})
	}
	return out, nil
}

// shorten 把段落压成"结论长度"（一句话能读完）。
// 抽取式没有改写能力，只能截断——所以截到第一个逗号/分号之前往往比硬截 80 字更像话。
func shorten(s string) string {
	r := []rune(strings.TrimSpace(s))
	const max = 80
	if len(r) <= max {
		return string(r)
	}
	for i := 0; i < len(r) && i < max; i++ {
		if r[i] == '，' || r[i] == '；' || r[i] == ';' || r[i] == ',' {
			if i >= 20 { // 太早截断会只剩半句
				return string(r[:i]) + "…"
			}
		}
	}
	return string(r[:max]) + "…"
}
