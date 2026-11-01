// Package snapshot 定义原文快照的存储接口。
//
// 现在只有一个本地实现（Pebble KV），默认零依赖、毫秒级读取；
// 接口带 context，是为了后续接 S3/B2 时不改动调用方——
// 远程实现需要超时、取消与重试，而本地实现直接忽略 context。
package snapshot

import (
	"context"
	"errors"

	"github.com/zealis/stdseek/internal/kv"
)

// ErrMaintenanceUnsupported 当前后端不支持枚举/压缩（远程对象存储应走 manifest）。
var ErrMaintenanceUnsupported = errors.New("当前存储后端不支持该维护操作")

// Store 原文快照存储。key 由 kv.Key(urlkey.Key(url)) 生成，调用方不变。
type Store interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, val []byte) error
	DeleteMany(ctx context.Context, keys []string) error
	Count(ctx context.Context, prefix string) (int, error)
	Has(ctx context.Context, key string) (bool, error)
	Size() int64
	Version() int64
}

// Maintenance 本地存储的维护能力（枚举/压缩）。
// 不放进 Store：远程对象存储上逐对象扫描正是「小文件问题」本身，
// 后续 S3 实现应该用 manifest 维护，而不是被迫实现一个昂贵的 Scan。
type Maintenance interface {
	Scan(ctx context.Context, prefix string, fn func(key string, val []byte) error) error
	Compact(ctx context.Context, prefix string) error
	// Flush 把最近的删除落盘。云端为主模式删完本地要立刻生效，
	// 否则强杀进程后本地副本会「复活」，看起来像清理没做。
	Flush(ctx context.Context) error
}

// local 把现有的 Pebble KV 包成 Store；读写行为与改造前完全一致。
type local struct {
	kv *kv.Store
}

// NewLocal 包装本地 KV；传入 nil 时返回 nil（表示未启用快照）。
// 返回 nil 接口而不是包装 nil 指针：调用方用 `s.snap == nil` 判断未启用。
func NewLocal(k *kv.Store) Store {
	if k == nil {
		return nil
	}
	return &local{kv: k}
}

func (l *local) Get(_ context.Context, key string) ([]byte, bool, error) {
	return l.kv.Get(key)
}

func (l *local) Put(_ context.Context, key string, val []byte) error {
	return l.kv.Put(key, val)
}

func (l *local) DeleteMany(_ context.Context, keys []string) error {
	return l.kv.DeleteMany(keys)
}

func (l *local) Count(_ context.Context, prefix string) (int, error) {
	return l.kv.Count(prefix)
}

func (l *local) Has(_ context.Context, key string) (bool, error) {
	return l.kv.Has(key)
}

func (l *local) Scan(_ context.Context, prefix string, fn func(key string, val []byte) error) error {
	return l.kv.Scan(prefix, fn)
}

func (l *local) Compact(_ context.Context, prefix string) error {
	return l.kv.Compact(prefix)
}

func (l *local) Flush(_ context.Context) error { return l.kv.Flush() }

func (l *local) Size() int64 { return l.kv.Size() }

func (l *local) Version() int64 { return l.kv.Version() }
