package suggest

import (
	"reflect"
	"testing"
)

// 真实响应样本（截断了后面的条目）：外层是 JSONP 包装，AS.Results 下按
// Type（VS 是热点推荐、AS 是联想词）分组，每组里是 Suggests[].Txt。
const sample = `if(typeof window.bing.sug == 'function') window.bing.sug({"AS":{"Query":"kubernetes","FullResults":1,"Results":[{"Type":"VS","Suggests":[{"Txt":"kubernetes 最新动态","Type":"MT","Sk":"CT1LT2","HCS":0}]},{"Type":"AS","Suggests":[{"Txt":"kubernetes","Type":"CT","Sk":"","HCS":0},{"Txt":"kubernetes教程","Type":"LT","Sk":"","HCS":0}]}]}} /* pageview_candidate */);`

func TestParseSuggestions(t *testing.T) {
	got, err := parseSuggestions([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"kubernetes 最新动态", "kubernetes", "kubernetes教程"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// 去重 + 上限：Bing 偶尔在不同 Type 组里给出同一个词。
func TestParseSuggestionsDedupAndCap(t *testing.T) {
	raw := []byte(`window.bing.sug({"AS":{"Results":[
		{"Type":"VS","Suggests":[{"Txt":"a"},{"Txt":"b"}]},
		{"Type":"AS","Suggests":[{"Txt":"a"},{"Txt":" "},{"Txt":"c"},{"Txt":"d"},{"Txt":"e"},{"Txt":"f"},{"Txt":"g"},{"Txt":"h"},{"Txt":"i"}]}
	]}});`)
	got, err := parseSuggestions(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != maxItems {
		t.Fatalf("应截断到 %d 条，得到 %d: %v", maxItems, len(got), got)
	}
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("去重或顺序不对: %v", got)
	}
}

// 空结果与异常输入都不该报错，返回空即可。
func TestParseSuggestionsEdgeCases(t *testing.T) {
	for _, in := range []string{
		`window.bing.sug({"AS":{"Results":[]}});`,
		`window.bing.sug({"AS":{"Query":"x","FullResults":0}});`,
	} {
		got, err := parseSuggestions([]byte(in))
		if err != nil {
			t.Errorf("%s → 不该报错: %v", in, err)
		}
		if len(got) != 0 {
			t.Errorf("%s → 应为空，得到 %v", in, got)
		}
	}
}

func TestStripJSONP(t *testing.T) {
	cases := map[string]string{
		`window.bing.sug({"a":1})`:      `{"a":1}`,
		`window.bing.sug({"a":"(x)"});`: `{"a":"(x)"}`,
		`{"a":1}`:                       `{"a":1}`, // 已经是纯 JSON
	}
	for in, want := range cases {
		if got := string(stripJSONP([]byte(in))); got != want {
			t.Errorf("stripJSONP(%q) = %q, want %q", in, got, want)
		}
	}
}
