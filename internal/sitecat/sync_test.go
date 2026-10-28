package sitecat

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// 分类词表在两处各写了一份：Go 侧（提示词、参数校验）与前端
// web/src/app/core/api.ts（下拉、默认间隔）。只改一边的症状很隐蔽——
// 「能筛但下拉里没这个名字」，或者下拉选了却报 400。
// 这里直接读前端源文件比对，让它不可能悄悄漂。
func TestFrontendCategoriesInSync(t *testing.T) {
	path := filepath.Join("..", "..", "web", "src", "app", "core", "api.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("读不到前端源码（跳过一致性检查）：%v", err)
	}
	src := string(raw)

	start := regexp.MustCompile(`export const CATEGORIES = \[`).FindStringIndex(src)
	if start == nil {
		t.Fatalf("%s 里找不到 CATEGORIES 数组", path)
	}
	end := regexp.MustCompile(`(?m)^\]\s*as const;`).FindStringIndex(src[start[1]:])
	if end == nil {
		t.Fatalf("%s 里找不到 CATEGORIES 数组结尾", path)
	}
	body := src[start[1] : start[1]+end[1]]

	item := regexp.MustCompile(`value:\s*'([^']+)'[^}]*?interval:\s*(\d+)`)
	matches := item.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		t.Fatalf("没从 %s 解析出任何分类", path)
	}

	want := List
	if len(matches) != len(want) {
		t.Fatalf("分类数量不一致：前端 %d，Go %d", len(matches), len(want))
	}
	for i, m := range matches {
		if m[1] != want[i].Key {
			t.Errorf("第 %d 个分类 key 不一致：前端 %q，Go %q（顺序也要一致）", i, m[1], want[i].Key)
		}
		iv, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("第 %d 个分类的 interval 不是数字：%q", i, m[2])
		}
		if iv != IntervalFor(want[i].Key) {
			t.Errorf("分类 %q 默认间隔不一致：前端 %d，Go %d", want[i].Key, iv, IntervalFor(want[i].Key))
		}
	}
}
