// Package embed 本地语义向量：multilingual-e5-small 的纯 Go 推理 + 向量索引。
//
// 设计约束（来自规划书）：
//   - 单二进制、零 CGO、零外部服务：模型推理与向量检索全部在进程内完成
//   - 语义相似度永远不得压过精确匹配：向量只作候选召回 + 有界排序信号
//   - 语言边界红线：纯中文查询的向量召回同样只回中文文档
package embed

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"unsafe"
)

// ---------- protobuf wire format 最小实现 ----------

type pbReader struct {
	buf []byte
	pos int
}

func (r *pbReader) eof() bool { return r.pos >= len(r.buf) }

func (r *pbReader) varint() (uint64, error) {
	var v uint64
	var shift uint
	for {
		if r.pos >= len(r.buf) {
			return 0, fmt.Errorf("onnx: varint 越界")
		}
		b := r.buf[r.pos]
		r.pos++
		v |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return v, nil
		}
		shift += 7
		if shift > 63 {
			return 0, fmt.Errorf("onnx: varint 过长")
		}
	}
}

func (r *pbReader) key() (field int, wire int, err error) {
	v, err := r.varint()
	if err != nil {
		return 0, 0, err
	}
	return int(v >> 3), int(v & 7), nil
}

func (r *pbReader) bytes() ([]byte, error) {
	n, err := r.varint()
	if err != nil {
		return nil, err
	}
	if r.pos+int(n) > len(r.buf) {
		return nil, fmt.Errorf("onnx: length-delimited 越界")
	}
	b := r.buf[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return b, nil
}

func (r *pbReader) skip(wire int) error {
	switch wire {
	case 0:
		_, err := r.varint()
		return err
	case 1:
		r.pos += 8
	case 2:
		if _, err := r.bytes(); err != nil {
			return err
		}
	case 5:
		r.pos += 4
	default:
		return fmt.Errorf("onnx: 不支持的 wire type %d", wire)
	}
	if r.pos > len(r.buf) {
		return fmt.Errorf("onnx: skip 越界")
	}
	return nil
}

// packedFloats / packedVarints 读取 packed repeated 字段
func (r *pbReader) packedFloats() ([]float32, error) {
	b, err := r.bytes()
	if err != nil {
		return nil, err
	}
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("onnx: packed float 长度 %d 不是 4 的倍数", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, nil
}

func (r *pbReader) packedVarints() ([]uint64, error) {
	b, err := r.bytes()
	if err != nil {
		return nil, err
	}
	sub := &pbReader{buf: b}
	var out []uint64
	for !sub.eof() {
		v, err := sub.varint()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// ---------- ONNX 子集 ----------

// onnxTensor 对应 TensorProto。Xenova 导出的大权重走 raw_data（小端原始字节），
// 按 data_type 解释；小张量可能走 float_data 等 packed 字段。
type onnxTensor struct {
	Name     string
	DataType int32
	Dims     []int64
	Raw      []byte    // raw_data
	Floats   []float32 // float_data（packed）
	Int32s   []int32   // int32_data（packed）
	Int64s   []int64   // int64_data（packed）
}

// Numel 元素总数
func (t *onnxTensor) Numel() int {
	n := 1
	for _, d := range t.Dims {
		n *= int(d)
	}
	return n
}

// AsFloat32 把张量数据读成 float32 切片。FLOAT 直接零拷贝视图 raw_data，
// 其它类型按值转换。调用方不得修改返回切片（raw_data 视图与文件缓冲区共享）。
func (t *onnxTensor) AsFloat32() ([]float32, error) {
	switch t.DataType {
	case 1: // FLOAT
		if len(t.Raw) > 0 {
			if len(t.Raw)%4 != 0 {
				return nil, fmt.Errorf("tensor %s: raw_data 长度 %d 不是 4 的倍数", t.Name, len(t.Raw))
			}
			return unsafeFloat32View(t.Raw), nil
		}
		return t.Floats, nil
	case 10: // FLOAT16
		if len(t.Raw)%2 != 0 {
			return nil, fmt.Errorf("tensor %s: fp16 raw 长度异常", t.Name)
		}
		out := make([]float32, len(t.Raw)/2)
		for i := range out {
			out[i] = float16ToFloat32(t.Raw[i*2:])
		}
		return out, nil
	}
	return nil, fmt.Errorf("tensor %s: 不支持的 data_type %d", t.Name, t.DataType)
}

// AsUint8 读取 UINT8 张量（量化嵌入表的 zero_point 等）。
// 标量常走 int32_data（raw_data 为空），需要兼容。
func (t *onnxTensor) AsUint8() ([]uint8, error) {
	if t.DataType != 2 {
		return nil, fmt.Errorf("tensor %s: data_type %d 不是 UINT8", t.Name, t.DataType)
	}
	if len(t.Raw) > 0 {
		return t.Raw, nil
	}
	out := make([]uint8, len(t.Int32s))
	for i, v := range t.Int32s {
		out[i] = uint8(v)
	}
	return out, nil
}

// AsInt8 读取 INT8 权重（量化模型）。同样兼容 packed int32_data 布局。
func (t *onnxTensor) AsInt8() ([]int8, error) {
	if t.DataType != 3 {
		return nil, fmt.Errorf("tensor %s: data_type %d 不是 INT8", t.Name, t.DataType)
	}
	if len(t.Raw) > 0 {
		out := make([]int8, len(t.Raw))
		for i, b := range t.Raw {
			out[i] = int8(b)
		}
		return out, nil
	}
	out := make([]int8, len(t.Int32s))
	for i, v := range t.Int32s {
		out[i] = int8(v)
	}
	return out, nil
}

type onnxNode struct {
	OpType  string
	Inputs  []string
	Outputs []string
	Attrs   map[string]any
}

type onnxGraph struct {
	Nodes        []onnxNode
	Initializers []onnxTensor
	// Inputs 仅记录模型级输入（名字 + 元素类型 + 形状），不含 initializer
	InputNames []string
}

// parseTensor 解析 TensorProto 消息体
func parseTensor(b []byte) (onnxTensor, error) {
	var t onnxTensor
	r := &pbReader{buf: b}
	for !r.eof() {
		f, w, err := r.key()
		if err != nil {
			return t, err
		}
		switch {
		case f == 1 && w == 2: // dims packed varint
			d, err := r.packedVarints()
			if err != nil {
				return t, err
			}
			t.Dims = make([]int64, len(d))
			for i, v := range d {
				t.Dims[i] = int64(v)
			}
		case f == 1 && w == 0: // dims 非 packed
			v, err := r.varint()
			if err != nil {
				return t, err
			}
			t.Dims = append(t.Dims, int64(v))
		case f == 2 && w == 0: // data_type
			v, err := r.varint()
			if err != nil {
				return t, err
			}
			t.DataType = int32(v)
		case f == 4 && w == 2: // float_data packed
			fd, err := r.packedFloats()
			if err != nil {
				return t, err
			}
			t.Floats = fd
		case f == 5 && w == 2: // int32_data packed
			d, err := r.packedVarints()
			if err != nil {
				return t, err
			}
			t.Int32s = make([]int32, len(d))
			for i, v := range d {
				t.Int32s[i] = int32(v)
			}
		case f == 7 && w == 2: // int64_data packed
			d, err := r.packedVarints()
			if err != nil {
				return t, err
			}
			t.Int64s = make([]int64, len(d))
			for i, v := range d {
				t.Int64s[i] = int64(v)
			}
		case f == 8 && w == 2: // name
			nb, err := r.bytes()
			if err != nil {
				return t, err
			}
			t.Name = string(nb)
		case f == 9 && w == 2: // raw_data
			rb, err := r.bytes()
			if err != nil {
				return t, err
			}
			t.Raw = rb
		default:
			if err := r.skip(w); err != nil {
				return t, err
			}
		}
	}
	return t, nil
}

// Raw 保存 raw_data 字节；data_type=FLOAT 时 Floats 在 loadWeights 阶段按需解码
// （raw_data 是小端 float32，直接 unsafe/切片转换即可，避免一次全量拷贝）。

// unsafeFloat32View 把 []byte 零拷贝视为 []float32（小端平台，x86/ARM 均成立）。
// 只读用途：onnx 文件缓冲区在整个进程生命周期内不变，调用方不得写回。
func unsafeFloat32View(b []byte) []float32 {
	if len(b) < 4 {
		return nil
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(&b[0])), len(b)/4)
}

// float16ToFloat32  IEEE 半精度 → 单精度
func float16ToFloat32(b []byte) float32 {
	u := uint16(b[0]) | uint16(b[1])<<8
	sign := uint32(u>>15) & 1
	exp := uint32(u>>10) & 0x1f
	frac := uint32(u & 0x3ff)
	var f uint32
	switch exp {
	case 0:
		if frac == 0 {
			f = sign << 31
		} else {
			// 次正规数
			e := uint32(127 - 15 + 1)
			for frac&0x400 == 0 {
				frac <<= 1
				e--
			}
			frac &= 0x3ff
			f = sign<<31 | e<<23 | frac<<13
		}
	case 0x1f:
		f = sign<<31 | 0xff<<23 | frac<<13
	default:
		f = sign<<31 | (exp-15+127)<<23 | frac<<13
	}
	return math.Float32frombits(f)
}

// parseGraph 解析 ModelProto，取出 graph（field 7）
func parseGraph(model []byte) (*onnxGraph, error) {
	r := &pbReader{buf: model}
	var graphBuf []byte
	for !r.eof() {
		f, w, err := r.key()
		if err != nil {
			return nil, err
		}
		if f == 7 && w == 2 {
			graphBuf, err = r.bytes()
			if err != nil {
				return nil, err
			}
			break
		}
		if err := r.skip(w); err != nil {
			return nil, err
		}
	}
	if graphBuf == nil {
		return nil, fmt.Errorf("onnx: 未找到 graph")
	}
	return parseGraphBody(graphBuf)
}

func parseGraphBody(b []byte) (*onnxGraph, error) {
	g := &onnxGraph{}
	r := &pbReader{buf: b}
	for !r.eof() {
		f, w, err := r.key()
		if err != nil {
			return nil, err
		}
		switch f {
		case 1: // node
			nb, err := r.bytes()
			if err != nil {
				return nil, err
			}
			n, err := parseNode(nb)
			if err != nil {
				return nil, err
			}
			g.Nodes = append(g.Nodes, n)
		case 5: // initializer
			tb, err := r.bytes()
			if err != nil {
				return nil, err
			}
			t, err := parseTensor(tb)
			if err != nil {
				return nil, err
			}
			g.Initializers = append(g.Initializers, t)
		case 11: // input
			ib, err := r.bytes()
			if err != nil {
				return nil, err
			}
			name, _, _, err := parseValueInfo(ib)
			if err != nil {
				return nil, err
			}
			g.InputNames = append(g.InputNames, name)
		default:
			if err := r.skip(w); err != nil {
				return nil, err
			}
		}
	}
	return g, nil
}

func parseNode(b []byte) (onnxNode, error) {
	var n onnxNode
	n.Attrs = map[string]any{}
	r := &pbReader{buf: b}
	for !r.eof() {
		f, w, err := r.key()
		if err != nil {
			return n, err
		}
		switch f {
		case 1: // input
			s, err := r.bytes()
			if err != nil {
				return n, err
			}
			n.Inputs = append(n.Inputs, string(s))
		case 2: // output
			s, err := r.bytes()
			if err != nil {
				return n, err
			}
			n.Outputs = append(n.Outputs, string(s))
		case 4: // op_type
			s, err := r.bytes()
			if err != nil {
				return n, err
			}
			n.OpType = string(s)
		case 5: // attribute
			ab, err := r.bytes()
			if err != nil {
				return n, err
			}
			if err := parseAttr(ab, n.Attrs); err != nil {
				return n, err
			}
		default:
			if err := r.skip(w); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// parseAttr 只取前向推理需要的：整数、浮点、字符串
func parseAttr(b []byte, out map[string]any) error {
	r := &pbReader{buf: b}
	var name string
	var kind int
	for !r.eof() {
		f, w, err := r.key()
		if err != nil {
			return err
		}
		switch {
		case f == 1 && w == 2: // name
			nb, err := r.bytes()
			if err != nil {
				return err
			}
			name = string(nb)
		case f == 20 && w == 0: // type
			v, err := r.varint()
			if err != nil {
				return err
			}
			kind = int(v)
		case f == 2 && w == 0: // i (int)
			v, err := r.varint()
			if err != nil {
				return err
			}
			out[name] = int64(v)
		case f == 3 && w == 5: // f (float)
			r.pos += 4
		case f == 4 && w == 2: // s (string)
			s, err := r.bytes()
			if err != nil {
				return err
			}
			out[name] = string(s)
		case f == 8 && w == 2: // ints
			d, err := r.packedVarints()
			if err != nil {
				return err
			}
			out[name] = d
		case f == 7 && w == 2: // floats
			fl, err := r.packedFloats()
			if err != nil {
				return err
			}
			out[name] = fl
		case f == 5 && w == 2: // t（TensorProto，Constant 节点的值）
			tb, err := r.bytes()
			if err != nil {
				return err
			}
			t, err := parseTensor(tb)
			if err != nil {
				return err
			}
			out[name] = &t
		default:
			if err := r.skip(w); err != nil {
				return err
			}
		}
	}
	_ = kind
	return nil
}

// parseValueInfo 返回 (name, elemType, dims)。dims 中 -1 表示动态维
func parseValueInfo(b []byte) (string, int32, []int64, error) {
	r := &pbReader{buf: b}
	var name string
	var elem int32
	var dims []int64
	for !r.eof() {
		f, w, err := r.key()
		if err != nil {
			return "", 0, nil, err
		}
		switch f {
		case 1:
			nb, err := r.bytes()
			if err != nil {
				return "", 0, nil, err
			}
			name = string(nb)
		case 2: // TypeProto
			tb, err := r.bytes()
			if err != nil {
				return "", 0, nil, err
			}
			elem, dims, err = parseTypeProto(tb)
			if err != nil {
				return "", 0, nil, err
			}
		default:
			if err := r.skip(w); err != nil {
				return "", 0, nil, err
			}
		}
	}
	return name, elem, dims, nil
}

func parseTypeProto(b []byte) (int32, []int64, error) {
	r := &pbReader{buf: b}
	for !r.eof() {
		f, w, err := r.key()
		if err != nil {
			return 0, nil, err
		}
		if f == 1 && w == 2 { // tensor_type
			tb, err := r.bytes()
			if err != nil {
				return 0, nil, err
			}
			tr := &pbReader{buf: tb}
			var elem int32
			var dims []int64
			for !tr.eof() {
				tf, tw, err := tr.key()
				if err != nil {
					return 0, nil, err
				}
				switch tf {
				case 1: // elem_type
					v, err := tr.varint()
					if err != nil {
						return 0, nil, err
					}
					elem = int32(v)
				case 2: // shape
					sb, err := tr.bytes()
					if err != nil {
						return 0, nil, err
					}
					sr := &pbReader{buf: sb}
					for !sr.eof() {
						df, dw, err := sr.key()
						if err != nil {
							return 0, nil, err
						}
						if df == 1 && dw == 2 { // dim
							db, err := sr.bytes()
							if err != nil {
								return 0, nil, err
							}
							dr := &pbReader{buf: db}
							for !dr.eof() {
								dd, ddw, err := dr.key()
								if err != nil {
									return 0, nil, err
								}
								if dd == 1 && ddw == 0 { // dim_value
									v, err := dr.varint()
									if err != nil {
										return 0, nil, err
									}
									dims = append(dims, int64(v))
								} else if err := dr.skip(ddw); err != nil {
									return 0, nil, err
								}
							}
						} else if err := sr.skip(dw); err != nil {
							return 0, nil, err
						}
					}
				default:
					if err := tr.skip(tw); err != nil {
						return 0, nil, err
					}
				}
			}
			return elem, dims, nil
		}
		if err := r.skip(w); err != nil {
			return 0, nil, err
		}
	}
	return 0, nil, fmt.Errorf("onnx: TypeProto 缺少 tensor_type")
}

// LoadGraph 读取 .onnx 文件并解析出图结构（不展开权重）
func LoadGraph(path string) (*onnxGraph, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseGraph(b)
}
