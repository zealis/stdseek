// 页头 / 页脚代码：让管理员放进统计脚本、自定义 CSS、验证 meta 之类的东西。
//
// 这是全项目唯一「把管理员写的原文塞进每个访客页面」的地方，能力上等同于
// 「给自己站留一个 XSS 后门」。所以边界必须写死，别在后面的人手里慢慢松掉：
//
//  1. **只注入访客页面。** /manage、/admin、/api 以及登录/注册/找回密码/验证页
//     一律不注入。理由不是洁癖——后台页面里有账号列表、白名单、令牌输入框，
//     第三方统计脚本一旦被投毒（供应链最常见的一步就是「统计代码里多了一行」），
//     它读到的就是这些东西。登录页同理：脚本能在提交前读到密码。
//  2. **只做两个锚点插入**（</head> 前、</body> 前），不参与任何模板替换。
//     用 strings/bytes 直接拼，不走 fmt/模板，代码里的 %s、{{ }} 就是字面量。
//  3. **大小上限 32KB，拒绝 NUL 字节。** 不校验语法——写错的 JS 只会静默失败，
//     我们判不出来，也不该假装能判。
//  4. **写入口只有 adminAuth 后面的 /admin/site，没有公开读接口。**
//     读接口一旦公开，这段代码就成了「任何人可探测的指纹」。
package api

import (
	"bytes"
	"net/http"
	"strings"
)

const (
	settingSiteHeadHTML = "site.head_html"
	settingSiteFootHTML = "site.foot_html"
	// maxSiteCodeBytes 单段代码上限。留够一个统计脚本 + 一小段样式，
	// 又不至于让人把整个框架塞进 settings 表（那张表每次读都是全表扫）。
	maxSiteCodeBytes = 32 << 10
)

// siteCode 读两段代码。任一为空就返回空串，调用方据此跳过注入。
func (s *Server) siteCode(r *http.Request) (head, foot string) {
	head, _ = s.st.GetSetting(r.Context(), settingSiteHeadHTML)
	foot, _ = s.st.GetSetting(r.Context(), settingSiteFootHTML)
	return strings.TrimSpace(head), strings.TrimSpace(foot)
}

// injectSiteCode 把页头代码插到 </head> 之前、页脚代码插到 </body> 之前。
//
// 只插第一个匹配：index.html 里这两个标签各出现一次，多插一份等于重复执行脚本。
func injectSiteCode(page []byte, head, foot string) []byte {
	if head != "" {
		page = insertBefore(page, "</head>", head)
	}
	if foot != "" {
		page = insertBefore(page, "</body>", foot)
	}
	return page
}

// insertBefore 在第一个 anchor 之前插入代码。找不到锚点就原样返回
// （宁可这段代码不生效，也不要把页面结构切坏）。
func insertBefore(page []byte, anchor, code string) []byte {
	i := bytes.Index(page, []byte(anchor))
	if i < 0 {
		return page
	}
	out := make([]byte, 0, len(page)+len(code)+1)
	out = append(out, page[:i]...)
	out = append(out, code...)
	out = append(out, '\n')
	out = append(out, page[i:]...)
	return out
}

// injectsSiteCode 这个路径要不要注入页头页脚。
//
// 后台与账号相关的页面不注入，原因见文件头。这里用前缀判断而不是精确匹配，
// 因为 /manage/xxx、/settings/xxx 都是同一片区域。
func injectsSiteCode(path string) bool {
	for _, prefix := range []string{
		"/manage", "/admin", "/api/",
		"/login", "/register", "/reset", "/verify", "/settings",
	} {
		if strings.HasPrefix(path, prefix) {
			return false
		}
	}
	return true
}
