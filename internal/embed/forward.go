package embed

import (
	"math"
	"runtime"
	"sync"
)

// actQuantMax 激活动态量化的整数范围上限，见 matmulGeneric 的溢出推导
const actQuantMax = 4095

// Embed 文本 → 384 维归一化向量（截断到模型上限 MaxPos=512），不加任何前缀。
// 流程与 transformers.js 的 feature-extraction(pooling=mean, normalize=true) 对齐，
// 对拍基准也是不带前缀跑出来的，所以前缀必须由调用侧显式加、不在这里默认加。
func (m *Model) Embed(text string) []float32 {
	return m.EmbedN(text, m.MaxPos)
}

// e5 系列的查询/文档前缀（intfloat/multilingual-e5 的官方用法）：
// 模型用对比学习训练，查询与文档是两个不同的"指令空间"，不加前缀时
// 两者虽然落在同一个向量空间，但区分度显著下降。
//
// 实测（61 条纯中文，纯向量排序）：带前缀 NDCG@10 0.929，不带 0.430。
// 前缀必须由调用侧加：索引侧写 passage、查询侧写 query，两侧口径必须一致，
// 否则索引用 passage、查询用裸文本，等于在两套空间里比余弦。
const (
	QueryPrefix   = "query: "
	PassagePrefix = "passage: "
)

// EmbedQuery 查询侧向量（自动加 "query: " 前缀）。
func (m *Model) EmbedQuery(q string) []float32 {
	return m.Embed(QueryPrefix + q)
}

// EmbedPassage 文档侧向量（自动加 "passage: " 前缀，截断到 maxTok）。
func (m *Model) EmbedPassage(text string, maxTok int) []float32 {
	return m.EmbedN(PassagePrefix+text, maxTok)
}

// EmbedN 指定最大 token 数。索引长文档时用 256：耗时与 token 数成正比
// （512 token 约 1.85s/篇，256 约 0.93s），而技术文档的标题与开头已承载
// 主要语义，截断到 256 的检索质量损失远小于索引耗时减半的收益。
func (m *Model) EmbedN(text string, maxTok int) []float32 {
	if maxTok <= 0 {
		maxTok = m.MaxPos
	}
	if maxTok > m.MaxPos {
		maxTok = m.MaxPos
	}
	ids := m.Tok.Encode(text, maxTok)
	if len(ids) < 2 {
		return nil
	}
	hidden := m.lastHidden(ids)
	t := len(ids)
	// mean pooling（mask 全 1，含特殊 token）
	out := make([]float32, m.Hidden)
	for i := 0; i < t; i++ {
		row := hidden[i*m.Hidden : (i+1)*m.Hidden]
		for j := range row {
			out[j] += row[j]
		}
	}
	inv := 1 / float32(t)
	for j := range out {
		out[j] *= inv
	}
	// L2 归一化
	var norm float64
	for _, v := range out {
		norm += float64(v) * float64(v)
	}
	if norm > 0 {
		invNorm := float32(1 / math.Sqrt(norm))
		for j := range out {
			out[j] *= invNorm
		}
	}
	return out
}

