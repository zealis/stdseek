// Package i18n 决定「这一次请求用哪种界面语言」。
//
// 只做一件事：把 cookie、Accept-Language 收敛成一个写入 HTML 的 locale。
// 为什么不放在 internal/lang：那个包判的是**内容语言**（这段正文是中文还是英文，
// 用于检索与语言过滤），和界面语言是两码事，混在一起早晚互相污染。
package i18n

import (
	"net/http"
	"strings"
)

// 支持的语言。加语言要同时改：这里、web/src/app/core/i18n.ts 的 LOCALES，
// 以及 web/src/app/core/i18n/<code>.ts 字典。
const (
	ZhCN = "zh-CN"
	En   = "en"
	// Default 默认语言。站点的语料是中文，没有明确信号时用中文。
	Default = ZhCN
)

// CookieName 用户显式选过的语言（前端切换时写入，前端也读 localStorage 优先）。
const CookieName = "stdseek.lang"

// Supported 判断是不是我们认得的值。
func Supported(loc string) bool {
	return loc == ZhCN || loc == En
}

// Pick 定这一次请求的语言：cookie > Accept-Language > 默认。
//
// cookie 优先的原因：用户点过顶栏的地球，那是显式选择；Accept-Language 只是浏览器的
// 偏好，不该盖掉它。
func Pick(r *http.Request) string {
	if r == nil {
		return Default
	}
	if c, err := r.Cookie(CookieName); err == nil && Supported(c.Value) {
		return c.Value
	}
	if loc := fromAcceptLanguage(r.Header.Get("Accept-Language")); loc != "" {
		return loc
	}
	return Default
}

// fromAcceptLanguage 从 Accept-Language 里挑第一个我们支持的语言。
//
// 不用 golang.org/x/text/language：它会做「zh-Hant → zh」这类匹配、返回最接近的项，
// 为两种语言引一个依赖不划算。这里只认前缀：en-US、en-GB 都归到 en，zh-TW 归到 zh-CN。
//
// 按出现顺序扫而不按 q 值排序：浏览器都把偏好的语言写在前面，q 排序在真实流量里极罕见。
// q=0（明确表示不要）也不额外处理——认不出来就回默认语言，够用了。
func fromAcceptLanguage(header string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		tag := strings.TrimSpace(part)
		if i := strings.IndexByte(tag, ';'); i >= 0 {
			tag = strings.TrimSpace(tag[:i])
		}
		low := strings.ToLower(tag)
		switch {
		case strings.HasPrefix(low, "zh"):
			return ZhCN
		case strings.HasPrefix(low, "en"):
			return En
		}
	}
	return ""
}
