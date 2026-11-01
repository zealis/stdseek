package api

import (
	"testing"

	"github.com/zealis/stdseek/internal/ua"
)

// 「浏览器 UA」开关的落库键必须与 crawler 读的那个字符串一致。
// 两处各写一份字面量的话，拼错一个字母的表现是「开关打开但抓取不变」，
// 现场几乎查不出来——所以这里钉死。
func TestBrowserUAFeatureRegistered(t *testing.T) {
	def, ok := featureByKey("browser_ua")
	if !ok {
		t.Fatal("featureDefs 里没有登记 browser_ua")
	}
	if def.DBKey != ua.BrowserFeatureKey {
		t.Fatalf("落库键漂了：featureDefs 用 %q，crawler 读 %q", def.DBKey, ua.BrowserFeatureKey)
	}
	if !def.Danger {
		t.Error("伪装成浏览器属于风险项，后台应该用告警色渲染")
	}
	if def.Default {
		t.Error("默认必须关：不声明自己是爬虫应当由管理员显式选择")
	}
}

// 开关卡片只写「这个开关做什么」，一句话说完。
//
// 规则解释（什么时候触发、调用几次、缓存多久、失败怎么退回）属于文档，
// 写进卡片只会让设置页变成一段说明文——2026-10-31 用户连续两次要求删减，
// 这里用测试把上限钉住，避免以后又长回来。
func TestFeatureDescStaysShort(t *testing.T) {
	const max = 30
	for _, f := range featureDefs {
		if n := len([]rune(f.Desc)); n > max {
			t.Errorf("%s 的说明 %d 字，超过 %d：%s", f.Key, n, max, f.Desc)
		}
		if f.Desc == "" {
			t.Errorf("%s 缺少说明", f.Key)
		}
	}
}
