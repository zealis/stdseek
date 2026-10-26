package summarize

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestVerifyCitations 引用校验是整条链路的红线闸门：
// 模型编造的编号必须被剔除，剔除后没有引用的结论必须标成「未验证」。
func TestVerifyCitations(t *testing.T) {
	ps := []Passage{
		{ID: 1, Text: "单元测试能够独立运行、快速验证结果。"},
		{ID: 2, Text: "持续维护要求测试与业务代码同步演进。"},
	}
	cs := []Conclusion{
		{Text: "编造来源", Citations: []Citation{{ID: 99}}},          // 越界
		{Text: "真实来源", Citations: []Citation{{ID: 1}}},           // 合法
		{Text: "无来源", Citations: nil},                            // 未验证
		{Text: "部分编造", Citations: []Citation{{ID: 2}, {ID: 77}}}, // 半真
		{Text: "   ", Citations: []Citation{{ID: 1}}},            // 空文本丢弃
	}
	kept, dropped, unverified := verify(cs, ps)

	if dropped != 2 {
		t.Fatalf("越界引用应剔除 2 个，实际 %d", dropped)
	}
	// 「编造来源」整条丢弃（伪造出处），「空文本」丢弃，其余 3 条保留
	if len(kept) != 3 {
		t.Fatalf("应保留 3 条，实际保留 %d：%+v", len(kept), kept)
	}
	if unverified != 1 { // 只有「无来源」是未验证
		t.Fatalf("应有 1 条未验证，实际 %d", unverified)
	}
	for _, c := range kept {
		if c.Text == "编造来源" {
			t.Fatal("伪造出处的结论必须整条丢弃，不能只摘掉引用后保留")
		}
	}
	// 合法引用必须由服务端按编号回填原文，而不是信任 Provider 给的文本
	if len(kept[0].Citations) != 1 || kept[0].Citations[0].Text != ps[0].Text {
		t.Fatalf("引用文本应回填自段落，实际 %+v", kept[0].Citations)
	}
	if len(kept[1].Citations) != 0 {
		t.Fatalf("无来源的结论不应被塞上引用，实际 %+v", kept[1].Citations)
	}
	if len(kept[2].Citations) != 1 || kept[2].Citations[0].ID != 2 {
		t.Fatalf("部分编造应只保留合法编号 2，实际 %+v", kept[2].Citations)
	}
}

