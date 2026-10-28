// Package sitecat 是站点分类的唯一词表。
//
// 为什么单独一个包：分类要同时出现在三个地方——白名单后台（人工选）、
// AI 打标（模型只能从这份清单里挑）、搜索页过滤（用户按它筛）。
// 三处各写一份必然漂移，尤其是 AI 返回的 key 与索引里的 category 对不上时，
// 表现为「明明打了标却筛不出来」，很难查。
//
// 排序即展示顺序：前五个是历史就有的分类，优先排在前面；
// 其余按现实使用频率从高到低。新增分类只加到这里 + 前端一张文字表。
package sitecat

// Category 一个站点分类。
type Category struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Hint 给 AI 的判定说明：只写「什么样的站点属于这一档」，
	// 不写规则解释（界面上也不展示）。
	Hint string `json:"-"`
}

// List 全部分类，顺序即展示顺序。
var List = []Category{
	{Key: "official", Label: "官方网站", Hint: "软件、产品、组织的官方站点或官方文档站，内容由官方发布"},
	{Key: "media", Label: "新闻阅读", Hint: "新闻媒体、资讯门户、杂志、专栏频道，以发布资讯或长文为主"},
	{Key: "platform", Label: "信息论坛", Hint: "问答、论坛、社区、聚合平台，内容主要由用户发帖产生"},
	{Key: "blog", Label: "技术博客", Hint: "个人或团队博客，作者持续写同一主题的技术文章"},
	{Key: "opensource", Label: "开源项目", Hint: "开源项目的主页、文档站、代码托管页"},
	{Key: "docs", Label: "项目文档", Hint: "产品/框架/API 的文档站、手册、参考与教程"},
	{Key: "image", Label: "图片素材", Hint: "图库、素材站、壁纸、图标、插画等以图片资源为主"},
	{Key: "download", Label: "软件下载", Hint: "软件、驱动、安装包、破解资源的下载站"},
	{Key: "design", Label: "设计创意", Hint: "设计作品集、灵感站、配色/字体/UI 资源"},
	{Key: "tool", Label: "效率工具", Hint: "在线工具、转换器、生成器、计算器这类开箱即用的服务"},
	{Key: "job", Label: "求职招聘", Hint: "招聘信息、职位搜索、外包接单、简历服务"},
	{Key: "life", Label: "生活服务", Hint: "医疗、政务、教育、出行、房产、餐饮等生活类服务"},
	{Key: "game", Label: "游戏影音", Hint: "游戏、影视、动漫、音乐等娱乐内容"},
	{Key: "social", Label: "社交平台", Hint: "微博、即刻、小红书这类社交动态平台"},
	{Key: "dev", Label: "开发工具", Hint: "开发者工具、SDK、云服务、运维与调试产品"},
	{Key: "shopping", Label: "购物平台", Hint: "电商、团购、二手交易、比价与优惠"},
}

// keys 全部合法 key（校验与 AI 输出过滤用）。
var keys = func() map[string]Category {
	m := make(map[string]Category, len(List))
	for _, c := range List {
		m[c.Key] = c
	}
	return m
}()

// Valid 判断 key 是否是已知分类。
func Valid(key string) bool {
	_, ok := keys[key]
	return ok
}

// Label 返回中文名；未知 key 原样返回（宁可显示机器名，也不要显示空白）。
func Label(key string) string {
	if c, ok := keys[key]; ok {
		return c.Label
	}
	return key
}

// Default 没有分类时的兜底值。与白名单建表默认值保持一致。
const Default = "blog"

// Prompt 给模型的分类说明：一行一个 key，让模型直接回 key。
func Prompt() string {
	out := ""
	for _, c := range List {
		out += "- " + c.Key + "：" + c.Label + "（" + c.Hint + "）\n"
	}
	return out
}
