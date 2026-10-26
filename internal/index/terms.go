package index

import (
	"sort"
	"strings"
)

// 中英术语对照表：中文查询打英文文档时的查询扩展。
//
// 为什么需要：术语桥类查询（「PHP 命名空间」「docker 构建镜像」）里，中文修饰词
// 在英文文档里完全不存在。实测「PHP 命名空间」的 16 条命中里，排在前面的全是
// php.net 的通用页（首页/docs.php），真正的 language.namespaces.php 被挤到十名
// 开外——因为「命名空间」这个词在英文手册里是 namespaces，字面上没有任何交集。
//
// 做法是「curated 词典」而不是同义词库或向量：规模小、可解释、可测，
// 而且能覆盖中文技术查询里最高频的一批词。词典只做查询扩展（加词不改原词），
// 因此不会让原本命中的结果消失。
//
// 扩展词权重低于原查询词（见 buildRequest 里的 expandBoost），
// 避免「安装」这种泛词把 install 相关页面全拽上来。
//
// 值是**切片**而不是单个字符串：一个中文词常有多个英文对应，硬选一个会系统性
// 做错一类查询。典型是「响应式」——Vue 的响应式是 reactivity（响应式系统），
// CSS 的响应式是 responsive（响应式布局），此前词典只收 responsive，
// 于是「vue 响应式基础」被扩成 responsive，目标页 Reactivity Fundamentals 永远排不上来。
var zhToEn = map[string][]string{
	// 文档站导航类（术语桥查询的高频词）
	"语言规范": {"spec"}, "规范": {"specification"}, "文档": {"documentation"},
	"手册": {"manual"}, "指南": {"guide"}, "教程": {"tutorial"}, "入门": {"getting-started"},
	"快速开始": {"quickstart"}, "速查表": {"cheatsheet"}, "术语表": {"glossary"},
	"常见问题": {"faq"}, "安装": {"install"}, "下载": {"download"}, "配置": {"configuration"},
	"部署": {"deployment"}, "发布": {"release"}, "版本": {"version"}, "历史": {"history"},
	"社区": {"community"}, "博客": {"blog"}, "新闻": {"news"}, "公告": {"announcement"},
	"安全": {"security"}, "漏洞": {"vulnerability"}, "更新日志": {"changelog"},
	"治理": {"governance"}, "下载页": {"downloads"}, "支持": {"support"},
	"培训": {"training"}, "行为准则": {"code-of-conduct"}, "日期时间": {"datetime"},
	"发布历史": {"release-history"}, "语言": {"language"}, "参考": {"reference"},
	"项目介绍": {"about"}, "介绍": {"introduction"}, "路线图": {"roadmap"},
	// 2026-10-24 按评测缺口补入（术语桥未覆盖 token 只有这几个，其余介词跳过）：
	// 「React 会议」目标页标题是 "React Conferences – React"、
	// 「Rust 学习」目标页是 "Learn Rust"，两词此前都不在词典里。
	// 「快速上手」补入主要让召回层受益——Vue 站所有页面 title 都是站点默认 "Vue.js"，
	// 排序层的 exactMatch 看不到功能名，只能靠 BM25 匹配正文。
	"会议": {"conferences"}, "学习": {"learn"}, "快速上手": {"getting-started"},
	// 语言/框架概念
	"命名空间": {"namespaces"}, "模块": {"module"}, "包": {"package"}, "函数": {"function"},
	"变量": {"variable"}, "常量": {"constants"}, "运算符": {"operators"},
	"表达式": {"expressions"}, "类型": {"types"}, "接口": {"interface"},
	"泛型": {"generics"}, "并发": {"concurrency"}, "协程": {"goroutine"},
	"反射": {"reflection"}, "错误处理": {"error-handling"}, "测试": {"testing"},
	"基准测试": {"benchmark"}, "性能": {"performance"}, "优化": {"optimization"},
	"内存": {"memory"}, "缓存": {"cache"}, "索引": {"index"}, "事务": {"transaction"},
	"锁": {"locking"}, "连接池": {"connection-pooling"}, "集群": {"cluster"},
	// Web / 前端
	"属性": {"attributes"}, "全局": {"global"}, "元素": {"elements"}, "标签": {"tags"},
	"响应式": {"reactivity", "responsive"}, "图片": {"images"}, "表单": {"forms"}, "样式": {"styles"},
	"组件": {"components"}, "状态": {"state"}, "路由": {"routing"}, "生命周期": {"lifecycle"},
	// 工程实践
	"镜像": {"image"}, "容器": {"container"}, "构建": {"build"}, "编译": {"compile"},
	"打包": {"bundle"}, "发布流程": {"release-process"}, "代码风格": {"style-guide"},
	"重构": {"refactoring"}, "设计模式": {"design-patterns"}, "架构": {"architecture"},
	"分布式": {"distributed"}, "微服务": {"microservices"}, "消息队列": {"message-queue"},
	"负载均衡": {"load-balancing"}, "监控": {"monitoring"}, "日志": {"logging"},
	"单元测试": {"unit-testing"}, "集成测试": {"integration-testing"},

	// ---- 2026-10-24 按 real-v2 失败查询补入 ----
	// 扩语料后新增 135 条评测，其中 real-v2 的 20 条里 16 条不达标，缺的英文词
	// 高度集中：全是「现代文档词汇」（reactivity/stream/deprecated/upstream…），
	// 而旧词典偏「网站导航词」（quickstart/glossary/community…）。
	// 只补这些查询实际缺的词，不做大而全的同义词库——词典的价值在于可解释可测。

	// Vue / React 前端文档
	"组合式": {"composition"}, "单文件": {"single-file", "sfc"},
	"状态管理": {"state-management"}, "基础": {"fundamentals"},
	// Node 文档
	"流处理": {"stream"}, "流式": {"stream"}, "流模块": {"stream"},
	"废弃": {"deprecated"}, "可执行": {"executable"},
	"应用程序": {"application"}, "事件循环": {"event-loop"},
	"回调": {"callback"}, "闭包": {"closure"},
	// Python 文档
	"堆": {"heap"}, "分配": {"allocation"}, "对象": {"objects"},
	"扫描":   {"scan"},
	"类型提示": {"type-hints"}, "正则": {"regex", "regular-expression"},
	"捕获": {"captures"}, "迭代": {"iteration"},
	// nginx / 网络
	"代理": {"proxy"}, "上游": {"upstream"}, "服务器": {"server"},
	"反向代理": {"reverse-proxy"},
	// Redis / 存储
	"数据类型": {"data-types"}, "概率": {"probabilistic"},
	"持久化": {"persistence"}, "发布订阅": {"pubsub"},
	"过期": {"expiration"}, "淘汰": {"eviction"},
	// 分布式/工程（同类缺口的常用词，一并补齐）
	"一致性": {"consistency"}, "分片": {"sharding"}, "副本": {"replica"},
	"序列化": {"serialization"}, "上下文": {"context"}, "幂等": {"idempotent"},
}