// TestModelFabricatedCitationDropped 端到端：模型返回不存在的引用编号，
// 整条链路必须丢弃它，而不是把它当成"有出处的结论"展示给用户。
func TestModelFabricatedCitationDropped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"conclusions\":[{\"text\":\"模型编造的结论\",\"citations\":[42]},{\"text\":\"有据可查的结论\",\"citations\":[1]}]}"}}]}`))
	}))
	defer srv.Close()

	doc := Document{
		Query: "单元测试",
		Title: "测试文档",
		Body:  "单元测试能够独立运行、快速验证结果，并且可以无限次重复执行。这段是干扰内容，与查询无关。",
	}
	res := Summarize(context.Background(), doc, &OpenAICompat{BaseURL: srv.URL, Model: "fake"}, Options{})

	if res.DroppedCitations == 0 {
		t.Fatal("越界引用应被记为 dropped_citations")
	}
	for _, c := range res.Conclusions {
		if strings.Contains(c.Text, "编造") {
			t.Fatalf("编造引用来源的结论不应保留：%+v", c)
		}
		if len(c.Citations) == 0 {
			t.Fatalf("保留的结论必须有引用：%+v", c)
		}
	}
	if len(res.Conclusions) != 1 {
		t.Fatalf("应只保留 1 条可核验结论，实际 %d", len(res.Conclusions))
	}
}

// TestModelUnavailableFallsBackToExtractive 模型超时必须降级并且**说明原因**，
// 不能静默降级——用户会以为那就是模型给的答案。
func TestModelUnavailableFallsBackToExtractive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "quota exceeded", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	doc := Document{
		Query: "单元测试",
		Body:  "单元测试能够独立运行、快速验证结果，并且可以无限次重复执行。",
	}
	res := Summarize(context.Background(), doc, &OpenAICompat{BaseURL: srv.URL, Model: "fake"}, Options{})

	if res.Provider != "extractive" {
		t.Fatalf("应降级为抽取式，实际 provider=%s", res.Provider)
	}
	if !strings.Contains(res.Reason, "降级") {
		t.Fatalf("降级原因必须写明，实际 %q", res.Reason)
	}
	if len(res.Conclusions) == 0 {
		t.Fatal("降级后仍应产出抽取式结论")
	}
}

// TestPureChineseDoesNotCrossLanguage 红线 9 在摘要层的落点：
// 纯中文查询不允许靠英文变体把英文页面抽成段落——那等于把中文用户导向英文摘要。
// 中英混杂查询则允许（「Rust 社区」打 Community 页面是合理场景）。
func TestPureChineseDoesNotCrossLanguage(t *testing.T) {
	body := "Unit testing lets you verify behavior quickly and repeatedly. " +
		"Community events are listed on this page for rust users around the world."
	expand := func(tok string) []string {
		switch tok {
		case "单元测试":
			return []string{"unit testing"}
		case "社区":
			return []string{"community"}
		}
		return nil
	}

	if got := SelectPassages("单元测试", body, 5, expand); len(got) != 0 {
		t.Fatalf("纯中文查询不应跨语言命中英文正文，实际抽到 %d 段：%+v", len(got), got)
	}
	if got := SelectPassages("rust 社区", body, 5, expand); len(got) == 0 {
		t.Fatal("中英混杂查询应允许经术语扩展命中英文正文")
	}
}

// TestNoRelevantPassageReturnsEmpty 找不到相关段落时必须返回空，
// 这是「宁可显示未找到可靠来源，也不硬编」的技术兑现。
func TestNoRelevantPassageReturnsEmpty(t *testing.T) {
	doc := Document{
		Query: "量子计算退相干",
		Body:  "本文介绍如何使用 CSS Grid 做响应式布局，包含三个示例。另一个段落讲 Flexbox 的对齐方式。",
	}
	res := Summarize(context.Background(), doc, &Extractive{}, Options{})
	if !res.Empty {
		t.Fatalf("无关正文应返回 empty，实际 %+v", res)
	}
	if res.Reason == "" {
		t.Fatal("空结果必须给出原因")
	}
}

// TestExtractiveCitesItself 抽取式的每条结论都引用自己所在的段落，
// 这保证"没有引用的结论"在默认实现里也不可能出现。
func TestExtractiveCitesItself(t *testing.T) {
	doc := Document{
		Query: "单元测试",
		Body: "单元测试能够独立运行、快速验证结果，并且可以无限次重复执行。" +
			"相比之下，手工回归测试每次都要重新点一遍界面，成本高得多。" +
			"持续维护要求测试与业务代码同步演进，否则测试会变成负担。",
	}
	res := Summarize(context.Background(), doc, &Extractive{}, Options{ExtractiveTop: 3})
	if res.Empty || len(res.Conclusions) == 0 {
		t.Fatalf("应产出抽取式结论，实际 %+v", res)
	}
	for _, c := range res.Conclusions {
		if len(c.Citations) != 1 {
			t.Fatalf("抽取式结论应各挂 1 条引用，实际 %+v", c)
		}
		if c.Citations[0].Text == "" {
			t.Fatal("引用必须带原文")
		}
	}
}

// TestSplitSentencesKeepsVersionNumbers 切句不能把 v1.28 / Node.js 切碎，
// 否则候选段落会碎成"v1."和"28 新增了…"这种半句。
func TestSplitSentencesKeepsVersionNumbers(t *testing.T) {
	got := splitSentences("Go 1.22 修改了循环变量语义。Node.js 20 支持了新的 API。")
	if len(got) != 2 {
		t.Fatalf("应切成 2 句，实际 %d：%q", len(got), got)
	}
	if !strings.Contains(got[0], "1.22") || !strings.Contains(got[1], "Node.js") {
		t.Fatalf("版本号被切碎：%q", got)
	}
}

// TestOpenAICompatParsesFencedJSON 模型常无视"只输出 JSON"而包一层 ```json，
// 容忍它比报错重试便宜。
func TestOpenAICompatParsesFencedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message": map[string]string{
					"content": "```json\n{\"conclusions\":[{\"text\":\"结论\",\"citations\":[1]}]}\n```",
				},
			}},
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	p := &OpenAICompat{BaseURL: srv.URL, Model: "fake"}
	cs, err := p.Summarize(context.Background(), Input{
		Query: "q", Passages: []Passage{{ID: 1, Text: "段落"}},
	})
	if err != nil {
		t.Fatalf("围栏 JSON 应被容忍：%v", err)
	}
	if len(cs) != 1 || cs[0].Text != "结论" {
		t.Fatalf("解析结果异常：%+v", cs)
	}
}
