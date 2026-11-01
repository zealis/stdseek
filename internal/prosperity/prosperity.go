// Package prosperity 实现反向链接繁荣度统计（对应 sese-engine 的反向链接统计器）。
//
// 算法：以「目标 URL 的路径前缀」为节点建一棵轻量 URL 树，统每个前缀被多少个
// 不同源域名引用；引用域名越多越繁荣。正着用是排序加成，反着用是「深网发现」。
//
// 设计取舍：
//   - MVP 用内存 map 精确计数，节点数/每节点域名数设上限，超限后冻结新建（sese 同策略）；
//   - 分数按 log1p 归一化到基准值 Base，避免头部站点能量无限膨胀。
package prosperity

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/zealis/stdseek/internal/store"
	"github.com/zealis/stdseek/internal/urlkey"
)

// Base 反向链接基准值，与 sese-engine 配置一致（分析.py 的 反向链接基准值）。
const Base = 200000.0

// 内存保护上限：单次统计最多 20 万节点、每节点最多记录 5000 个引用域名。
const (
	maxNodes         = 200000
	maxDomainsPerKey = 5000
)

// Stat 统计摘要（日志与后台展示用）。
type Stat struct {
	Pairs      int // 出链条数
	Keys       int // URL 前缀节点数
	MaxRef     int // 单节点最大引用域名数
	FrozenKeys int // 因超限停止新建的节点数
}

// Compute 统计繁荣度并返回可直接落库的记录。
func Compute(ctx context.Context, st *store.Store) ([]store.ProsperityRow, Stat, error) {
	pairs, err := st.AllOutlinks(ctx)
	if err != nil {
		return nil, Stat{}, fmt.Errorf("load outlinks: %w", err)
	}
	stat := Stat{Pairs: len(pairs)}
	if len(pairs) == 0 {
		return nil, stat, nil
	}

	refs := make(map[string]map[string]struct{}, 1024)
	for _, p := range pairs {
		if err := ctx.Err(); err != nil {
			return nil, stat, err
		}
		for _, k := range Keys(p.ToURL) {
			set, ok := refs[k]
			if !ok {
				if len(refs) >= maxNodes {
					stat.FrozenKeys++
					continue
				}
				set = make(map[string]struct{})
				refs[k] = set
			}
			if len(set) >= maxDomainsPerKey {
				continue
			}
			set[p.FromDomain] = struct{}{}
		}
	}

	maxRef := 0
	for _, set := range refs {
		if len(set) > maxRef {
			maxRef = len(set)
		}
	}
	stat.Keys = len(refs)
	stat.MaxRef = maxRef
	if maxRef == 0 {
		return nil, stat, nil
	}

	rows := make([]store.ProsperityRow, 0, len(refs))
	for k, set := range refs {
		n := len(set)
		score := Base * math.Log1p(float64(n)) / math.Log1p(float64(maxRef))
		rows = append(rows, store.ProsperityRow{Key: k, Score: score, RefDomains: n})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	return rows, stat, nil
}

// Refresh 统计并整体替换 prosperity 表。
func Refresh(ctx context.Context, st *store.Store) (Stat, error) {
	rows, stat, err := Compute(ctx, st)
	if err != nil {
		return stat, err
	}
	if err := st.ReplaceProsperity(ctx, rows); err != nil {
		return stat, err
	}
	return stat, nil
}

// Key 归一化 URL 为繁荣度 key（委托 urlkey，保持包内调用方便）。
func Key(raw string) string { return urlkey.Key(raw) }

// Keys 返回 URL 的全部前缀 key，与 sese-engine「每多一个斜线变成子节点」一致。
func Keys(raw string) []string { return urlkey.Keys(raw) }
