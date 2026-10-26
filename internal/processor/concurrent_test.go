package processor

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestProcessConcurrent 验证 Process 可被多个 goroutine 同时调用。
//
// 为什么需要这条：reindex 并行化的前提是 processor 无共享可变状态。
// Processor 只持有 gse.Segmenter，包级变量全是 init 后只读的正则/切片，
// 唯一嫌疑是 seg.Cut。这条测试用 -race 跑，有数据竞争会立刻炸。
// 注意：gse 官方不保证 Cut 并发安全，所以这条也是"升级 gse 时的哨兵"。
func TestProcessConcurrent(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	// 构造 8 篇不同的文档，避免缓存/短路让测试失去意义
	docs := make([][]byte, 8)
	for i := range docs {
		body := strings.Repeat(fmt.Sprintf("第%d篇测试文档，讨论分布式系统的一致性哈希与容错机制。", i), 40)
		docs[i] = []byte("<!doctype html><html><head><title>并发测试" + fmt.Sprint(i) +
			"</title></head><body><article><p>" + body + "</p></article></body></html>")
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				d := docs[(g+i)%len(docs)]
				res, err := p.Process("https://example.com/concurrent", d, Options{})
				if err != nil {
					t.Errorf("goroutine %d: %v", g, err)
					return
				}
				if res.Body == "" {
					t.Errorf("goroutine %d: 正文为空", g)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
