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
//
// 页头那段会先过一遍 deferExternalScripts：管理员多半是直接从统计平台复制片段，
// 而那些片段默认是同步的，放在 </head> 前会阻塞首屏解析。页脚不动——它本来就在
// 文档末尾，阻塞的只是 load。
func injectSiteCode(page []byte, head, foot string) []byte {
	if head != "" {
		page = insertBefore(page, "</head>", deferExternalScripts(head))
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

// deferExternalScripts 给外链 <script> 补 defer。
//
// 规则（宁可少改，不要改错）：
//   - 只认带 src 的；内联脚本原样保留（defer 对内联无效，加了也是噪音）
//   - 已经写了 defer / async 的不动（async 本身就非阻塞，语义还更强）
//   - type="module" 不动（模块脚本默认就是 defer 行为）
//   - type 是 JSON-LD / importmap 这类非 JS 的不动
//   - HTML 注释里的 <script 不动（那是示例代码，不是要执行的标签）
//
// 为什么不用 golang.org/x/net/html 解析：它会把整段代码重新序列化，
// 管理员的引号风格、自闭合写法、大小写全会被改写；这里只需要在标签里插一个属性，
// 保持原文更安全。代价是得自己处理「引号里的 >」这种边界（见 scriptTagEnd）。
func deferExternalScripts(code string) string {
	low := strings.ToLower(code)
	var out strings.Builder
	out.Grow(len(code) + 16)

	for i := 0; i < len(code); {
		// 注释整段跳过，避免把示例代码里的 <script 当成真的要执行的标签。
		if strings.HasPrefix(code[i:], "<!--") {
			end := strings.Index(code[i+4:], "-->")
			if end < 0 {
				out.WriteString(code[i:])
				break
			}
			end += i + 4 + len("-->")
			out.WriteString(code[i:end])
			i = end
			continue
		}

		if !strings.HasPrefix(low[i:], "<script") || !scriptTagBoundary(low, i+len("<script")) {
			out.WriteByte(code[i])
			i++
			continue
		}

		end := scriptTagEnd(code, i+len("<script"))
		if end < 0 {
			out.WriteString(code[i:])
			break
		}
		out.WriteString(addDefer(code[i:end]))
		i = end
	}
	return out.String()
}

// scriptTagBoundary 判断 <script 后面是不是标签边界：
// <scriptx 是自定义元素名，不该当成 script。
func scriptTagBoundary(low string, i int) bool {
	if i >= len(low) {
		return false
	}
	c := low[i]
	return c == '>' || c == '/' || c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// scriptTagEnd 返回标签 '>' 之后的下标；引号里的 '>' 不算结束。找不到返回 -1。
func scriptTagEnd(s string, from int) int {
	var quote byte
	for i := from; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '>':
			return i + 1
		}
	}
	return -1
}

// addDefer 在标签内部插一个 defer；不满足条件时原样返回。
func addDefer(tag string) string {
	inner := tag[len("<script") : len(tag)-1] // 不含 "<script" 与 ">" 的属性区
	low := strings.ToLower(inner)

	if !hasAttr(low, "src") || hasAttr(low, "defer") || hasAttr(low, "async") {
		return tag
	}
	if t, ok := attrValue(low, "type"); ok && !isJSMediaType(t) {
		return tag
	}

	body := tag[:len(tag)-1]
	suffix := ""
	// <script src="x" /> 这种自闭合写法：defer 要插在斜杠之前，否则属性会跑到斜杠后面。
	if strings.HasSuffix(body, "/") {
		body = body[:len(body)-1]
		suffix = "/"
	}
	// 去掉标签末尾原有空白，避免拼出 `<script src="x"  defer>`。
	body = strings.TrimRight(body, " \t\r\n")
	return body + " defer" + suffix + ">"
}

// hasAttr 在属性区里找一个独立属性名。
// 必须按「属性名」比对，不能用 strings.Contains：data-src 里有 src，
// data-defer 里有 defer，用包含判断会把这两种都判错。
func hasAttr(low, name string) bool {
	_, ok := attrValue(low, name)
	return ok
}

// attrValue 取属性值（去引号，未做实体解码）。属性存在但无值（如 defer）时返回 ("", true)。
func attrValue(low, name string) (string, bool) {
	for i := 0; i < len(low); {
		for i < len(low) && (isSpaceByte(low[i]) || low[i] == '/') {
			i++
		}
		start := i
		for i < len(low) && !isSpaceByte(low[i]) && low[i] != '=' && low[i] != '/' {
			i++
		}
		if start == i {
			i++
			continue
		}
		key := low[start:i]

		j := i
		for j < len(low) && isSpaceByte(low[j]) {
			j++
		}
		val := ""
		hasVal := false
		if j < len(low) && low[j] == '=' {
			hasVal = true
			j++
			for j < len(low) && isSpaceByte(low[j]) {
				j++
			}
			if j < len(low) && (low[j] == '"' || low[j] == '\'') {
				q := low[j]
				j++
				vs := j
				for j < len(low) && low[j] != q {
					j++
				}
				val = low[vs:j]
				if j < len(low) {
					j++
				}
			} else {
				vs := j
				for j < len(low) && !isSpaceByte(low[j]) {
					j++
				}
				val = low[vs:j]
			}
			i = j
		}

		if key == name {
			if hasVal {
				return strings.TrimSpace(val), true
			}
			return "", true
		}
	}
	return "", false
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// isJSMediaType 只有这些（以及缺省）才当脚本执行。
// 返回 false 的典型是 application/ld+json、importmap、speculationrules——
// 它们没有 src 也好、有 src 也好，都不该被 defer 影响。
func isJSMediaType(t string) bool {
	switch strings.TrimSpace(strings.ToLower(t)) {
	case "", "text/javascript", "application/javascript",
		"text/ecmascript", "application/ecmascript":
		return true
	}
	return false
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
