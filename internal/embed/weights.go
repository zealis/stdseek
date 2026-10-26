package embed

import (
	"fmt"
	"path/filepath"
	"strings"
)

// matWeight 一个量化矩阵乘权重：int8 [in×out] + 逐输出通道 zero_point + 逐输出通道 scale。
// 反量化在推理时逐矩阵进行一次（写进复用 scratch），内存里始终是 int8。
//
// scale/zero_point 必须是逐输出通道向量（长度 = Out，来自 weight 张量的第二维）。
// 曾按标量处理（只取 scale[0]）：本模型 per-channel scale 差异达 20 倍
// （intermediate 层 0.00134~0.02737），张量内各通道被系统性错配，
// 表现为每级 1~3% 余弦偏差逐层累积，最终 embedding 余弦只有 0.72。
type matWeight struct {
	W     []int8    // in*out，行主序 [in][out]（ONNX MatMul 权重布局）
	ZP    []int8    // out，逐输出通道
	Scale []float32 // out，逐输出通道
	Out   int
	In    int
}

// lnWeight LayerNorm 的 weight/bias（fp32 小张量）
type lnWeight struct {
	Gamma []float32
	Beta  []float32
}

// layerWeights 单层 transformer 的全部权重
type layerWeights struct {
	QW, KW, VW, AW matWeight // 注意力投影与输出投影
	IW, OW         matWeight // 前馈
	QB, KB, VB, AB []float32
	IB, OB         []float32
	AttnLN         lnWeight
	OutLN          lnWeight
}

// quantTable 一个 uint8 量化嵌入表（词/位置/类型共用结构）
type quantTable struct {
	Data   []uint8   // rows*hidden
	Scale  []float32 // 每行一个，或单元素（每张量）
	ZP     []uint8   // 同上
	Rows   int
	Hidden int
}

// row 取第 r 行并反量化为 float32。scale/zp 每张量一个（本模型即是），
// 兼容每行一个的布局。
func (q *quantTable) row(r int, dst []float32) {
	if len(dst) < q.Hidden || r < 0 || r >= q.Rows {
		return
	}
	base := r * q.Hidden
	sc := q.Scale[0]
	if len(q.Scale) == q.Rows {
		sc = q.Scale[r]
	}
	zp := q.ZP[0]
	if len(q.ZP) == q.Rows {
		zp = q.ZP[r]
	}
	for i := 0; i < q.Hidden; i++ {
		dst[i] = sc * (float32(q.Data[base+i]) - float32(zp))
	}
}

// Model e5-small 推理模型：tokenizer + 量化权重 + 结构参数。
// 权重常驻 int8/uint8（约 118MB），激活全程 fp32。
type Model struct {
	Tok      *Tokenizer
	Layers   []layerWeights
	Word     quantTable
	Pos      quantTable
	Type     quantTable
	EmbLN    lnWeight
	Hidden   int
	Heads    int
	NumLayer int
	Inter    int
	Eps      float32
	MaxPos   int
	// 推理 scratch（单线程使用；并发调用方各自 Embed 前需拷贝或加锁）
	scratch []float32
}

// modelConfig config.json 里取得到的结构参数
type modelConfig struct {
	HiddenSize       int     `json:"hidden_size"`
	NumHeads         int     `json:"num_attention_heads"`
	NumHiddenLayers  int     `json:"num_hidden_layers"`
	IntermediateSize int     `json:"intermediate_size"`
	LayerNormEps     float64 `json:"layer_norm_eps"`
	MaxPositionEmb   int     `json:"max_position_embeddings"`
	VocabSize        int     `json:"vocab_size"`
	TypeVocabSize    int     `json:"type_vocab_size"`
}

// Load 从模型目录加载（onnx/model_quantized.onnx + tokenizer.json + config.json）
func Load(dir string) (*Model, error) {
	var cfg modelConfig
	if err := readJSON(filepath.Join(dir, "config.json"), &cfg); err != nil {
		return nil, fmt.Errorf("config.json: %w", err)
	}
	if cfg.HiddenSize <= 0 || cfg.NumHiddenLayers <= 0 || cfg.NumHeads <= 0 {
		return nil, fmt.Errorf("config.json: 结构参数缺失（hidden=%d layers=%d heads=%d）",
			cfg.HiddenSize, cfg.NumHiddenLayers, cfg.NumHeads)
	}

	tok, err := LoadTokenizer(filepath.Join(dir, "tokenizer.json"))
	if err != nil {
		return nil, err
	}

	g, err := LoadGraph(pickONNX(dir))
	if err != nil {
		return nil, err
	}

	m := &Model{
		Tok:      tok,
		Hidden:   cfg.HiddenSize,
		Heads:    cfg.NumHeads,
		NumLayer: cfg.NumHiddenLayers,
		Inter:    cfg.IntermediateSize,
		Eps:      float32(cfg.LayerNormEps),
		MaxPos:   cfg.MaxPositionEmb,
	}
	if m.Eps == 0 {
		m.Eps = 1e-12
	}
	if m.Inter == 0 {
		m.Inter = m.Hidden * 4
	}
	if m.MaxPos == 0 {
		m.MaxPos = 512
	}
	if err := m.loadWeights(g); err != nil {
		return nil, err
	}
	return m, nil
}

