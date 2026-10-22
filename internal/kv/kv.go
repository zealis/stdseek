// Package kv 提供基于 Pebble 的本地 KV 存储，用于保存网页原文快照。
// 对应规划书「KV: Pebble（替代 sese-engine 文件存储）」与功能 D「证据链」的原文回溯基础。
package kv

import (
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble"
)

// Store 包装 pebble.DB，键为归一化 URL（page:<urlkey>）。
type Store struct {
	db *pebble.DB
}

// Open 打开（不存在则创建）指定目录的 KV 库。
func Open(path string) (*Store, error) {
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("open pebble %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Put 写入键值（NoSync：快照可重建，不追求每次落盘持久性）。
func (s *Store) Put(key string, val []byte) error {
	return s.db.Set([]byte(key), val, pebble.NoSync)
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
	return s.db.Delete([]byte(key), pebble.NoSync)
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// Size 返回库占用字节数（近似，用于指标/后台展示）。
func (s *Store) Size() int64 {
	m := s.db.Metrics()
	return int64(m.DiskSpaceUsage())
}

// Key 生成快照键。urlkey 由调用方归一化，避免 http/https 重复存两份。
func Key(urlKey string) string { return "page:" + urlKey }

// TTL 说明：MVP 不做过期清理，快照随重新抓取覆盖；后续可按 fetched_at 做 GC。
