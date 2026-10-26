package lang

import "testing"

func TestDetect(t *testing.T) {
	cases := []struct {
		q    string
		want string
	}{
		{"单元测试", Zh},
		{"分布式系统怎么学习", Zh},
		{"鸿蒙长列表滑动卡顿", Zh},
		{"docker buildx", En},
		{"kubernetes", En},
		{"go 语言规范", "mix"},  // 中英混杂：允许跨语言召回
		{"PHP 命名空间", "mix"}, // 同上
		{"docker 构建镜像", "mix"},
		{"2026-10-23", "none"}, // 纯数字符号
		{"", "none"},
	}
	for _, c := range cases {
		if got := Detect(c.q); got != c.want {
			t.Errorf("Detect(%q)=%q want %q", c.q, got, c.want)
		}
	}
}

func TestDocLang(t *testing.T) {
	cases := []struct {
		name        string
		title, body string
		want        string
	}{
		{"纯中文文章", "什么是分布式系统", "分布式系统是若干独立计算机的集合", Zh},
		// 技术文档常带大量英文术语，不能因此判成英文
		{"夹带英文术语的中文文章", "Kubernetes 的问题和局限性",
			"Kubernetes 的调度器在高并发场景下会遇到 etcd 写入瓶颈，这是社区公认的设计取舍", Zh},
		{"纯英文文档", "PHP: Namespaces - Manual",
			"Namespaces allow grouping of related classes interfaces functions and constants", En},
		{"空正文", "", "", Zh}, // 无字母归中文：本站中文优先
	}
	for _, c := range cases {
		if got := DocLang(c.title, c.body); got != c.want {
			t.Errorf("%s: DocLang()=%q want %q", c.name, got, c.want)
		}
	}
}

// MustZh 只对纯中文查询为真——这是边界的唯一入口。
func TestMustZh(t *testing.T) {
	if !MustZh("单元测试") {
		t.Error("纯中文查询应要求只召回中文")
	}
	for _, q := range []string{"go 语言规范", "docker buildx", ""} {
		if MustZh(q) {
			t.Errorf("%q 不应触发中文边界", q)
		}
	}
}
