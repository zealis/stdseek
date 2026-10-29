package snapshot

import "context"

// KVSource 不带 context 的快照读写接口，给重建索引/修复校验这类批量任务用。
//
// 为什么不直接用 Store：批量任务里每篇都要读一次，逐次传 context 会把签名
// 改得到处都是；而它们本来就是「跑完为止」的长任务，一个背景 ctx 足够。
// kv.Store 天然满足这个接口，所以本地路径一行都不用改。
type KVSource interface {
	Get(key string) ([]byte, bool, error)
	Scan(prefix string, fn func(key string, val []byte) error) error
}

// KVAdapter 把 Store 适配成 KVSource（云端模式下 reads 会回源）。
type KVAdapter struct {
	s   Store
	ctx context.Context
}

func NewKVAdapter(s Store) *KVAdapter {
	return &KVAdapter{s: s, ctx: context.Background()}
}

func (a *KVAdapter) Get(key string) ([]byte, bool, error) {
	if a == nil || a.s == nil {
		return nil, false, nil
	}
	return a.s.Get(a.ctx, key)
}

func (a *KVAdapter) Scan(prefix string, fn func(key string, val []byte) error) error {
	if a == nil || a.s == nil {
		return nil
	}
	if mt, ok := a.s.(Maintenance); ok {
		return mt.Scan(a.ctx, prefix, fn)
	}
	return ErrMaintenanceUnsupported
}
