package embed

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// loadGT 读取 Node transformers.js 生成的对拍基准（token IDs + embedding）
func loadGT(t *testing.T) []gtEntry {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Skip(err.Error())
	}
	raw, err := os.ReadFile(filepath.Join(root, "tmpdata/spike/ground-truth.json"))
	if err != nil {
		t.Skip("找不到 ground-truth.json（先跑 tmpdata/spike/gt_dump.js）")
	}
	var out []gtEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type gtEntry struct {
	Text string    `json:"text"`
	IDs  []int64   `json:"ids"`
	Mask []int64   `json:"mask"`
	Emb  []float64 `json:"emb"`
}

// loadTok 加载基准同款模型的 tokenizer
func loadTok(t *testing.T) *Tokenizer {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Skip(err.Error())
	}
	tok, err := LoadTokenizer(filepath.Join(root, "tmpdata/spike/multilingual-e5-small/tokenizer.json"))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestTokenizerMatchesGroundTruth 与 transformers.js 逐条对拍：
// 24 条文本覆盖中英混排、全角、日韩、emoji、HTML、空白等，IDs 必须完全一致。
func TestTokenizerMatchesGroundTruth(t *testing.T) {
	tok := loadTok(t)
	for _, g := range loadGT(t) {
		got := tok.Encode(g.Text, 512)
		want := make([]int32, len(g.IDs))
		for i, v := range g.IDs {
			want[i] = int32(v)
		}
		if len(got) != len(want) {
			t.Errorf("%q: token 数 %d want %d\n got=%v\nwant=%v", trunc(g.Text), len(got), len(want), got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q: ids[%d]=%d want %d\n got=%v\nwant=%v", trunc(g.Text), i, got[i], want[i], got, want)
				break
			}
		}
	}
}

func trunc(s string) string {
	if len(s) > 24 {
		return s[:24] + "…"
	}
	return s
}

// TestNormalizeAndMetaspace 归一化与 Metaspace 的单元行为
func TestNormalizeAndMetaspace(t *testing.T) {
	cases := []struct{ in, norm, meta string }{
		{"PHP 命名空间", "PHP 命名空间", "▁PHP▁命名空间"},
		{"全角：＂中文＂", "全角:\"中文\"", "▁全角:\"中文\""}, // NFKC 全角转半角
		{"   ", "", "▁"},               // 连续空白折叠后剥离
		{"a  b\tc", "a b c", "▁a▁b▁c"}, // 制表符当空白
	}
	for _, c := range cases {
		if got := normalizeText(c.in); got != c.norm {
			t.Errorf("normalize(%q)=%q want %q", c.in, got, c.norm)
		}
		if got := metaspace(c.norm); got != c.meta {
			t.Errorf("metaspace(%q)=%q want %q", c.norm, got, c.meta)
		}
	}
}

// TestEncodeTruncation 截断后仍保留特殊 token 且不超长
func TestEncodeTruncation(t *testing.T) {
	tok := loadTok(t)
	long := ""
	for i := 0; i < 500; i++ {
		long += "这是一段用于测试截断行为的中文文本。"
	}
	ids := tok.Encode(long, 64)
	if len(ids) != 64 {
		t.Fatalf("截断后长度=%d want 64", len(ids))
	}
	if ids[0] != 0 || ids[63] != 2 {
		t.Fatalf("首尾应为 <s>/</s>，得到 %d/%d", ids[0], ids[63])
	}
}

