// Package classify 用 OpenAI 兼容接口给站点判定分类。
//
// 与 summarize 的区别：摘要读单篇正文，分类判的是「整站是干嘛的」，所以输入是
// 站点自述（名称 + 首页描述）+ 若干标题 + 3 段正文摘录 + 抓取程序统计的结构性信号，
// 而不是把整站正文喂进去（那要多一个数量级的成本，判站点性质也用不上）。
// 分类结果是**站点级**的（写回白名单），不是页级标签：
// 同一站点的所有页面共享一个分类，用户筛选的也是「我要哪类站」。
package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"voidxo/internal/labels"
	"voidxo/internal/sitecat"
)

// Endpoint 一个模型接口（主用或备用）。Name 只用于日志与「哪个接口在干活」的回传。
type Endpoint struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
}

// Config 接口配置。
//
// 两种写法：Endpoints 显式给一串（主在前，失败时依次往后试）；
// 只填 BaseURL/Model/APIKey 时等价于「只有一个主接口」。两种写法并存是为了
// 让调用方不需要为了配单接口而构造切片。
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	// Endpoints 非空时优先使用它，BaseURL 那组被忽略。
	Endpoints []Endpoint
	Timeout   time.Duration
}

// endpoints 归一化成一串接口（过滤掉没填地址或模型的）。
func (c Config) endpoints() []Endpoint {
	if len(c.Endpoints) > 0 {
		out := make([]Endpoint, 0, len(c.Endpoints))
		for _, e := range c.Endpoints {
			if e.BaseURL == "" || e.Model == "" {
				continue
			}
			out = append(out, e)
		}
		return out
	}
	if c.BaseURL == "" || c.Model == "" {
		return nil
	}
	return []Endpoint{{Name: "primary", BaseURL: c.BaseURL, APIKey: c.APIKey, Model: c.Model}}
}

// Client 分类客户端。零值不可用，用 New 构造。
type Client struct {
	cfg Config
	hc  *http.Client
}

// New 构造客户端；未配置时 Ready() 为 false，调用方据此降级。
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Client{cfg: cfg, hc: &http.Client{Timeout: cfg.Timeout}}
}

// Ready 是否具备调用条件：至少有一个接口填全了地址与模型。
func (c *Client) Ready() bool {
	return c != nil && len(c.cfg.endpoints()) > 0
}

// Model 当前主接口（第一个可用接口）的模型名，后台展示用。
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	eps := c.cfg.endpoints()
	if len(eps) == 0 {
		return ""
	}
	return eps[0].Model
}

// EndpointNames 可用接口名字（主在前），后台展示与日志用。
func (c *Client) EndpointNames() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.cfg.Endpoints)+1)
	for _, e := range c.cfg.endpoints() {
		out = append(out, e.Name)
	}
	return out
}

// Signals 抓取程序统计出来的结构性证据（不是模型推断）。
//
// 为什么要给模型这些数字：只看标题时「个人站 vs 平台」几乎判不准——
// 平台站的文章标题和作者博客的标题长得一模一样。URL 形态（/u/、/user/、/people/）
// 和规则标签的占比是机器能数出来的硬证据，比让模型猜可靠。
type Signals struct {
	Docs          int // 已入库页面数
	MedianWords   int // 正文中位字数（判断是长文站还是索引页）
	UserPathPages int // URL 形如用户主页/作者页的页面数
	ArticlePages  int // URL 形如文章详情页的页面数
	AdTagged      int // 规则判定「广告较多」的页面数
	AITagged      int // 规则判定「疑似 AI 生成」的页面数
}

// Percent 返回 part/total 的百分比（total 为 0 时给 0）。
func (s Signals) Percent(part int) int {
	if s.Docs <= 0 {
		return 0
	}
	return part * 100 / s.Docs
}

// Input 判定输入：站点自述 + 语料 + 结构性信号。
//
// 刻意不喂整站正文：一个站几百页，全喂进去成本高一个数量级，
// 而判定「这站是干嘛的」并不需要读完全部内容——抽样 3 段代表性正文够用。
type Input struct {
	Domain      string
	Name        string
	Description string
	Titles      []string // 按发布时间倒序的代表性标题（已过滤站务页）
	Excerpts    []string // 3 段正文摘录（篇幅最长的几篇）
	Signals     Signals
}

// Result 一次判定的结果：分类单选、标签多选。
type Result struct {
	Category string
	Labels   []string
	// Endpoint 这次实际用的是哪个接口（主用/备用）。调用方据此判断是否发生了切换，
	// 备用接口「悄悄顶上」而不告诉管理员，等于故障被静音。
	Endpoint string
	// Failures 这次调用里**排在它前面、失败了**的接口与原因（"primary: 模型返回 502…"）。
	// 成功时也要带出来：不然通知邮件只能说「主接口异常」却说不出为什么，
	// 管理员照样得自己去翻日志。
	Failures []string
}

