package embed

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// onnxTensorAlias / aliasOf 仅为测试日志避免拷贝大张量
type onnxTensorAlias struct {
	Name     string
	DataType int32
	Dims     []int64
}

func aliasOf(t *onnxTensor) *onnxTensorAlias {
	return &onnxTensorAlias{Name: t.Name, DataType: t.DataType, Dims: t.Dims}
}

// TestIntrospectGraph 解析真实模型文件，打印图结构与权重清单。
// 用手头的 e5-small 量化版跑：go test ./internal/embed/ -run Introspect -v
// 这是前向实现的依据——先看清图再写代码，不猜。
func TestIntrospectGraph(t *testing.T) {
	path := findModel(t)
	g, err := LoadGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("模型输入: %v", g.InputNames)

	ops := map[string]int{}
	for _, n := range g.Nodes {
		ops[n.OpType]++
	}
	keys := make([]string, 0, len(ops))
	for k := range ops {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t.Logf("节点 %d 个，算子分布:", len(g.Nodes))
	for _, k := range keys {
		t.Logf("   %-22s %d", k, ops[k])
	}

	// 追 position_embeddings 的 Gather 索引来源
	for _, nd := range g.Nodes {
		joined := strings.Join(nd.Inputs, ",")
		if strings.Contains(joined, "position_embeddings") {
			t.Logf("   POS %-14s in=%v out=%v", nd.OpType, nd.Inputs, nd.Outputs)
		}
	}
	// 量化 scale 的接线 + scale/zp 初始化的形状
	for _, nd := range g.Nodes {
		if strings.Contains(strings.Join(nd.Outputs, ","), "quant_scales") {
			t.Logf("   SC %-8s in=%v out=%v", nd.OpType, nd.Inputs, nd.Outputs)
		}
	}
	for _, w := range g.Initializers {
		if strings.Contains(w.Name, "_scale") || strings.Contains(w.Name, "_zero_point") {
			if strings.Contains(w.Name, "embeddings") || strings.Contains(w.Name, "1525") || strings.Contains(w.Name, "1536") {
				t.Logf("   QP %-58s dtype=%-2d dims=%v", w.Name, w.DataType, w.Dims)
			}
		}
	}
	t.Logf("initializer %d 个:", len(g.Initializers))
	for _, w := range g.Initializers {
		t.Logf("   %-56s dtype=%-2d dims=%v numel=%d", w.Name, w.DataType, w.Dims, w.Numel())
	}
}

// findModel 定位测试用模型。go test 的工作目录是包目录，先向上找仓库根。
func findModel(t *testing.T) string {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Skip(err.Error())
	}
	for _, rel := range []string{
		"tmpdata/spike/multilingual-e5-small/onnx/model_quantized.onnx",
		"tmpdata/spike/multilingual-e5-small/onnx/model.onnx",
		"tmpdata/spike/gte2/onnx/model.onnx",
	} {
		p := filepath.Join(root, rel)
		if fileExists(p) {
			return p
		}
	}
	t.Skip("仓库内找不到模型 onnx 文件")
	return ""
}

// repoRoot 从当前目录向上找含 go.mod 的目录
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if fileExists(filepath.Join(dir, "go.mod")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("找不到仓库根（go.mod）")
		}
		dir = parent
	}
}
