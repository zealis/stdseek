package snapshot

import (
	"context"
	"errors"
	"fmt"
)

// ErrRemote 云端回源失败。要和「归档里没有这一页」分开：
// 前者是「网络/权限出问题了，过会儿再试」，后者是「这页确实没抓过」，
// 界面上一个是错误提示，一个是「补抓」按钮，混在一起用户只会以为数据丢了。
var ErrRemote = errors.New("云端读取快照失败")

// Remote 云端只读视图（由 backup.Reader 实现）。
// 定义在 snapshot 侧是为了不反向依赖 backup：backup 已经 import 本包。
type Remote interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Has(ctx context.Context, key string) (bool, error)
}

// NewCloud 给本地存储加一层「云端回源」。
//
// cloudOnly 返回 true 时（后台选了「只留云端」），本地没有就去云端取；
// 返回 false 时行为与纯本地一致——读路径不因为配置了桶就悄悄变成网络请求。
//
// 刻意不做「取回后写回本地」：这个模式的全部意义就是本地不留副本，
// 偷偷回填会让磁盘又长回去，且用户以为已经清空了。
type cloudStore struct {
	local     Store
	remote    Remote
	cloudOnly func() bool
}

func NewCloud(local Store, remote Remote, cloudOnly func() bool) Store {
	if local == nil {
		return nil
	}
	if remote == nil {
		return local
	}
	return &cloudStore{local: local, remote: remote, cloudOnly: cloudOnly}
}

func (c *cloudStore) online() bool {
	return c.remote != nil && c.cloudOnly != nil && c.cloudOnly()
}

func (c *cloudStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	val, ok, err := c.local.Get(ctx, key)
	if err != nil || ok {
		return val, ok, err
	}
	if !c.online() {
		return nil, false, nil
	}
	val, ok, err = c.remote.Get(ctx, key)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrRemote, err)
	}
	return val, ok, nil
}

func (c *cloudStore) Has(ctx context.Context, key string) (bool, error) {
	ok, err := c.local.Has(ctx, key)
	if err != nil || ok {
		return ok, err
	}
	if !c.online() {
		return false, nil
	}
	ok, err = c.remote.Has(ctx, key)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrRemote, err)
	}
	return ok, nil
}

func (c *cloudStore) Put(ctx context.Context, key string, val []byte) error {
	return c.local.Put(ctx, key, val)
}

func (c *cloudStore) DeleteMany(ctx context.Context, keys []string) error {
	return c.local.DeleteMany(ctx, keys)
}

func (c *cloudStore) Count(ctx context.Context, prefix string) (int, error) {
	return c.local.Count(ctx, prefix)
}

func (c *cloudStore) Size() int64 { return c.local.Size() }

func (c *cloudStore) Version() int64 { return c.local.Version() }

// Scan / Compact 只在本地做：维护动作（孤儿统计、压缩）看的是本地这一层，
// 云端归档的清理是另一个动作，不能靠一次扫描顺手删掉 128MB 的包。
func (c *cloudStore) Scan(ctx context.Context, prefix string, fn func(key string, val []byte) error) error {
	if mt, ok := c.local.(Maintenance); ok {
		return mt.Scan(ctx, prefix, fn)
	}
	return ErrMaintenanceUnsupported
}

func (c *cloudStore) Flush(ctx context.Context) error {
	if mt, ok := c.local.(Maintenance); ok {
		return mt.Flush(ctx)
	}
	return nil
}

func (c *cloudStore) Compact(ctx context.Context, prefix string) error {
	if mt, ok := c.local.(Maintenance); ok {
		return mt.Compact(ctx, prefix)
	}
	return ErrMaintenanceUnsupported
}
