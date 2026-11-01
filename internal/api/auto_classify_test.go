package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zealis/stdseek/internal/auth"
	"github.com/zealis/stdseek/internal/mail"
	"github.com/zealis/stdseek/internal/store"
)

// 判定门槛：30 → 300 → 之后每 +3000，且「从未判过」时只要有内容就判。
func TestClassifyDue(t *testing.T) {
	cases := []struct {
		cat          string
		aiDocs, docs int
		want         bool
	}{
		{"blog", 0, 0, false},  // 一页都没有，判了也只是猜
		{"", 0, 1, true},       // 分类为空 + 有内容 = 首判（用户定的规则）
		{"", 0, 5, true},       // 同上，不因内容少而推迟
		{"blog", 0, 1, false},  // 已有分类：先等门槛，不为省事就重猜
		{"blog", 0, 29, false}, // 未判过的站，第一道门槛就是 30
		{"blog", 0, 30, true},
		{"blog", 30, 299, false}, // 判过 30 篇，等 300
		{"blog", 30, 300, true},
		{"blog", 300, 3299, false}, // 之后每 +3000
		{"blog", 300, 3300, true},
		{"blog", 3300, 6300, true}, // 游标随判定推进，不会重复命中
		{"blog", 3300, 6299, false},
	}
	for _, c := range cases {
		if got := classifyDue(c.cat, c.aiDocs, c.docs); got != c.want {
			t.Fatalf("classifyDue(%q,%d,%d)=%v want %v", c.cat, c.aiDocs, c.docs, got, c.want)
		}
	}
	// 门槛必须严格递增，否则会反复命中同一个站（等于每轮抓取都烧一次模型调用）。
	for _, docs := range []int{0, 25, 300, 3300, 9000, 30000} {
		if next := nextClassifyDocs(docs); next <= docs {
			t.Fatalf("nextClassifyDocs(%d)=%d 必须大于当前值", docs, next)
		}
	}
}