// 每次请求的 token 预算。见 chat() 的说明：推理模型会把预算花在思考上。
const (
	classifyMaxTokens   = 512
	classifyRetryTokens = 2048
	// excerptRunes 每段摘录的字数上限：3 段 × 300 字足够看出文风与广告密度，
	// 再多就是在为「反正也用不上」的字符付钱。
	excerptRunes = 300
)

// Site 判定单个站点的分类与标签。
func (c *Client) Site(ctx context.Context, in Input) (Result, error) {
	if !c.Ready() {
		return Result{}, errors.New("AI 分类未配置")
	}
	prompt := buildPrompt(in)

	eps := c.cfg.endpoints()
	if len(eps) == 0 {
		return Result{}, errors.New("AI 分类未配置")
	}
	// 逐个接口试：主接口挂了（超时/5xx/连接失败）就退到备用。
	// 只有「拿到正文了但内容不合规」这种情况不算故障，不换接口——
	// 模型答非所问是模型问题，换供应商也多半一样。
	var (
		lastErr  error
		failures []string
	)
	for _, ep := range eps {
		raw, err := c.chat(ctx, ep, prompt)
		if err != nil {
			lastErr = err
			failures = append(failures, fmt.Sprintf("%s：%v", ep.Name, strings.TrimSpace(err.Error())))
			slog.Warn("classify endpoint failed", "endpoint", ep.Name, "model", ep.Model, "err", err)
			continue
		}
		key := parseCategory(raw)
		if !sitecat.Valid(key) {
			// 模型没按词表回答（或答了个不存在的 key）时落到「其他」，不报错：
			// 批量 20 个站时，一个不合规的回答不该让整条结果变成红色错误，
			// 而「其他」本身就是「判不出来」的落点，语义是对的。
			key = sitecat.Other
		}
		return Result{Category: key, Labels: parseLabels(raw), Endpoint: ep.Name, Failures: failures}, nil
	}
	return Result{}, lastErr
}

// buildPrompt 拼提示词。用 Builder 而不是一串 %s：字段多起来后
// fmt 的占位符次序极容易错位（错位不会报错，只会让模型看到「域名」是篇文章的标题）。
func buildPrompt(in Input) string {
	var b strings.Builder
	b.WriteString("你是站点分类器。根据下面的「站点自述」「语料」和「抓取信号」，判定这个站点的分类与标签。\n\n")

	b.WriteString("一、分类（单选，只能返回清单里的 key）：\n")
	b.WriteString(sitecat.Prompt())
	b.WriteString("\n二、标签（可多选，没有就给空数组；只能返回清单里的 key）：\n")
	b.WriteString(labels.Prompt())

	b.WriteString(`
三、规则：
1. 只输出 JSON：{"category":"key","labels":["key"]}，不要 markdown、不要解释。
2. 分类看「这个站整体是干嘛的」，不是看某一篇的技术栈。官方产品站与它的文档站都算 official；
   问答/论坛/社区/聚合算 platform；个人长期更新同一主题算 blog。
3. 摘录只代表一部分页面：个别文章跑题、单篇带推广，都不足以改变整站分类或打标签。
4. 分类信息不足、或确实不属于清单里任何一类时返回 "other"。清单里的分类都列全了，
   不要为了给出答案硬塞一个最接近的。
5. 标签只打**整站成立**的特征，且 ad_heavy / ai_suspect 必须有抓取信号支持：
   - personal：站点由个人（或极小团队）维护，内容以自己的文章为主；看站名、描述、署名、
     以及 user_path_pages 占比。
   - ugc_platform：内容主要来自大量不同用户发帖；user_path_pages 占比高是强证据。
   - ad_heavy：ad_tagged 占已入库页面的比例高（≥30%），或摘录里推广/带货段落占正文明显比例。
     只是页面上挂了广告位不算。
   - ai_suspect：ai_tagged 占比高（≥30%），或摘录本身是模板化套话、没有具体案例与出处。
6. 信号数字是程序统计的真实计数，不要臆测没给出的数据；数据不足时标签留空数组。
   误标比不标更讨厌——用户靠标签排除内容。
`)

	b.WriteString("\n站点域名：" + orDash(in.Domain) + "\n")
	b.WriteString("站点名称：" + orDash(in.Name) + "\n")
	b.WriteString("站点描述：" + orDash(in.Description) + "\n")

	s := in.Signals
	b.WriteString("\n抓取信号（程序统计）：\n")
	fmt.Fprintf(&b, "- 已入库页面 %d 篇，正文中位字数 %d\n", s.Docs, s.MedianWords)
	fmt.Fprintf(&b, "- URL 形如用户主页/作者页：%d 篇（%d%%）\n", s.UserPathPages, s.Percent(s.UserPathPages))
	fmt.Fprintf(&b, "- URL 形如文章详情页：%d 篇（%d%%）\n", s.ArticlePages, s.Percent(s.ArticlePages))
	fmt.Fprintf(&b, "- 规则判定「广告较多」的页面：%d 篇（%d%%）\n", s.AdTagged, s.Percent(s.AdTagged))
	fmt.Fprintf(&b, "- 规则判定「疑似 AI 生成」的页面：%d 篇（%d%%）\n", s.AITagged, s.Percent(s.AITagged))

	b.WriteString("\n近期文章标题（按发布时间倒序）：\n")
	if len(in.Titles) == 0 {
		b.WriteString("-（暂无）\n")
	} else {
		for _, t := range in.Titles {
			b.WriteString("- " + truncate(t, 80) + "\n")
		}
	}

	b.WriteString("\n正文摘录（取篇幅最长的代表页，每段最多约 300 字）：\n")
	if len(in.Excerpts) == 0 {
		b.WriteString("（暂无正文，只能依据标题与站点描述判断）\n")
	} else {
		for i, e := range in.Excerpts {
			fmt.Fprintf(&b, "[摘录 %d] %s\n", i+1, truncate(e, excerptRunes))
		}
	}
	return b.String()
}

