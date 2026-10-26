package embed

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Tokenizer 实现 XLM-R 的 sentencepiece Unigram 分词（multilingual-e5-small 用）。
//
// 流程与 HF tokenizers 对齐（对拍基准见 embed_test.go）：
//  1. 归一化：NFKC + 连续空白折叠为单个空格 + 首尾空白剥离。
//     原始的 Precompiled charsmap 未实现——用 NFKC 近似，24 条多样化文本
//     （含全角/日韩/emoji/HTML）对拍完全一致；若未来出现分歧再补 charsmap 解码。
//  2. Metaspace 预分词：前置 ▁，空格全部替换为 ▁，整串作为一个 piece。
//  3. Unigram Viterbi：在整串上找总分最高的切分（最大化分数和）。
//  4. 后处理：包 <s> ... </s>，按 maxLen 截断内容 token。
type Tokenizer struct {
	vocab    []string // 有序词表，二分查找用
	ids      map[string]int32
	scores   map[string]float64
	maxTokB  int // 最长 token 的字节数（Viterbi 剪枝上界）
	unkID    int32
	unkScore float64
	bosID    int32
	eosID    int32
}

// vocabPair 词表条目：JSON 里是 [token, score] 数组
type vocabPair struct {
	Token string
	Score float64
}

func (v *vocabPair) UnmarshalJSON(b []byte) error {
	var arr [2]interface{}
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	v.Token, _ = arr[0].(string)
	v.Score, _ = arr[1].(float64)
	return nil
}

// tokenizerJSON 只解析需要的字段
type tokenizerJSON struct {
	Model struct {
		Type  string      `json:"type"`
		UnkID int         `json:"unk_id"`
		Vocab []vocabPair `json:"vocab"`
	} `json:"model"`
	AddedTokens []struct {
		ID      int    `json:"id"`
		Content string `json:"content"`
		Special bool   `json:"special"`
	} `json:"added_tokens"`
	PreTokenizer struct {
		Type           string `json:"type"`
		Replacement    string `json:"replacement"`
		AddPrefixSpace bool   `json:"add_prefix_space"`
	} `json:"pre_tokenizer"`
}

// LoadTokenizer 从 tokenizer.json 加载
func LoadTokenizer(path string) (*Tokenizer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tj tokenizerJSON
	if err := json.Unmarshal(raw, &tj); err != nil {
		return nil, fmt.Errorf("tokenizer.json: %w", err)
	}
	if tj.Model.Type != "Unigram" {
		return nil, fmt.Errorf("只支持 Unigram 模型，得到 %q", tj.Model.Type)
	}

	t := &Tokenizer{
		ids:    make(map[string]int32, len(tj.Model.Vocab)),
		scores: make(map[string]float64, len(tj.Model.Vocab)),
		unkID:  int32(tj.Model.UnkID),
		bosID:  0,
		eosID:  2,
	}
	for i, e := range tj.Model.Vocab {
		t.vocab = append(t.vocab, e.Token)
		t.ids[e.Token] = int32(i)
		t.scores[e.Token] = e.Score
		if len(e.Token) > t.maxTokB {
			t.maxTokB = len(e.Token)
		}
	}
	if len(t.vocab) == 0 {
		return nil, fmt.Errorf("tokenizer.json: 词表为空")
	}
	// unk 的分数取「原词表中 unk_id 位置」的分数（必须在排序前取）。
	// 对拍基准里 unk 分数为 0.0。
	if tj.Model.UnkID >= 0 && tj.Model.UnkID < len(tj.Model.Vocab) {
		t.unkScore = tj.Model.Vocab[tj.Model.UnkID].Score
	}
	sort.Strings(t.vocab)
	return t, nil
}

// normalizeText NFKC + 空白折叠/剥离。XLM-R 的 Precompiled charsmap 的近似。
func normalizeText(s string) string {
	s = norm.NFKC.String(s)
	// 空白统一为空格并折叠
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// metaspace 前置 ▁ 并把空格替换为 ▁
func metaspace(s string) string {
	if s == "" {
		return "▁"
	}
	var b strings.Builder
	b.Grow(len(s) + 3)
	b.WriteString("▁")
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			b.WriteString("▁")
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Encode 分词并截断。返回的 ids 含 <s>/</s>，长度 ≤ maxLen。
func (t *Tokenizer) Encode(text string, maxLen int) []int32 {
	if text == "" {
		return []int32{t.bosID, t.eosID}
	}
	s := metaspace(normalizeText(text))
	toks := t.viterbi(s)

	// 截断内容 token（特殊 token 各占 1 位）
	limit := maxLen - 2
	if limit > 0 && len(toks) > limit {
		toks = toks[:limit]
	}
	ids := make([]int32, 0, len(toks)+2)
	ids = append(ids, t.bosID)
	ids = append(ids, toks...)
	ids = append(ids, t.eosID)
	return ids
}

// viterbi 在整串上做 Unigram 动态规划：best[j] = s[:j] 的最高总分。
// 候选只有两种：命中词表的 token，或兜底的单字符 unk。
func (t *Tokenizer) viterbi(s string) []int32 {
	n := len(s)
	if n == 0 {
		return nil
	}
	const negInf = -1e18
	best := make([]float64, n+1)
	from := make([]int32, n+1)
	tokID := make([]int32, n+1)
	for i := 1; i <= n; i++ {
		best[i] = negInf
	}
	for i := 0; i < n; i++ {
		if best[i] == negInf {
			continue
		}
		// s[i] 必是 rune 起点：词表 token 都是合法 UTF-8，unk 按整 rune 跳
		maxL := t.maxTokB
		if i+maxL > n {
			maxL = n - i
		}
		matched := false
		for l := 1; l <= maxL; l++ {
			j := i + l
			id, sc, ok := t.lookup(s[i:j])
			if !ok {
				continue
			}
			matched = true
			if v := best[i] + sc; v > best[j] {
				best[j] = v
				from[j] = int32(i)
				tokID[j] = id
			}
		}
		// unk 是兜底而不是候选：只有该位置没有任何词表 token 命中
		// （lattice 为空）时才按一个 rune 跳过。若让 unk 按分数参与竞争，
		// 分数 0.0 的 <unk> 会击败所有负分词数的真 token，整段变成 unk
		// （对拍时 24 条里 11 条就是这么挂的）。
		if !matched {
			_, sz := utf8.DecodeRuneInString(s[i:])
			j := i + sz
			if v := best[i] + t.unkScore; v > best[j] {
				best[j] = v
				from[j] = int32(i)
				tokID[j] = t.unkID
			}
		}
	}
	if best[n] == negInf {
		return nil
	}
	// 回溯
	var out []int32
	for j := n; j > 0; {
		out = append(out, tokID[j])
		j = int(from[j])
	}
	for i, l := 0, len(out); i < l/2; i++ {
		out[i], out[l-1-i] = out[l-1-i], out[i]
	}
	return out
}

// lookup 二分查找 token（词表已排序）
func (t *Tokenizer) lookup(s string) (int32, float64, bool) {
	i := sort.SearchStrings(t.vocab, s)
	if i < len(t.vocab) && t.vocab[i] == s {
		return t.ids[s], t.scores[s], true
	}
	return 0, 0, false
}

// Vocab 词表大小（测试用）
func (t *Tokenizer) Vocab() int { return len(t.vocab) }
