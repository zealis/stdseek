package processor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zealis/stdseek/internal/kv"
)

// TestStructHashShellExcluded SPA 空壳必须返回 0 哨兵。
//
// 这是 structMinPaths 存在的理由：JS 渲染页面的 HTML 只有一个骨架，
// 结构全都一样。实测跨站距离为 0 的对全是这类（路径数 10）
// ——云厂商与资讯平台会被判成同一套模板。
func TestStructHashShellExcluded(t *testing.T) {
	shell := `<!doctype html><html><head><meta charset="utf-8"><title>x</title></head>` +
		`<body><div id="app"></div><script src="/bundle.js"></script></body></html>`
	if got := StructHash([]byte(shell)); got != 0 {
		t.Errorf("SPA 空壳应返回 0，实际 %d（路径数 %d）", got, len(StructPaths([]byte(shell))))
	}
	if got := StructHash([]byte("纯文本快照，没有 HTML 结构")); got != 0 {
		t.Errorf("纯文本应返回 0，实际 %d", got)
	}
	if got := StructHash(nil); got != 0 {
		t.Errorf("空输入应返回 0，实际 %d", got)
	}
}

// TestStructHashTemplateIdentity 同一套模板、不同正文 → 结构指纹相同。
// 这正是它相对正文指纹的独特价值：正文被改写后仍能认出「同一套模板」。
func TestStructHashTemplateIdentity(t *testing.T) {
	const tpl = `<!doctype html><html><head><title>%s</title></head><body>` +
		`<header><nav><ol><li><a href="/">首页</a></li><li><a href="/docs">文档</a></li></ol></nav></header>` +
		`<main><article><h1>%s</h1><section><h2>概述</h2><table><thead><tr><th>字段</th><th>类型</th>` +
		`</tr></thead><tbody><tr><td>id</td><td>int</td></tr><tr><td>name</td><td>text</td></tr></tbody></table>` +
		`<h2>用法</h2><dl><dt>参数</dt><dd>说明一</dd><dt>返回</dt><dd>说明二</dd></dl>` +
		`<h2>示例</h2><ol><li>第一步</li><li>第二步</li><li>第三步</li></ol></section></article></main>` +
		`<aside><section><h3>相关</h3><ol><li>链接一</li><li>链接二</li></ol></section></aside>` +
		`<footer><section><h4>关于</h4><dl><dt>作者</dt><dd>某某</dd></dl></section></footer>` +
		`</body></html>`
	a := StructHash([]byte(fmt.Sprintf(tpl, "页面甲", "标题甲")))
	b := StructHash([]byte(fmt.Sprintf(tpl, "页面乙", "标题乙")))
	if a == 0 || b == 0 {
		t.Fatalf("模板页应能算出结构指纹（路径数 %d）", len(StructPaths([]byte(fmt.Sprintf(tpl, "甲", "甲")))))
	}
	if d := Hamming(a, b); d != 0 {
		t.Errorf("同一模板不同正文的结构距离应为 0，实际 %d", d)
	}
}

