package snapshot

import (
	"context"
	"time"

	"voidxo/internal/objstore"
)

// S3Config 与对象存储客户端共用一份配置定义。
// 类型别名而不是复制结构体：两处各写一遍字段，加一个字段就会漏一边。
type S3Config = objstore.Config

// TestS3 探测桶是否存在、凭据是否有权限。
func TestS3(ctx context.Context, c S3Config, timeout time.Duration) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return objstore.New(c, timeout).HeadBucket(ctx)
}