// enAltForms 英文词的额外形态：同一概念在真实页面标题里的不同写法。
//
// 只影响排序层（TermVariants），**不影响 expandTerms**——召回口径保持稳定，
// 避免把 "quick start" 拆成 quick + start 两个泛词污染 BM25 的 should 子句。
//
// 真实案例：「React 快速开始」的目标页 react.dev/learn 标题是 "Quick Start – React"，
// 词典值是 quickstart（无空格），字面匹配不上，目标页因此排不进前十。
var enAltForms = map[string][]string{
	"quickstart": {"quick-start", "quick start"},
}

// enForms 生成英文词的形态变体。连字符词自动展开成空格版与紧凑版：
// getting-started → [getting-started, getting started, gettingstarted]，
// 覆盖 "Getting Started" / "GettingStarted" 等真实标题写法。
func enForms(en string) []string {
	out := []string{en}
	if strings.Contains(en, "-") {
		out = append(out, strings.ReplaceAll(en, "-", " "), strings.ReplaceAll(en, "-", ""))
	}
	out = append(out, enAltForms[en]...)
	return out
}

// TermVariants 返回单个查询 token 对应的英文写法，供**排序层**使用。
//
// 与 expandTerms 的分工：expandTerms 吃整条查询、返回一批扩展词，用于构建
// BM25 的 should 子句（召回）；TermVariants 吃单个 token，用于 exactMatch
// 判断「这个中文词在英文文档标题里写成什么」（排序）。
//
// 为什么排序层也需要：术语桥的典型失败是「Rust 社区」——目标页 rust-lang.org/community/
// 标题只有 Community，而中文查询词「社区」在标题里不存在，exactMatch 给它 0 分；
// 同期首页 rust-lang.org/ 标题含 Rust 拿到 0.4 分，于是首页压过目标页（实测首相关第 8 位）。
// 词典早就把「社区」→community 收进去了，但只喂给了召回、没喂给排序。
//
// token 是中文复合词（中文没有空格，「发布版本」是一个 token）时，按「包含」
// 展开成多个英文词：发布→release、版本→version。结果排序后返回，保证可复现。
func TermVariants(tok string) []string {
	if !hasHan(tok) {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(en string) {
		for _, form := range enForms(en) {
			if form != "" && !seen[form] {
				seen[form] = true
				out = append(out, form)
			}
		}
	}
	if ens, ok := zhToEn[tok]; ok {
		for _, en := range ens {
			add(en)
		}
	}
	for zh, ens := range zhToEn {
		if zh != tok && strings.Contains(tok, zh) {
			for _, en := range ens {
				add(en)
			}
		}
	}
	sort.Strings(out)
	return out
}

// expandTerms 从查询里找出词典命中的中文词，返回对应英文词（去重）。
// 只在**中英混杂**查询（既含汉字又含拉丁字母）时生效。
//
// 为什么纯中文查询不扩展：扩出来的都是英文词，而纯中文查询的目标是中文内容
// （红线 9：中文 query 只召回中文内容）。虽然索引层的 lang=zh 硬过滤会把英文文档
// 挡在外面，但这些英文词仍会匹配到中文文档里出现的英文片段（代码块、专有名词），
// 引入词面噪音。实测补入「学习」→learn 之后，纯中文组 NDCG@10 0.834→0.828。
func expandTerms(qs string) []string {
	if !hasHan(qs) || !hasLatin(qs) {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for zh, ens := range zhToEn {
		if !strings.Contains(qs, zh) {
			continue
		}
		// 多字词优先：查询里同时含「单元测试」和「测试」时只扩展前者，
		// 否则 unit-testing 与 testing 两个近义词会把候选池撑大。
		redundant := false
		for other := range zhToEn {
			if other != zh && len(other) > len(zh) && strings.Contains(qs, other) &&
				strings.Contains(other, zh) {
				redundant = true
				break
			}
		}
		if redundant {
			continue
		}
		for _, en := range ens {
			if en == "" || seen[en] {
				continue
			}
			seen[en] = true
			out = append(out, en)
		}
	}
	return out
}

// hasLatin 查询里是否含拉丁字母，用于区分纯中文查询与中英混杂查询。
func hasLatin(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}