// loadModel 加载完整推理模型（量化版 onnx）
func loadModel(t *testing.T) *Model {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Skip(err.Error())
	}
	m, err := Load(filepath.Join(root, "tmpdata/spike/multilingual-e5-small"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestEmbeddingMatchesGroundTruth 前向对拍：与 transformers.js（同一量化模型）
// 的 mean-pooling + L2 归一化结果比余弦。
//
// 容差为什么是 0.995 而不是 0.999：基准侧跑的是量化 ONNX，ONNX Runtime 对激活
// 也做了动态 uint8 量化（DynamicQuantizeLinear），Go 侧激活全程 fp32。两边激活
// 精度不同，差异下限不由实现决定，而由模型自身的量化噪声决定。
// 实测该噪声线（同一模型 fp32 与量化两套权重各自跑出来的差异）：
// 常规文本 0.99496 ~ 0.99792；退化输入（"" t=2 / "   " t=3，pooling 只有 2~3 个
// token，信号极弱）低至 0.990565 / 0.991386。
// 即"完全正确的实现"与量化基准的余弦也只能落在 0.99 以上，0.999 物理上达不到。
// 精确判定交给下面的 TestEmbeddingNotWorseThanFP32Reference（逐条比官方 fp32 参照）。
func TestEmbeddingMatchesGroundTruth(t *testing.T) {
	m := loadModel(t)
	if m.Hidden != 384 || m.NumLayer != 12 {
		t.Fatalf("模型结构异常: hidden=%d layers=%d", m.Hidden, m.NumLayer)
	}
	worst := 1.0
	worstText := ""
	for _, g := range loadGT(t) {
		got := m.Embed(g.Text)
		if len(got) != len(g.Emb) {
			t.Fatalf("%q: 向量维度 %d want %d", trunc(g.Text), len(got), len(g.Emb))
		}
		cos := cosine(got, g.Emb)
		if cos < worst {
			worst, worstText = cos, trunc(g.Text)
		}
		if cos < 0.99 {
			t.Errorf("%q: 余弦 %.6f < 0.99（量化噪声下沿 0.990565）", trunc(g.Text), cos)
		}
	}
	t.Logf("24 条对拍最低余弦 %.6f（%s）", worst, worstText)
}

// TestEmbeddingNotWorseThanFP32Reference 交叉判据：Go 实现必须不劣于官方 fp32 实现。
//
// 单纯放宽阈值会掩盖真实 bug（曾经 scale 取错成标量时余弦只有 0.72）。这条用
// 相对判据兜底：拿同一批文本，比较
//
//	cos(Go, 量化基准)  vs  cos(官方 fp32, 量化基准)
//
// 前者必须不低于后者（留 0.0005 浮点余量）。含义是"我们的实现至少和官方 fp32
// 权重跑出来的结果一样接近量化输出"——任何系统性实现错误（权重布局、scale、
// 位置编码、GELU）都会让前者显著劣于后者，从而被抓住。
func TestEmbeddingNotWorseThanFP32Reference(t *testing.T) {
	m := loadModel(t)
	root, err := repoRoot()
	if err != nil {
		t.Skip(err.Error())
	}
	raw, err := os.ReadFile(filepath.Join(root, "tmpdata/spike/ground-truth-fp32.json"))
	if err != nil {
		t.Skip("找不到 ground-truth-fp32.json（先跑 tmpdata/spike/gt_fp32.js）")
	}
	var fp32 []struct {
		Text string    `json:"text"`
		Emb  []float64 `json:"emb"`
	}
	if err := json.Unmarshal(raw, &fp32); err != nil {
		t.Fatal(err)
	}
	quant := map[string][]float64{}
	for _, g := range loadGT(t) {
		quant[g.Text] = g.Emb
	}
	if len(fp32) == 0 {
		t.Skip("fp32 基准为空")
	}
	for _, e := range fp32 {
		q, ok := quant[e.Text]
		if !ok {
			t.Fatalf("fp32 基准文本 %q 不在量化基准里，无法交叉比对", e.Text)
		}
		got := m.Embed(e.Text)
		if len(got) == 0 {
			t.Fatalf("%q: Go 侧返回空向量", e.Text)
		}
		ours := cosine(got, q)
		ref := cosine64(e.Emb, q)
		if ours < ref-0.0005 {
			t.Errorf("%q: 我们 %.6f 劣于官方 fp32 参照 %.6f（差 %.6f）——存在系统性实现偏差",
				trunc(e.Text), ours, ref, ref-ours)
		} else {
			t.Logf("%q: 我们 %.6f vs 官方 fp32 %.6f（+%0.6f）", trunc(e.Text), ours, ref, ours-ref)
		}
	}
}

// TestQueryPassagePrefix 前缀必须真的生效。
//
// e5 的查询/文档是两个指令空间，索引侧写 passage、查询侧写 query；
// 任一侧漏了前缀（或两侧都裸文本），余弦仍算得出来、不报错、不崩溃，
// 只是检索质量悄悄塌掉——纯向量 NDCG@10 从 0.929 掉到 0.430。
// 这种"静默退化"只能靠测试钉住。
func TestQueryPassagePrefix(t *testing.T) {
	m := loadModel(t)
	bare := m.Embed("单元测试")
	qv := m.EmbedQuery("单元测试")
	pv := m.EmbedPassage("单元测试", 256)
	if len(bare) == 0 || len(qv) == 0 || len(pv) == 0 {
		t.Fatal("向量为空")
	}
	// 三者两两不同：加前缀后 token 序列变了，向量必然变化
	for _, c := range []struct {
		name string
		a, b []float32
	}{
		{"query vs bare", qv, bare},
		{"passage vs bare", pv, bare},
		{"query vs passage", qv, pv},
	} {
		if cos := cosine32(c.a, c.b); cos > 0.999 {
			t.Errorf("%s: 余弦 %.6f，前缀似乎没生效（两者几乎相同）", c.name, cos)
		} else {
			t.Logf("%s: 余弦 %.4f（有区分度）", c.name, cos)
		}
	}
}

// cosine32 两个 float32 向量的余弦
func cosine32(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// cosine float32 向量与 float64 基准向量的余弦
func cosine(a []float32, b []float64) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * b[i]
		na += float64(a[i]) * float64(a[i])
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// cosine64 两个 float64 向量的余弦（基准互比用）
func cosine64(a, b []float64) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
