package embed

import (
	"path/filepath"
	"strings"
	"testing"
)

func benchModel(b *testing.B) *Model {
	b.Helper()
	root, err := repoRoot()
	if err != nil {
		b.Skip(err.Error())
	}
	m, err := Load(filepath.Join(root, "tmpdata/spike/multilingual-e5-small"))
	if err != nil {
		b.Skip("模型未下载：" + err.Error())
	}
	return m
}

// BenchmarkEmbedQuery 查询侧：短文本（6~8 token）。
// 单次前向要把 118MB 的 int8 权重全过一遍，短序列下是"瘦矩阵"GEMM，
// 耗时与 token 数关系不大，主要由权重读取 + 整数乘加决定。
func BenchmarkEmbedQuery(b *testing.B) {
	m := benchModel(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Embed("单元测试")
	}
}

// BenchmarkEmbedDoc 索引侧：长文本（截断到 512 token）。
func BenchmarkEmbedDoc(b *testing.B) {
	m := benchModel(b)
	doc := strings.Repeat("这是一段用于测试索引性能的中文技术文档内容，包含接口设计与实现细节。", 60)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Embed(doc)
	}
}