// lastHidden 跑完整前向，返回 [t, hidden] 的最后一层隐状态（行主序）
func (m *Model) lastHidden(ids []int32) []float32 {
	t := len(ids)
	h := m.Hidden
	x := make([]float32, t*h)

	// ---- 嵌入：word + type(0) + pos(2..t+1) ----
	var wbuf, tbuf, pbuf []float32
	for i := 0; i < t; i++ {
		if wbuf == nil {
			wbuf, tbuf, pbuf = make([]float32, h), make([]float32, h), make([]float32, h)
		}
		id := int(ids[i])
		if id >= 0 && id < m.Word.Rows {
			m.Word.row(id, wbuf)
		} else {
			clear(wbuf)
		}
		m.Type.row(0, tbuf) // token_type_ids 恒 0
		// position_ids 从 0 开始（不是 XLM-R 常见的偏移 2）。
		// 按模型家族想当然取 2 时，嵌入段与 ONNX Runtime 的余弦只有 0.926；
		// 用 ORT 逐段对拍扫出来的正确值是 0，余弦 1.000000。
		pos := i
		if pos < m.Pos.Rows {
			m.Pos.row(pos, pbuf)
		} else {
			clear(pbuf)
		}
		dst := x[i*h : (i+1)*h]
		for j := 0; j < h; j++ {
			dst[j] = wbuf[j] + tbuf[j] + pbuf[j]
		}
	}
	layerNormRows(m, x, &m.EmbLN)

	// ---- 12 层 transformer ----
	// 每层的中间缓冲（串行层间依赖，层内可并行）
	var q, k, v, attnOut, inter, ffnOut []float32
	if t > 0 {
		q = make([]float32, t*h)
		k = make([]float32, t*h)
		v = make([]float32, t*h)
		attnOut = make([]float32, t*h)
		inter = make([]float32, t*m.Inter)
		ffnOut = make([]float32, t*h)
	}
	// 权重反量化 scratch 已取消：反量化融进矩阵乘内层，按输入维逐行进行，
	// 暂存块只有输出通道那么大（≤384 个 float，常驻 L1）。
	for l := 0; l < m.NumLayer; l++ {
		L := &m.Layers[l]
		matmulQ(m, &L.QW, x, t, L.QB, q)
		matmulQ(m, &L.KW, x, t, L.KB, k)
		matmulQ(m, &L.VW, x, t, L.VB, v)

		// 多头注意力：12 头 × 32 维
		headDim := h / m.Heads
		scale := float32(1 / math.Sqrt(float64(headDim)))
		// 每个头必须有自己的 scores 缓冲区：并发写同一个 [t×t] 会互相覆盖，
		// 表现为 Q/K/V 都对（余弦 0.98）而注意力输出是垃圾（0.31）。
		scores := make([]float32, m.Heads*t*t)
		var wg sync.WaitGroup
		for hd := 0; hd < m.Heads; hd++ {
			wg.Add(1)
			go func(hd int) {
				defer wg.Done()
				off := hd * headDim
				hs := scores[hd*t*t : (hd+1)*t*t] // 该头专属切片
				// scores[i][j] = q_i·k_j·scale
				for i := 0; i < t; i++ {
					qi := q[i*h+off : i*h+off+headDim]
					row := hs[i*t : (i+1)*t]
					for j := 0; j < t; j++ {
						kj := k[j*h+off : j*h+off+headDim]
						var s float32
						for d := 0; d < headDim; d++ {
							s += qi[d] * kj[d]
						}
						row[j] = s * scale
					}
				}
				// softmax（按行）
				for i := 0; i < t; i++ {
					row := hs[i*t : (i+1)*t]
					mx := row[0]
					for _, s := range row {
						if s > mx {
							mx = s
						}
					}
					var sum float32
					for j := range row {
						row[j] = float32(math.Exp(float64(row[j] - mx)))
						sum += row[j]
					}
					if sum > 0 {
						inv := 1 / sum
						for j := range row {
							row[j] *= inv
						}
					}
				}
				// out_i = Σ_j p_ij·v_j
				for i := 0; i < t; i++ {
					oi := attnOut[i*h+off : i*h+off+headDim]
					for d := 0; d < headDim; d++ {
						oi[d] = 0
					}
					for j := 0; j < t; j++ {
						p := hs[i*t+j]
						if p == 0 {
							continue
						}
						vj := v[j*h+off : j*h+off+headDim]
						for d := 0; d < headDim; d++ {
							oi[d] += p * vj[d]
						}
					}
				}
			}(hd)
		}
		wg.Wait()

		matmulQ(m, &L.AW, attnOut, t, L.AB, ffnOut)
		addLayerNorm(m, x, ffnOut, &L.AttnLN)

		// 前馈：gelu(x@Wi+bi) @Wo+bo
		matmulI(m, &L.IW, x, t, L.IB, inter)
		for i := range inter {
			inter[i] = geluErf(inter[i])
		}
		matmulQ(m, &L.OW, inter, t, L.OB, ffnOut)
		addLayerNorm(m, x, ffnOut, &L.OutLN)
	}
	return x
}

// geluErf 精确 GELU（erf 版，与 ONNX 图的 Erf 算子一致）
func geluErf(x float32) float32 {
	return float32(0.5 * float64(x) * (1 + math.Erf(float64(x)/math.Sqrt2)))
}

// layerNormRows 对 [t, hidden] 逐行做 LayerNorm（就地）
func layerNormRows(m *Model, x []float32, ln *lnWeight) {
	h := m.Hidden
	eps := m.Eps
	for i := 0; i < len(x)/h; i++ {
		row := x[i*h : (i+1)*h]
		var mean float32
		for _, v := range row {
			mean += v
		}
		mean /= float32(h)
		var varSum float32
		for _, v := range row {
			d := v - mean
			varSum += d * d
		}
		varSum /= float32(h)
		inv := float32(1 / math.Sqrt(float64(varSum)+float64(eps)))
		for j := range row {
			row[j] = (row[j]-mean)*inv*ln.Gamma[j] + ln.Beta[j]
		}
	}
}

// addLayerNorm 残差 + LayerNorm：x = LayerNorm(x + sub)
func addLayerNorm(m *Model, x, sub []float32, ln *lnWeight) {
	for i := range x {
		x[i] += sub[i]
	}
	layerNormRows(m, x, ln)
}

// matmulQ y[t,out] = bias + x[t,in] @ W，输出维度为 Hidden 的通用路径。
func matmulQ(m *Model, w *matWeight, x []float32, t int, bias, y []float32) {
	matmulGeneric(w, x, t, bias, y, m.Hidden)
}

// matmulI 前馈第一级（输出维度 Inter）
func matmulI(m *Model, w *matWeight, x []float32, t int, bias, y []float32) {
	matmulGeneric(w, x, t, bias, y, m.Inter)
}