// loadWeights 从图里取权重。量化导出的命名约定（Xenova/transformers.js）：
//   - 嵌入表：<name>.weight_quantized(uint8) + scale/zp（Constant 或 initializer）
//   - 矩阵乘：onnx::MatMul_NNNN_quantized(int8 [out,in]) + _zero_point(in) + scale(Constant)
//
// 每个 matmul 的 scale 通过 ".../MatMul_quant_scales_mul" 的 Mul 节点反查：
// 两个输入分别是动态激活 scale 与 Constant 权重 scale。
func (m *Model) loadWeights(g *onnxGraph) error {
	inits := map[string]*onnxTensor{}
	for i := range g.Initializers {
		inits[g.Initializers[i].Name] = &g.Initializers[i]
	}
	// Constant 节点输出名 → 张量值
	consts := map[string]*onnxTensor{}
	for _, nd := range g.Nodes {
		if nd.OpType != "Constant" {
			continue
		}
		if v, ok := nd.Attrs["value"]; ok {
			if t, ok := v.(*onnxTensor); ok && len(nd.Outputs) > 0 {
				consts[nd.Outputs[0]] = t
			}
		}
	}
	get := func(name string) *onnxTensor {
		if t, ok := inits[name]; ok {
			return t
		}
		return consts[name]
	}

	// ---- 嵌入表 ----
	var err error
	if m.Word, err = loadQuantTable(get, "embeddings.word_embeddings.weight", m.Hidden); err != nil {
		return err
	}
	if m.Pos, err = loadQuantTable(get, "embeddings.position_embeddings.weight", m.Hidden); err != nil {
		return err
	}
	if m.Type, err = loadQuantTable(get, "embeddings.token_type_embeddings.weight", m.Hidden); err != nil {
		return err
	}
	if m.EmbLN, err = loadLN(get, "embeddings.LayerNorm"); err != nil {
		return err
	}

	// ---- matmul scale ----
	// 量化导出的命名约定：每个矩阵乘有三个 initializer——
	//   onnx::MatMul_NNNN_quantized  (int8 [out,in])
	//   onnx::MatMul_NNNN_zero_point (int8 [in])
	//   onnx::MatMul_NNNN_scale      (float 标量)
	// 从权重名直接推导 scale 名即可（曾按 Mul 节点追踪，白费一轮：
	// 那种写法要求 scale 是 Constant 节点产出，实际它是 initializer）。

	// ---- 每层权重 ----
	m.Layers = make([]layerWeights, m.NumLayer)
	for l := 0; l < m.NumLayer; l++ {
		L := &m.Layers[l]
		seg := func(sub string) string {
			return fmt.Sprintf("/encoder/layer.%d/%s/", l, sub)
		}
		type mw struct {
			dst *matWeight
			sub string // MatMulInteger 输出名中的段，如 "attention/self/query"
		}
		jobs := []mw{
			{&L.QW, "attention/self/query"},
			{&L.KW, "attention/self/key"},
			{&L.VW, "attention/self/value"},
			{&L.AW, "attention/output/dense"},
			{&L.IW, "intermediate/dense"},
			{&L.OW, "output/dense"},
		}
		for _, j := range jobs {
			// 找该段的 MatMulInteger 节点
			var node *onnxNode
			for i := range g.Nodes {
				nd := &g.Nodes[i]
				if nd.OpType != "MatMulInteger" || len(nd.Outputs) == 0 {
					continue
				}
				if contains(nd.Outputs[0], seg(j.sub)) {
					node = nd
					break
				}
			}
			if node == nil || len(node.Inputs) < 4 {
				return fmt.Errorf("layer %d: 找不到 %s 的 MatMulInteger", l, j.sub)
			}
			w := get(node.Inputs[1]) // 权重 int8
			zp := get(node.Inputs[3])
			if w == nil || w.DataType != 3 {
				return fmt.Errorf("layer %d %s: 权重不是 INT8", l, j.sub)
			}
			wi, err := w.AsInt8()
			if err != nil {
				return err
			}
			if len(w.Dims) != 2 {
				return fmt.Errorf("layer %d %s: 权重维度 %v 不是 2 维", l, j.sub, w.Dims)
			}
			// ONNX MatMul 的权重布局是 [in, out]（不是 [out, in]）。
			// 搞反的后果：方阵被静默转置（余弦掉到 0.65 而不报错），
			// 非方阵（前馈 384×1536）因形状不符走兜底路径只剩 bias。
			j.dst.W = wi
			j.dst.In = int(w.Dims[0])
			j.dst.Out = int(w.Dims[1])
			if zp != nil && zp.DataType == 3 {
				if zi, err := zp.AsInt8(); err == nil {
					j.dst.ZP = zi
				}
			}
			// zero_point 逐输出通道（非方阵上一眼可辨：前馈 384×1536 的 zp 长 1536）
			if len(j.dst.ZP) != j.dst.Out {
				return fmt.Errorf("layer %d %s: zero_point 长度 %d != out %d", l, j.sub, len(j.dst.ZP), j.dst.Out)
			}
			// scale：从权重名 onnx::MatMul_NNNN_quantized 推导 onnx::MatMul_NNNN_scale。
			// 逐输出通道，长度必须等于 Out（非方阵上一眼可辨：前馈 384×1536 的 scale 长 1536）。
			scName := strings.TrimSuffix(node.Inputs[1], "_quantized") + "_scale"
			st := get(scName)
			if st == nil {
				return fmt.Errorf("layer %d %s: 找不到 scale initializer %s", l, j.sub, scName)
			}
			fl, err := st.AsFloat32()
			if err != nil || len(fl) == 0 {
				return fmt.Errorf("layer %d %s: scale 读取失败 (%v)", l, j.sub, err)
			}
			if len(fl) != j.dst.Out {
				return fmt.Errorf("layer %d %s: scale 长度 %d != out %d", l, j.sub, len(fl), j.dst.Out)
			}
			j.dst.Scale = fl
		}
		// bias（fp32 initializer）
		L.QB = mustFloat(get, fmt.Sprintf("encoder.layer.%d.attention.self.query.bias", l), m.Hidden)
		L.KB = mustFloat(get, fmt.Sprintf("encoder.layer.%d.attention.self.key.bias", l), m.Hidden)
		L.VB = mustFloat(get, fmt.Sprintf("encoder.layer.%d.attention.self.value.bias", l), m.Hidden)
		L.AB = mustFloat(get, fmt.Sprintf("encoder.layer.%d.attention.output.dense.bias", l), m.Hidden)
		L.IB = mustFloat(get, fmt.Sprintf("encoder.layer.%d.intermediate.dense.bias", l), m.Inter)
		L.OB = mustFloat(get, fmt.Sprintf("encoder.layer.%d.output.dense.bias", l), m.Hidden)
		if L.AttnLN, err = loadLN(get, fmt.Sprintf("encoder.layer.%d.attention.output.LayerNorm", l)); err != nil {
			return err
		}
		if L.OutLN, err = loadLN(get, fmt.Sprintf("encoder.layer.%d.output.LayerNorm", l)); err != nil {
			return err
		}
	}
	return nil
}

