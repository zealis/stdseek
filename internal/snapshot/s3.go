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
//
// 先试 HeadBucket；失败再 HEAD 一个探测对象兜底。原因：B2 的「桶级应用密钥」
// 没有列桶权限，HeadBucket 可能回 403，但读写在同一个桶里其实是通的。
// 只要探测对象返回 200 或 404（没有这个对象），就说明签名与密钥有效。
// 两次都失败时返回 HeadBucket 的原始错误——它更能说明是端点/区域/桶名的问题。
func TestS3(ctx context.Context, c S3Config, timeout time.Duration) error {
	if err := c.Validate(); err != nil {
		return err
	}
	client := objstore.New(c, timeout)
	headErr := client.HeadBucket(ctx)
	if headErr == nil {
		return nil
	}
	if probeErr := client.Probe(ctx, c.ObjectKey(".probe")); probeErr == nil {
		return nil
	}
	return headErr
}