// 自动判定：只判到点的站；手改过的分类不覆盖；判完写游标防止反复判。
func TestAutoClassifyHonoursThresholdsAndManual(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()

	var calls int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]string{"content": `{"category":"dev","labels":["personal"]}`},
			}},
		})
	}))
	defer model.Close()

	for k, v := range map[string]string{
		"ai.base_url":         model.URL,
		"ai.model":            "stub",
		"feature.ai_labeling": "true",
	} {
		if err := e.st.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}

	// from 是起始序号：同一个站分两批灌数据时，URL 不能重复（UNIQUE 会把新行
	// 当成更新，文档数永远涨不上去——测试里踩过一次）。
	seedDocs := func(domain string, from, n int) {
		for i := from; i < from+n; i++ {
			if _, err := e.st.UpsertDocument(ctx, store.Document{
				URL: fmt.Sprintf("https://%s/p/%d", domain, i), Domain: domain,
				Title: fmt.Sprintf("%s 的第 %d 篇文章", domain, i), Description: "desc",
				Labels: "[]", LabelReasons: "{}", WordCount: 900, FetchedAt: time.Now().Unix(),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	addSite := func(domain, category string) int64 {
		if err := e.st.AddWhitelist(ctx, domain, category, 86400); err != nil {
			t.Fatal(err)
		}
		sites, _, err := e.st.ListWhitelist(ctx, domain, 10, 0)
		if err != nil || len(sites) != 1 {
			t.Fatalf("load %s: %v %d", domain, err, len(sites))
		}
		return sites[0].ID
	}

	// A：判过 5 篇，现有 5 篇 → 还没到 30，不判
	aID := addSite("a.example", "blog")
	seedDocs("a.example", 0, 5)
	if err := e.st.ApplySiteClassify(ctx, aID, "blog", nil, 5); err != nil {
		t.Fatal(err)
	}
	// B：管理员手改过分类，300 篇也不许自动改
	bID := addSite("b.example", "blog")
	seedDocs("b.example", 0, 300)
	if err := e.st.SetWhitelistCategory(ctx, bID, "official"); err != nil {
		t.Fatal(err)
	}
	// C：从未判过就已经有 30 篇 → 首次到门槛，该判
	_ = addSite("c.example", "blog")
	seedDocs("c.example", 0, 30)
	// D：只有 1 篇且已有分类 → 不到门槛，不判
	_ = addSite("d.example", "blog")
	seedDocs("d.example", 0, 1)

	n, err := e.srv.AutoClassify(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("should judge only c.example, got %d (calls=%d)", n, atomic.LoadInt32(&calls))
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("model called %d times, want 1", got)
	}

	sites, _, err := e.st.ListWhitelist(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	byDomain := map[string]store.WhitelistSite{}
	for _, s := range sites {
		byDomain[s.Domain] = s
	}
	if s := byDomain["c.example"]; s.Category != "dev" || s.CategorySource != store.CategorySourceAI || s.AIClassifiedDocs != 30 {
		t.Fatalf("c.example 应被判定并写游标，got %+v", s)
	}
	if s := byDomain["d.example"]; s.CategorySource == store.CategorySourceAI {
		t.Fatalf("不到门槛的站不该被判，got %+v", s)
	}
	if len(byDomain["c.example"].Labels) != 1 || byDomain["c.example"].Labels[0] != "personal" {
		t.Fatalf("站点级标签应写回，got %v", byDomain["c.example"].Labels)
	}
	if s := byDomain["b.example"]; s.Category != "official" || s.CategorySource != store.CategorySourceManual {
		t.Fatalf("手改的分类不能被自动判定覆盖，got %+v", s)
	}
	if s := byDomain["a.example"]; s.AIClassifiedDocs != 5 {
		t.Fatalf("未到门槛的站不该被写游标，got %+v", s)
	}

	// A 涨到 30 篇：这一轮该判它
	seedDocs("a.example", 5, 25)
	if n, err := e.srv.AutoClassify(ctx, nil); err != nil || n != 1 {
		t.Fatalf("A 涨到 30 篇后应判 1 个，got n=%d err=%v", n, err)
	}
	sites, _, _ = e.st.ListWhitelist(ctx, "a.example", 10, 0)
	if sites[0].AIClassifiedDocs != 30 {
		t.Fatalf("游标应更新为 30，got %d", sites[0].AIClassifiedDocs)
	}

	// 再跑一次：没人到点，不该再调模型
	before := atomic.LoadInt32(&calls)
	if n, err := e.srv.AutoClassify(ctx, nil); err != nil || n != 0 {
		t.Fatalf("second run should judge nothing, got n=%d err=%v", n, err)
	}
	if got := atomic.LoadInt32(&calls); got != before {
		t.Fatalf("空闲轮不该调用模型：%d -> %d", before, got)
	}
}

// 开关关着时自动判定必须彻底安静（不能悄悄烧模型调用）。
func TestAutoClassifySkipsWhenSwitchOff(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()

	var calls int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": `{"category":"dev"}`}}}})
	}))
	defer model.Close()
	_ = e.st.SetSetting(ctx, "ai.base_url", model.URL)
	_ = e.st.SetSetting(ctx, "ai.model", "stub")
	_ = e.st.SetSetting(ctx, "feature.ai_labeling", "false")

	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://go.dev/x", Domain: "go.dev", Title: "x", Labels: "[]",
		LabelReasons: "{}", WordCount: 500, FetchedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := e.srv.AutoClassify(ctx, nil); err != nil || n != 0 {
		t.Fatalf("switch off should judge nothing, got n=%d err=%v", n, err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("开关关着时不该调用模型")
	}
}

// 白名单按分类筛选：结果只含该分类，且带回各分类计数（筛选下拉要用）。
func TestWhitelistCategoryFilter(t *testing.T) {
	h, _ := newTestServerWithToken(t, "tok")
	req := httptest.NewRequest(http.MethodGet, "/admin/whitelist?cat=blog", nil)
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Total  int                   `json:"total"`
		Items  []store.WhitelistSite `json:"items"`
		Counts map[string]int        `json:"cat_counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 1 || len(out.Items) != 1 || out.Items[0].Domain != "blog.example.com" {
		t.Fatalf("cat=blog 应只返回 blog.example.com，got %+v", out)
	}
	if out.Counts["blog"] != 1 || out.Counts["official"] != 1 {
		t.Fatalf("分类计数不对：%+v", out.Counts)
	}
	// 未知分类要返回空列表而不是 400：筛选值来自 UI，用户手改 URL 不该报错
	req = httptest.NewRequest(http.MethodGet, "/admin/whitelist?cat=nope", nil)
	req.Header.Set("X-Admin-Token", "tok")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown cat status=%d", rec.Code)
	}
}

// AI 改分类不该动抓取间隔：间隔是管理员按站点体量配的（或按分类给的默认值），
// 模型对「这个站该多久抓一次」没有发言权。
func TestAutoClassifyKeepsCrawlInterval(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]string{"content": `{"category":"media","labels":[]}`},
		}}})
	}))
	defer model.Close()
	_ = e.st.SetSetting(ctx, "ai.base_url", model.URL)
	_ = e.st.SetSetting(ctx, "ai.model", "stub")
	_ = e.st.SetSetting(ctx, "feature.ai_labeling", "true")

	if err := e.st.AddWhitelist(ctx, "keep.example", "official", 43200); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if _, err := e.st.UpsertDocument(ctx, store.Document{
			URL: fmt.Sprintf("https://keep.example/a/%d", i), Domain: "keep.example",
			Title: "文章标题" + fmt.Sprint(i), Labels: "[]", LabelReasons: "{}",
			WordCount: 800, FetchedAt: time.Now().Unix(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := e.srv.AutoClassify(ctx, nil); err != nil || n != 1 {
		t.Fatalf("should judge 1 site, got n=%d err=%v", n, err)
	}
	sites, _, err := e.st.ListWhitelist(ctx, "keep.example", 1, 0)
	if err != nil || len(sites) != 1 {
		t.Fatal(err)
	}
	if sites[0].Category != "media" {
		t.Fatalf("分类应被改成 media，got %q", sites[0].Category)
	}
	if sites[0].CrawlInterval != 43200 {
		t.Fatalf("抓取间隔不该被 AI 判定改动，got %d", sites[0].CrawlInterval)
	}
}

// 批量改抓取间隔：按分类重算 + 统一设定（退路）。
func TestAdminResetIntervals(t *testing.T) {
	// 用同一个 env 的路由：handler 写的是这个 env 的库，
	// 换成另一个 server 的 router 会打到另一个库上（改完读不到变化）。
	e := newTestEnv(t, testOpts{token: "tok"})
	h := e.h
	ctx := context.Background()

	post := func(body string) (int, int) {
		req := httptest.NewRequest(http.MethodPost, "/admin/whitelist/intervals", strings.NewReader(body))
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var out struct {
			Changed  int `json:"changed"`
			Interval int `json:"interval"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Changed, out.Interval
	}

	// 建两个站：一个是官方站（目标 1 天，本来就是 1 天 → 不算改动），
	// 一个是新闻站（目标 30 分钟，会被改）
	if err := e.st.AddWhitelist(ctx, "reset-news.example", "media", 86400); err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddWhitelist(ctx, "reset-docs.example", "official", 86400); err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for _, d := range []string{"reset-news.example", "reset-docs.example"} {
		sites, _, err := e.st.ListWhitelist(ctx, d, 1, 0)
		if err != nil || len(sites) != 1 {
			t.Fatal(err)
		}
		ids = append(ids, sites[0].ID)
	}

	// 未带 token 要 401（写操作）
	anon := httptest.NewRequest(http.MethodPost, "/admin/whitelist/intervals", strings.NewReader(`{"interval":0}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, anon)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("write must require token, got %d", rec.Code)
	}

	// 按分类重算：只有新闻站真的变了
	n, iv := post(fmt.Sprintf(`{"ids":[%d,%d],"interval":0}`, ids[0], ids[1]))
	if n != 1 || iv != 0 {
		t.Fatalf("应为按分类重算且只改 1 个，got changed=%d interval=%d", n, iv)
	}
	sites, _, _ := e.st.ListWhitelist(ctx, "reset-news.example", 1, 0)
	if sites[0].CrawlInterval != 1800 {
		t.Fatalf("新闻站应改成 1800，got %d", sites[0].CrawlInterval)
	}

	// 统一设为 1 天：退路
	n, _ = post(fmt.Sprintf(`{"ids":[%d],"interval":86400}`, ids[0]))
	if n != 1 {
		t.Fatalf("统一设定应改 1 个，got %d", n)
	}
	sites, _, _ = e.st.ListWhitelist(ctx, "reset-news.example", 1, 0)
	if sites[0].CrawlInterval != 86400 {
		t.Fatalf("应退回 86400，got %d", sites[0].CrawlInterval)
	}

	// 负数要被挡住
	bad := httptest.NewRequest(http.MethodPost, "/admin/whitelist/intervals", strings.NewReader(`{"interval":-1}`))
	bad.Header.Set("X-Admin-Token", "tok")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bad)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative interval should be rejected, got %d", rec.Code)
	}
}

// 主接口异常 → 自动切备用并记下故障期；故障期过后主接口能用 → 自动切回。
// 邮件在这里没配 SMTP，走「跳过并留日志」分支，不能因此报错。
func TestAIFailoverSwitchesAndRecovers(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	ctx := context.Background()

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]string{"content": `{"category":"dev","labels":[]}`},
		}}})
	}))
	defer good.Close()

	set := func(k, v string) {
		if err := e.st.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	set("ai.base_url", bad.URL)
	set("ai.model", "primary-model")
	set("ai.backup_base_url", good.URL)
	set("ai.backup_model", "backup-model")
	set("feature.ai_labeling", "true")

	postTest := func() (string, int) {
		req := httptest.NewRequest(http.MethodPost, "/admin/ai/test", strings.NewReader(`{}`))
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var out struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Endpoint, rec.Code
	}

	// 主接口 502 → 走备用，并进入故障期
	if ep, _ := postTest(); ep != "backup" {
		t.Fatalf("主接口挂了应走备用，got %q", ep)
	}
	if !e.srv.AIFailoverState(ctx) {
		t.Fatal("切到备用后应记录故障期")
	}
	// 故障期内备用优先：客户端第一个接口就是备用
	if names := e.srv.AIClient(ctx).EndpointNames(); len(names) != 2 || names[0] != "backup" {
		t.Fatalf("故障期内备用应排在前面，got %v", names)
	}

	// 主接口恢复（模拟故障期已过：把时间戳拨到 1 小时前）
	set("ai.base_url", good.URL)
	set("ai.failover_at", strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10))
	if ep, _ := postTest(); ep != "primary" {
		t.Fatalf("主接口恢复后应切回，got %q", ep)
	}
	if v, _ := e.st.Setting(ctx, "ai.failover_at", "0"); v != "0" {
		t.Fatalf("恢复后应清掉故障标记，got %q", v)
	}
}

// 主接口故障时要真的发信给管理员：备用接口悄悄顶上而没人知道，
// 比直接失败更糟——界面看着正常，主供应商可能已经挂了几天。
func TestAIFailoverNotifiesAdminsByMail(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	ctx := context.Background()

	// 起一个最小 SMTP 服务：只实现 net/smtp 客户端会发的那几条命令。
	// 刻意不宣告 STARTTLS / AUTH，让客户端跳过加密与认证走明文。
	var (
		mu   sync.Mutex
		got  []string
		body strings.Builder
	)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &fakeSMTP{onMail: func(m string) {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
		body.WriteString(m)
	}}
	go srv.serve(ln)
	defer ln.Close()

	e.srv.Mail = mail.New(mail.Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, From: "stdseek@example.com"})
	hash, err := auth.HashPassword("strongpass1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateUser(ctx, "admin@example.com", hash, "member", true, "127.0.0.1", true); err != nil {
		t.Fatal(err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]string{"content": `{"category":"dev","labels":[]}`},
		}}})
	}))
	defer good.Close()

	set := func(k, v string) {
		if err := e.st.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	set("ai.base_url", bad.URL)
	set("ai.model", "primary-model")
	set("ai.backup_base_url", good.URL)
	set("ai.backup_model", "backup-model")

	req := httptest.NewRequest(http.MethodPost, "/admin/ai/test", strings.NewReader(`{}`))
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n == 0 {
		t.Fatal("主接口故障应给管理员发一封通知邮件")
	}
	txt := body.String()
	if !strings.Contains(txt, "admin@example.com") {
		t.Fatalf("收件人应是管理员，got %q", txt[:min(len(txt), 200)])
	}
	if !strings.Contains(txt, "备用接口") {
		t.Fatalf("邮件正文应说明切到了备用接口，got %q", txt[:min(len(txt), 300)])
	}
	// 失败原因要写进正文：只说「异常」等于让管理员自己去翻日志
	if !strings.Contains(txt, "502") {
		t.Fatalf("邮件正文应带上失败原因（502），got %q", txt[:min(len(txt), 400)])
	}
}

// fakeSMTP 只够 net/smtp 客户端跑完一轮：没有 STARTTLS / AUTH 扩展。
type fakeSMTP struct {
	onMail func(msg string)
}

func (f *fakeSMTP) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)
	write := func(s string) { w.WriteString(s + "\r\n"); w.Flush() }
	write("220 fake ESMTP")

	var inData bool
	var data strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				f.onMail(data.String())
				data.Reset()
				write("250 queued")
				continue
			}
			data.WriteString(line + "\n")
			continue
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			write("250 fake")
		case strings.HasPrefix(line, "MAIL FROM"):
			write("250 ok")
		case strings.HasPrefix(line, "RCPT TO"):
			write("250 ok")
		case line == "DATA":
			inData = true
			write("354 end with .")
		case line == "QUIT":
			write("221 bye")
			return
		case line == "RSET":
			write("250 ok")
		default:
			write("250 ok")
		}
	}
}