// loadQuantTable 加载 uint8 量化嵌入表
func loadQuantTable(get func(string) *onnxTensor, name string, hidden int) (quantTable, error) {
	var q quantTable
	q.Hidden = hidden
	w := get(name + "_quantized")
	if w == nil {
		return q, fmt.Errorf("找不到嵌入表 %s", name)
	}
	q.Data = w.Raw
	if len(w.Dims) != 2 {
		return q, fmt.Errorf("%s: 维度 %v 不是 2 维", name, w.Dims)
	}
	q.Rows = int(w.Dims[0])
	if int(w.Dims[1]) != hidden {
		return q, fmt.Errorf("%s: hidden %d != %d", name, w.Dims[1], hidden)
	}
	if len(q.Data) != q.Rows*hidden {
		return q, fmt.Errorf("%s: 数据长度 %d != %d", name, len(q.Data), q.Rows*hidden)
	}
	// scale / zero_point：先找 initializer，再找 Constant
	if s := get(name + "_scale"); s != nil {
		if fl, err := s.AsFloat32(); err == nil {
			q.Scale = fl
		}
	}
	if z := get(name + "_zero_point"); z != nil {
		if zp, err := z.AsUint8(); err == nil {
			q.ZP = zp
		}
	}
	if len(q.Scale) == 0 || len(q.ZP) == 0 {
		return q, fmt.Errorf("%s: 缺少 scale 或 zero_point", name)
	}
	return q, nil
}

// loadLN 加载 LayerNorm 权重
func loadLN(get func(string) *onnxTensor, name string) (lnWeight, error) {
	var l lnWeight
	w := get(name + ".weight")
	b := get(name + ".bias")
	if w == nil || b == nil {
		return l, fmt.Errorf("找不到 LayerNorm %s", name)
	}
	var err error
	if l.Gamma, err = w.AsFloat32(); err != nil {
		return l, err
	}
	if l.Beta, err = b.AsFloat32(); err != nil {
		return l, err
	}
	if len(l.Gamma) != len(l.Beta) {
		return l, fmt.Errorf("LayerNorm %s: weight/bias 长度不一致", name)
	}
	return l, nil
}

// mustFloat 取 fp32 权重，缺失时返回全零（容错：bias 理论上都在）
func mustFloat(get func(string) *onnxTensor, name string, n int) []float32 {
	out := make([]float32, n)
	if t := get(name); t != nil {
		if fl, err := t.AsFloat32(); err == nil && len(fl) == n {
			return fl
		}
	}
	return out
}

func contains(s, sub string) bool { return indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// pickONNX 优先量化版（小），没有则退回 FP32
func pickONNX(dir string) string {
	q := filepath.Join(dir, "onnx", "model_quantized.onnx")
	if fileExists(q) {
		return q
	}
	return filepath.Join(dir, "onnx", "model.onnx")
}