// parseLabels 从模型输出里取标签，**只保留词表里的 key**。
//
// 模型偶尔会编一个不存在的标签（比如 "low_quality"），直接写进索引就成了
// 一条永远筛不到、也永远清不掉的脏数据；这里直接丢掉，宁可少一个标签。
func parseLabels(s string) []string {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return nil
	}
	var obj struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(s[i:j+1]), &obj); err != nil {
		return nil
	}
	out := make([]string, 0, len(obj.Labels))
	seen := map[string]bool{}
	for _, l := range obj.Labels {
		k := strings.ToLower(strings.TrimSpace(l))
		if !labels.Valid(k) || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// chat 走 /chat/completions，返回模型正文。
//
// max_tokens 给到 512 而不是几十：现在常见的「flash」档模型其实是**推理模型**，
// 会先输出一段 reasoning_content 再给正文。实测 deepseek-v4.1-flash 用 64 时
// content 为空、finish_reason=length，解析结果落到「其他」——24 个站全被标成「其他」，
// 比不判还糟。512 够它想完再吐那 30 来个字符的 JSON。
func (c *Client) chat(ctx context.Context, ep Endpoint, prompt string) (string, error) {
	content, finish, err := c.chatOnce(ctx, ep, prompt, classifyMaxTokens)
	if err != nil {
		return "", err
	}
	// 个别站点的提示词会让模型想得更久：被截断且没拿到正文时，放大预算再要一次。
	if strings.TrimSpace(content) == "" && finish == "length" {
		content, _, err = c.chatOnce(ctx, ep, prompt, classifyRetryTokens)
		if err != nil {
			return "", err
		}
	}
	// 空正文必须报错，不能静默落「其他」：那是「判不出来」的语义，
	// 而这里其实是「这次调用没成功」，两者混在一起会让批量结果看起来全都正常。
	if strings.TrimSpace(content) == "" {
		return "", errors.New("模型没有返回正文（推理预算被吃光，或该模型不支持 JSON 输出）")
	}
	return content, nil
}

// chatOnce 发一次请求，返回正文与 finish_reason。
func (c *Client) chatOnce(ctx context.Context, ep Endpoint, prompt string, maxTokens int) (string, string, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"model":       ep.Model,
		"temperature": 0,
		"max_tokens":  maxTokens,
		"messages": []map[string]string{
			{"role": "system", "content": "你只输出严格 JSON，不写任何解释。"},
			{"role": "user", "content": prompt},
		},
	})
	url := strings.TrimRight(ep.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if ep.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("请求模型失败：%w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("模型返回 %d：%s", resp.StatusCode, truncate(string(data), 160))
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return "", "", errors.New("模型响应无法解析")
	}
	return out.Choices[0].Message.Content, out.Choices[0].FinishReason, nil
}

// parseCategory 从模型输出里抠出分类 key。
// 模型偶尔会裹上代码块或加一句解释，所以先按 JSON 解析，失败再退回「扫已知 key」。
func parseCategory(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			var obj struct {
				Category string `json:"category"`
			}
			if err := json.Unmarshal([]byte(s[i:j+1]), &obj); err == nil && obj.Category != "" {
				return strings.ToLower(strings.TrimSpace(obj.Category))
			}
		}
	}
	low := strings.ToLower(s)
	for _, c := range sitecat.List {
		if strings.Contains(low, c.Key) {
			return c.Key
		}
	}
	return ""
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（无）"
	}
	return truncate(s, 120)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