// TestStructHashDifferentTemplates 不同骨架的结构距离必须足够大。
// 与实测的「跨站最小 14」同向：结构不同就该差得明显，否则区分不了站点。
func TestStructHashDifferentTemplates(t *testing.T) {
	// 两块骨架都用「多种容器标签并列」拼出足够多的不同路径——
	// 路径是去重后的集合，repeat 同一个块不会增加路径数（第一版栽在这里，
	// 路径数只有 8/9，被 structMinPaths 门槛挡掉，测试变成 Skip 等于没测）。
	flat := `<!doctype html><html><body>` +
		`<header><hgroup><h1>标题</h1><h2>副标题</h2></hgroup></header>` +
		`<nav><ol><li>导航一</li><li>导航二</li><li>导航三</li></ol></nav>` +
		`<main><section><h2>小节甲</h2><table><thead><tr><th>字段</th></tr></thead>` +
		`<tbody><tr><td>值</td></tr></tbody></table></section>` +
		`<section><h2>小节乙</h2><dl><dt>项</dt><dd>说明</dd></dl></section></main>` +
		`<aside><section><h3>侧栏</h3><ol><li>链接</li></ol></section></aside>` +
		`<footer><address>联系方式</address><section><h4>版权</h4><dl><dt>年份</dt></dl></section></footer>` +
		`</body></html>`
	nested := `<!doctype html><html><body>` +
		`<article><blockquote><figure><figcaption>图注甲</figcaption>` +
		`<blockquote><figure><figcaption>图注乙</figcaption>` +
		`<blockquote><figure><figcaption>图注丙</figcaption>` +
		`<details><summary>摘要</summary><dl><dt>键</dt><dd>值</dd></dl></details>` +
		`</figure></blockquote></figure></blockquote></figure></blockquote>` +
		`<section><details><summary>更多</summary><dl><dt>甲</dt><dd>一</dd></dl>` +
		`<details><summary>再展开</summary><dl><dt>乙</dt><dd>二</dd></dl></details></details></section>` +
		`</article></body></html>`
	a, b := StructHash([]byte(flat)), StructHash([]byte(nested))
	if a == 0 || b == 0 {
		t.Skipf("样本结构不足（路径数 %d/%d），跳过", len(StructPaths([]byte(flat))), len(StructPaths([]byte(nested))))
	}
	if d := Hamming(a, b); d < 8 {
		t.Errorf("不同骨架的结构距离应 ≥8，实际 %d", d)
	}
}

// TestStructHashSeparationOnRealCorpus 真实语料上的分离度诊断。
//
// 这是「结构指纹能不能用」的依据：同站（同一套模板）应当极近，
// 跨站应当足够远。数字固定在这里，将来换实现或调门槛会立刻发现退化。
func TestStructHashSeparationOnRealCorpus(t *testing.T) {
	st, err := kv.Open("../../data/kv")
	if err != nil {
		t.Skip("无法打开 data/kv:", err)
	}
	defer st.Close()

	type sample struct {
		domain string
		hash   uint64
	}
	var samples []sample
	n := 0
	_ = st.Scan(kv.Key(""), func(key string, val []byte) error {
		if len(samples) >= 200 {
			return nil // 够了就跳过剩下的，不返回错误（错误会传播成测试失败）
		}
		n++
		if n%6 != 0 {
			return nil // 采样：全量两两比较是 O(n²)
		}
		h := StructHash(val)
		if h == 0 {
			return nil
		}
		u := strings.TrimPrefix(key, "page:")
		if i := strings.Index(u, "/"); i >= 0 {
			u = u[:i]
		}
		samples = append(samples, sample{domain: u, hash: h})
		return nil
	})
	if len(samples) < 30 {
		t.Skip("样本不足")
	}

	sameMin, crossMin := 64, 64
	sameSum, sameN, crossSum, crossN := 0, 0, 0, 0
	for i := 0; i < len(samples); i++ {
		for j := i + 1; j < len(samples); j++ {
			d := Hamming(samples[i].hash, samples[j].hash)
			if samples[i].domain == samples[j].domain {
				sameSum += d
				sameN++
				if d < sameMin {
					sameMin = d
				}
			} else {
				crossSum += d
				crossN++
				if d < crossMin {
					crossMin = d
				}
			}
		}
	}
	if sameN == 0 || crossN == 0 {
		t.Skip("配对不足")
	}
	t.Logf("样本 %d 篇 | 同站 平均 %.1f 最小 %d | 跨站 平均 %.1f 最小 %d",
		len(samples), float64(sameSum)/float64(sameN), sameMin,
		float64(crossSum)/float64(crossN), crossMin)

	// 同站应当极近（同一套模板）
	if float64(sameSum)/float64(sameN) > 8 {
		t.Errorf("同站平均结构距离 %.1f 过大，同站页面本就该共用模板",
			float64(sameSum)/float64(sameN))
	}
	// 跨站最小必须留足余量：这是「跨站结构极近 = 站群」这条判定的安全边界。
	// 实测 14，取 8 为下限——低于它就说明不同站点的骨架开始撞车。
	if crossMin < 8 {
		t.Errorf("跨站最小结构距离 %d 过小，不同站点的模板开始撞车", crossMin)
	}
}
