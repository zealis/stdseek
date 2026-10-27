// Package kv 提供基于 Pebble 的本地 KV 存储，用于保存网页原文快照。
// 对应规划书「KV: Pebble（替代 sese-engine 文件存储）」与功能 D「证据链」的原文回溯基础。
package kv

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/cockroachdb/pebble"
)

// Store 包装 pebble.DB，键为归一化 URL（page:<urlkey>）。
type Store struct {
	db *pebble.DB
	// ver 存储代数：任何 Put/Delete 成功后 +1。
	// 上层缓存（api 的证据片段缓存）靠它判断"快照可能变了"，
	// 与 index.Version() 同一套思路——快照被重新抓取后，
	// 缓存里的片段必须作废，否则用户看到的是旧正文的证据。
	ver atomic.Int64
}

// Version 当前存储代数。只增不减，具体数值无意义，只用于比较是否变化。
func (s *Store) Version() int64 { return s.ver.Load() }

// Open 打开（不存在则创建）指定目录的 KV 库。
//
// DisableWAL：Pebble 的 WAL 会在写入量达到阈值时轮转出新文件（000091.log、000150.log…），
// 本机环境对这类高频新建写入返回 Access is denied，直接把进程打崩（真实复现两次）。
// 快照本身可由重新抓取重建，牺牲崩溃时的持久性换取写入稳定。
// 正常 Close 时 memtable 会 flush 成 SST，数据不丢。
func Open(path string) (*Store, error) {
	db, err := pebble.Open(path, &pebble.Options{DisableWAL: true})
	if err != nil {
		return nil, fmt.Errorf("open pebble %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Put 写入键值（NoSync：快照可重建，不追求每次落盘持久性）。
func (s *Store) Put(key string, val []byte) error {
	if err := s.db.Set([]byte(key), val, pebble.NoSync); err != nil {
		return err
	}
	s.ver.Add(1)
	return nil
}

// Get 读取键值；不存在时返回 found=false。
func (s *Store) Get(key string) ([]byte, bool, error) {
	v, closer, err := s.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer closer.Close()
	out := make([]byte, len(v))
	copy(out, v)
	return out, true, nil
}

// Has 只判断键是否存在，不拷贝内容。
//
// 结果页要标出「这页爬虫没抓过」，用 Get 会把几十 KB 正文整段拷进新切片再扔掉；
// 这里拿到 closer 立刻关掉，省掉那次拷贝。
func (s *Store) Has(key string) (bool, error) {
	_, closer, err := s.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = closer.Close()
	return true, nil
}

// Scan 遍历指定前缀的所有键值（按 key 升序）。
func (s *Store) Scan(prefix string, fn func(key string, val []byte) error) error {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix + "\uffff"),
	})
	if err != nil {
		return err
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		if err := fn(string(iter.Key()), iter.Value()); err != nil {
			return err
		}
	}
	return iter.Error()
}

// Delete 删除键。
func (s *Store) Delete(key string) error {
	if err := s.db.Delete([]byte(key), pebble.NoSync); err != nil {
		return err
	}
	s.ver.Add(1)
	return nil
}

// DeleteMany 批量删除（后台清理快照用）。一次 Batch 提交，避免逐键 fsync。
func (s *Store) DeleteMany(keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	b := s.db.NewBatch()
	defer b.Close()
	for _, k := range keys {
		if err := b.Delete([]byte(k), pebble.NoSync); err != nil {
			return err
		}
	}
	if err := b.Commit(pebble.NoSync); err != nil {
		return err
	}
	s.ver.Add(1)
	return nil
}

// Count 统计前缀下的键数量（不读值，只走键）。
func (s *Store) Count(prefix string) (int, error) {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix + "\uffff"),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	return n, iter.Error()
}

// Compact 触发前缀范围内的合并，让删除的键真正释放磁盘。
//
// 只做 Delete 不做 Compact 的话 DiskSpaceUsage 不会降——墓碑写下去，
// 空间要等后台 compaction 自然轮到才回收，用户会以为"清理没用"。
func (s *Store) Compact(prefix string) error {
	if prefix == "" {
		return s.db.Compact([]byte{0x00}, []byte{0xff, 0xff, 0xff, 0xff}, true)
	}
	return s.db.Compact([]byte(prefix), []byte(prefix+"\uffff"), true)
}

// Close 关闭数据库。
// DisableWAL 模式下必须先 Flush：WAL 本是唯一的持久化路径，关掉后不主动 flush，
// memtable 里的写入会随进程退出一起消失（TestPersistenceAcrossReopen 实测）。
func (s *Store) Close() error {
	if err := s.db.Flush(); err != nil {
		return err
	}
	return s.db.Close()
}

// Size 返回库占用字节数（近似，用于指标/后台展示）。
func (s *Store) Size() int64 {
	m := s.db.Metrics()
	return int64(m.DiskSpaceUsage())
}

// Key 生成快照键。urlkey 由调用方归一化，避免 http/https 重复存两份。
func Key(urlKey string) string { return "page:" + urlKey }

// 过期策略：不做自动 TTL。快照随重新抓取覆盖；孤儿（页面已从索引删除）由后台
// 「功能开关 → 原文快照」里的清理动作手动回收——自动 GC 需要判断"多久没被访问"，
// 而这个信号在当前规模下还不可靠，误删比占空间更糟。
