// Package ua 集中管理爬虫与预检对外自称的 User-Agent。
//
// 为什么单独一个包：抓取（crawler）与预检（api）必须用同一个身份。
// 以前预检写死 "voidxo-precheck/0.1"、抓取写死带 voidxo.example 的串，
// 结果同一台机器对同一个站点给出两种身份——排查时看到「预检 403、抓取成功」
// 会以为有两条不同的网络路径，其实只是两段代码没对齐。
package ua

const (
	// Bot 默认的爬虫标识。括号里的联系方式由启动时按站点地址补（见 cmd/stdseek 的 crawlUserAgent）。
	// 不在这里写死域名：写成占位域名会被按规范校验的站点直接 403。
	Bot = "stdseek-spider/0.1"

	// Precheck 预检专用标识。
	// 与正式抓取分开是有用的：站点在日志里能区分「有人来试探首页」和「真的开始抓了」，
	// 预检只取首页与 robots.txt，频率也低得多。
	Precheck = "stdseek-precheck/0.1"

	// Browser 打开「浏览器 UA」开关后使用的标识。
	//
	// 固定写一个具体的 Chrome 版本，不要用「最新版」之类的占位：有的 WAF 会校验
	// UA 里的版本号是否合理，写错反而更容易被拦。
	//
	// 注意这是**伪装**：对方无法从 UA 判断我们是爬虫。所以它默认关、后台标风险色，
	// 只在站点仅凭 UA 关键词（spider/bot）拦截时使用，且仍应遵守 robots.txt 与站点条款。
	Browser = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// BrowserFeatureKey 是「浏览器 UA」开关在 settings 里的落库键。
//
// 导出来是为了让 crawler（读开关）与 api（登记开关）引用同一个字符串：
// 两处各写一份字面量，拼错一个字母的表现是「开关打开但抓取不变」，很难查。
const BrowserFeatureKey = "feature.browser_ua"