// matmulGeneric 量化矩阵乘：int8 权重 × int16 激活 → int32 累加，最后一次性反量化。
//
//	y[o] = bias[o] + aScale·scale[o]·(Σ_i xq[i]·W[i][o] − zp[o]·Σ_i xq[i])
//
// 激活为什么量化到 int16 而不是 int8：
// 对称 int8（±127）对有偏分布浪费一半动态范围，余弦从 0.997 掉到 0.985；
// 非对称 uint8（对齐 ORT 的 DynamicQuantizeLinear）回到 0.9937，仍劣于官方
// fp32 参照（0.996~0.998）。int16（±4095）的量化噪声约 2e-4，相对最终余弦的
// 影响在 1e-8 量级，等价于 fp32，但内循环仍是整数乘加——和 int8 一样快。
// 取 ±4095 是溢出上限：4095×127×1536 ≈ 8.0e8 < 2^31，int32 累加不溢出。
func matmulGeneric(w *matWeight, x []float32, t int, bias, y []float32, outDim int) {
	if w.Out != outDim {
		// 形状不符时 y 直接置为 bias，避免静默错位
		for tt := 0; tt < t; tt++ {
			row := y[tt*outDim : (tt+1)*outDim]
			if len(bias) == outDim {
				copy(row, bias)
			} else {
				clear(row)
			}
		}
		return
	}
	inDim := w.In
	n := t * inDim
	// 激活动态对称量化到 int16：先扫一遍求绝对最大值
	var amax float32
	for _, v := range x[:n] {
		if v < 0 {
			v = -v
		}
		if v > amax {
			amax = v
		}
	}
	if amax == 0 {
		for tt := 0; tt < t; tt++ {
			row := y[tt*outDim : (tt+1)*outDim]
			if len(bias) == outDim {
				copy(row, bias)
			} else {
				clear(row)
			}
		}
		return
	}
	aScale := amax / actQuantMax
	inv := 1 / aScale
	// 激活按 [inDim][t] 转置存放：GEMM 内层是"固定输入维 i、扫所有 token"，
	// 转置后这段扫描连续命中；不转置的话步长是 inDim×2B，每个 token 一次
	// cache miss。512 token 的文档 2.02s → 1.85s（收益有限，长文档是算力瓶颈）。
	xq := make([]int16, n)
	sumq := make([]int32, t) // 每个 token 的 Σ_i xq[i]，用于 zero_point 修正项
	for tt := 0; tt < t; tt++ {
		base := tt * inDim
		var s int32
		for i := 0; i < inDim; i++ {
			v := int32(math.Round(float64(x[base+i] * inv)))
			if v > actQuantMax {
				v = actQuantMax
			} else if v < -actQuantMax {
				v = -actQuantMax
			}
			xq[i*t+tt] = int16(v)
			s += v
		}
		sumq[tt] = s
	}
	work := inDim * outDim * t
	// 并行度权衡：块太小则权重按行跨步读、每块只命中几个 cache line；
	// 块太大则核数吃不满——384 维的 Q/K/V/A/O 占 4/6 的乘法，只有 384/32=12 路
	// 可用。实测最小块 32/48/64/96 对应 25.9/26.1/27.1/30.9 ms，取 32。
	workers := runtime.GOMAXPROCS(0)
	if maxW := outDim / 32; workers > maxW {
		workers = maxW
	}
	if workers <= 1 || work < 300_000 {
		matmulChunkQ(w, xq, sumq, aScale, bias, y, t, 0, outDim)
		return
	}
	chunk := (outDim + workers - 1) / workers
	var wg sync.WaitGroup
	for s := 0; s < outDim; s += chunk {
		e := s + chunk
		if e > outDim {
			e = outDim
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			matmulChunkQ(w, xq, sumq, aScale, bias, y, t, s, e)
		}(s, e)
	}
	wg.Wait()
}

// matmulChunkQ 整数 GEMM 后反量化，负责输出通道 [s,e)。
// 循环顺序：i（输入维）→ tt（token）→ idx（输出通道）。权重的第 i 行从内存
// 读一次，之后 t 个 token 复用（行宽 ≤1536B，常驻 L1），不重复打内存。
func matmulChunkQ(w *matWeight, xq []int16, sumq []int32, aScale float32,
	bias, y []float32, t, s, e int) {
	inDim, outDim := w.In, w.Out
	width := e - s
	acc := make([]int32, t*width)
	for i := 0; i < inDim; i++ {
		src := i*outDim + s
		xrow := xq[i*t : (i+1)*t]
		for tt := range xrow {
			xvi := int32(xrow[tt])
			if xvi == 0 {
				continue
			}
			arow := acc[tt*width : (tt+1)*width]
			// range 写法让编译器证明下标不越界，省掉内层每次访问的边界检查
			for idx := range arow {
				arow[idx] += xvi * int32(w.W[src+idx])
			}
		}
	}
	for tt := 0; tt < t; tt++ {
		arow := acc[tt*width : (tt+1)*width]
		yrow := y[tt*outDim+s : tt*outDim+e]
		sq := sumq[tt]
		for idx := range arow {
			o := s + idx
			deq := float32(arow[idx]-int32(w.ZP[o])*sq) * w.Scale[o]
			yrow[idx] = bias[o] + aScale*deq
		}
	}
}
