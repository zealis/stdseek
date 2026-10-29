// AI 站点分类：配置接口 + 批量判定接口。
//
// 为什么配置落 DB 而不是启动参数：换模型/换供应商是常态，
// 需要重启才能改的配置等于实际上没人会改（同人机验证那条教训）。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"voidxo/internal/classify"
	"voidxo/internal/labels"
	"voidxo/internal/sitecat"
	"voidxo/internal/store"
)

const (
	settingAIBaseURL = "ai.base_url"
	settingAIKey     = "ai.api_key"
	settingAIModel   = "ai.model"

	// 备用接口：主接口超时/5xx 时自动顶上，并在切换与恢复时给管理员发邮件。
	settingAIBackupBaseURL = "ai.backup_base_url"
	settingAIBackupKey     = "ai.backup_api_key"
	settingAIBackupModel   = "ai.backup_model"

	// 第二备用接口：主接口与备用都不可用时的最后兜底。
	settingAIBackup2BaseURL = "ai.backup2_base_url"
	settingAIBackup2Key     = "ai.backup2_api_key"
	settingAIBackup2Model   = "ai.backup2_model"

	// 摘要优先使用哪个接口（primary / backup / backup2）；
	// 只影响摘要的尝试顺序，不改变站点分类的主备判定。
	settingAISummaryEndpoint = "ai.summary_endpoint"

	// ai.failover_at：上次切到备用接口的时间（0 = 正常用主接口）。
	settingAIFailoverAt = "ai.failover_at"

	// classifyMaxPerCall 单次请求最多判几个站：每个站一次模型调用，
	// 一次点太多会让请求跑几分钟、前端只能干等。
	classifyMaxPerCall = 20
	// classifyWorkers 并发度：模型接口按 QPS 限流的多，压太高只会 429。
	classifyWorkers = 4
)

// aiEndpoints 按配置顺序返回填全的接口（主 → 备用 → 第二备用）。
// 只填了地址没填模型的配置视为未启用：请求一个没有 model 的接口只会拿到 400。
func (s *Server) aiEndpoints(ctx context.Context) []classify.Endpoint {
	base, _ := s.st.Setting(ctx, settingAIBaseURL, "")
	key, _ := s.st.Setting(ctx, settingAIKey, "")
	model, _ := s.st.Setting(ctx, settingAIModel, "")
	bBase, _ := s.st.Setting(ctx, settingAIBackupBaseURL, "")
	bKey, _ := s.st.Setting(ctx, settingAIBackupKey, "")
	bModel, _ := s.st.Setting(ctx, settingAIBackupModel, "")
	b2Base, _ := s.st.Setting(ctx, settingAIBackup2BaseURL, "")
	b2Key, _ := s.st.Setting(ctx, settingAIBackup2Key, "")
	b2Model, _ := s.st.Setting(ctx, settingAIBackup2Model, "")

	all := []classify.Endpoint{
		{Name: endpointPrimary, BaseURL: base, APIKey: key, Model: model},
		{Name: endpointBackup, BaseURL: bBase, APIKey: bKey, Model: bModel},
		{Name: endpointBackup2, BaseURL: b2Base, APIKey: b2Key, Model: b2Model},
	}
	out := make([]classify.Endpoint, 0, len(all))
	for _, e := range all {
		if e.BaseURL == "" || e.Model == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// aiClient 每次按当前配置构造（几次 SQL 主键查询）。
// 不缓存客户端的原因是「改完立即生效」比省一次查询重要得多。
//
// 接口顺序决定「先试谁」：正常时主在前、备用兜底；故障期内反过来，
// 否则一轮 20 个站每个都要先等主接口超时一次（30 秒 × 20）。
func (s *Server) aiClient(ctx context.Context) *classify.Client {
	eps := s.aiEndpoints(ctx)
	if s.inFailover(ctx) {
		// 非主接口在前：刚确认主接口坏了就别再逐个站点去撞它。
		out := make([]classify.Endpoint, 0, len(eps))
		for _, e := range eps {
			if e.Name != endpointPrimary {
				out = append(out, e)
			}
		}
		for _, e := range eps {
			if e.Name == endpointPrimary {
				out = append(out, e)
			}
		}
		eps = out
	}
	return classify.New(classify.Config{Endpoints: eps, Timeout: 30 * time.Second})
}

// summaryEndpoints 摘要的接口顺序：管理员选中的优先，其余按主 → 备用 → 第二备用。
// 选中的接口没配好时退回自然顺序，避免界面选了一个空接口就整个摘要不可用。
func (s *Server) summaryEndpoints(ctx context.Context) []classify.Endpoint {
	all := s.aiEndpoints(ctx)
	if len(all) == 0 {
		return nil
	}
	pref, _ := s.st.Setting(ctx, settingAISummaryEndpoint, "")
	pref = strings.TrimSpace(pref)
	if pref == "" {
		return all
	}
	out := make([]classify.Endpoint, 0, len(all))
	for _, e := range all {
		if e.Name == pref {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return all
	}
	for _, e := range all {
		if e.Name != pref {
			out = append(out, e)
		}
	}
	return out
}

// 接口名（classify.Result.Endpoint 回传这个，切换判定与邮件都靠它）。
const (
	endpointPrimary = "primary"
	endpointBackup  = "backup"
	endpointBackup2 = "backup2"

	// aiFailoverWindow 故障期长度：切到备用后这段时间内都先试备用，
	// 到期再回到「主在前」，主接口能用就自动恢复并通知。
	aiFailoverWindow = 30 * time.Minute
)

// endpointLabel 后台与邮件里给人看的接口名。
func endpointLabel(name string) string {
	switch name {
	case endpointPrimary:
		return "主接口"
	case endpointBackup:
		return "备用接口"
	case endpointBackup2:
		return "第二备用接口"
	}
	return name
}

// validEndpointName 摘要优先接口只能是这三个之一。
func validEndpointName(name string) bool {
	return name == endpointPrimary || name == endpointBackup || name == endpointBackup2
}

// inFailover 是否处于「已切到备用」的故障期。
func (s *Server) inFailover(ctx context.Context) bool {
	raw, _ := s.st.Setting(ctx, settingAIFailoverAt, "0")
	ts, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || ts <= 0 {
		return false
	}
	return time.Since(time.Unix(ts, 0)) < aiFailoverWindow
}

// AIFailoverState 是否处于「已切到备用」的故障期（测试与后台展示用）。
func (s *Server) AIFailoverState(ctx context.Context) bool { return s.inFailover(ctx) }

// AIClient 按当前配置构造分类客户端（测试与后台展示用）。
func (s *Server) AIClient(ctx context.Context) *classify.Client { return s.aiClient(ctx) }

// noteAIEndpoint 记下这次调用实际用的接口，状态变化时发邮件给管理员。
//
// 为什么要显式通知：备用接口「悄悄顶上」比直接失败更危险——
// 界面看起来一切正常，实际主供应商已经挂了几天没人知道。
// 只在状态**变化**时发（切换 / 恢复各一封），否则每判定一个站都发一封。
func (s *Server) noteAIEndpoint(ctx context.Context, used string, failures []string) {
	if used == "" {
		return
	}
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	failover := s.inFailover(ctx)
	raw, _ := s.st.Setting(ctx, settingAIFailoverAt, "0")
	stamped := strings.TrimSpace(raw) != "" && strings.TrimSpace(raw) != "0"

	switch {
	case used != endpointPrimary && !failover:
		_ = s.st.SetSetting(ctx, settingAIFailoverAt, strconv.FormatInt(time.Now().Unix(), 10))
		slog.Warn("ai failed over from primary", "endpoint", used, "failures", failures)
		s.mailAdmins(ctx, "voidxo：AI 主接口异常，已切换到"+endpointLabel(used),
			fmt.Sprintf("主接口调用失败，AI 功能已改用%s。\n\n失败原因：%s\n\n"+
				"接下来 30 分钟内会优先使用可用接口，之后自动再试主接口；主接口恢复时会再发一封通知。\n"+
				"如果长期没恢复，请检查主接口的配额与余额。", endpointLabel(used), joinFailures(failures)))
	case used == endpointPrimary && failover:
		_ = s.st.SetSetting(ctx, settingAIFailoverAt, "0")
		slog.Info("ai primary endpoint recovered")
		s.mailAdmins(ctx, "voidxo：AI 主接口已恢复",
			"主接口调用成功，AI 功能已切回主接口，备用接口停止使用。")
	case used == endpointPrimary && stamped:
		// 故障期已过（超过 30 分钟窗口）才第一次成功：清掉过期时间戳。
		// 不发恢复邮件——那时界面上也没显示「故障中」，突然来封信只会让人困惑。
		_ = s.st.SetSetting(ctx, settingAIFailoverAt, "0")
	}
}

// mailAdmins 给全部管理员发信。SMTP 没配时只写日志——
// 通知失败不该影响判定流程本身。
func (s *Server) mailAdmins(ctx context.Context, subject, body string) {
	if s.Mail == nil || !s.Mail.Enabled() {
		slog.Warn("admin notification skipped: mail not configured", "subject", subject)
		return
	}
	// 用独立上下文：判定 Goroutine 的 ctx 有 60 秒上限、HTTP 请求的 ctx 会随连接断开，
	// 通知邮件不该因为「这个请求结束了」就发不出去。
	mctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	to, err := s.st.AdminEmails(mctx)
	if err != nil {
		slog.Warn("admin emails load failed", "err", err)
		return
	}
	if len(to) == 0 {
		slog.Warn("admin notification skipped: no admin account", "subject", subject)
		return
	}
	for _, addr := range to {
		if err := s.Mail.Send(mctx, addr, subject, body); err != nil {
			slog.Warn("admin notify failed", "to", addr, "err", err)
		}
	}
}

// joinFailures 把「哪个接口失败了、为什么」拼成一两行给人看。
// 拿不到详情时要明说原因不明，不要写「未知」这种让人以为没查的词。
func joinFailures(failures []string) string {
	if len(failures) == 0 {
		return "（未取到失败详情，请查看服务日志）"
	}
	return strings.Join(failures, "\n")
}

// aiConfigView GET /admin/ai 的返回。secret 不下发明文，只报 has_key。
type aiConfigView struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	HasKey  bool   `json:"has_key"`
	Ready   bool   `json:"ready"`
	// 备用接口：主接口异常时自动顶上。
	BackupBaseURL string `json:"backup_base_url"`
	BackupModel   string `json:"backup_model"`
	BackupHasKey  bool   `json:"backup_has_key"`
	BackupReady   bool   `json:"backup_ready"`
	// 第二备用接口：主与备用都不可用时的最后兜底。
	Backup2BaseURL string `json:"backup2_base_url"`
	Backup2Model   string `json:"backup2_model"`
	Backup2HasKey  bool   `json:"backup2_has_key"`
	Backup2Ready   bool   `json:"backup2_ready"`
	// SummaryEndpoint 摘要优先使用的接口（primary / backup / backup2）。
	SummaryEndpoint string `json:"summary_endpoint"`
	// Failover 是否正处于「已切到备用」的故障期（后台要显示出来，
	// 否则管理员看到一切正常，其实主供应商已经挂了）。
	Failover bool `json:"failover"`
}

func (s *Server) handleGetAIConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	base, _ := s.st.Setting(ctx, settingAIBaseURL, "")
	model, _ := s.st.Setting(ctx, settingAIModel, "")
	key, _ := s.st.Setting(ctx, settingAIKey, "")
	bBase, _ := s.st.Setting(ctx, settingAIBackupBaseURL, "")
	bModel, _ := s.st.Setting(ctx, settingAIBackupModel, "")
	bKey, _ := s.st.Setting(ctx, settingAIBackupKey, "")
	b2Base, _ := s.st.Setting(ctx, settingAIBackup2BaseURL, "")
	b2Model, _ := s.st.Setting(ctx, settingAIBackup2Model, "")
	b2Key, _ := s.st.Setting(ctx, settingAIBackup2Key, "")
	summaryEndpoint, _ := s.st.Setting(ctx, settingAISummaryEndpoint, endpointPrimary)
	if !validEndpointName(summaryEndpoint) {
		summaryEndpoint = endpointPrimary
	}
	c := s.aiClient(ctx)
	writeJSON(w, http.StatusOK, aiConfigView{
		Enabled:         s.FeatureEnabled(ctx, "ai_labeling"),
		BaseURL:         base,
		Model:           model,
		HasKey:          key != "",
		Ready:           c.Ready(),
		BackupBaseURL:   bBase,
		BackupModel:     bModel,
		BackupHasKey:    bKey != "",
		BackupReady:     bBase != "" && bModel != "",
		Backup2BaseURL:  b2Base,
		Backup2Model:    b2Model,
		Backup2HasKey:   b2Key != "",
		Backup2Ready:    b2Base != "" && b2Model != "",
		SummaryEndpoint: summaryEndpoint,
		Failover:        s.inFailover(ctx),
	})
}

// handleSetAIConfig POST /admin/ai {base_url, model, api_key, ..., summary_endpoint}
// api_key 留空表示不修改——页面拿不到明文，就不能要求用户每次重填。
func (s *Server) handleSetAIConfig(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		APIKey  string `json:"api_key"`
		// 备用接口：三项都可选，留空表示不启用备用。
		BackupBaseURL string `json:"backup_base_url"`
		BackupModel   string `json:"backup_model"`
		BackupAPIKey  string `json:"backup_api_key"`
		// 第二备用接口：主与备用都不可用时的最后兜底。
		Backup2BaseURL string `json:"backup2_base_url"`
		Backup2Model   string `json:"backup2_model"`
		Backup2APIKey  string `json:"backup2_api_key"`
		// 摘要优先使用的接口：primary / backup / backup2。
		SummaryEndpoint string `json:"summary_endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	summaryEndpoint := strings.TrimSpace(req.SummaryEndpoint)
	if summaryEndpoint == "" {
		summaryEndpoint = endpointPrimary
	}
	if !validEndpointName(summaryEndpoint) {
		writeErr(w, http.StatusBadRequest, "摘要优先接口只能是 primary / backup / backup2")
		return
	}
	ctx := r.Context()
	if err := s.st.SetSetting(ctx, settingAIBaseURL, strings.TrimSpace(req.BaseURL)); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入配置失败")
		return
	}
	if err := s.st.SetSetting(ctx, settingAIModel, strings.TrimSpace(req.Model)); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入配置失败")
		return
	}
	if strings.TrimSpace(req.APIKey) != "" {
		if err := s.st.SetSetting(ctx, settingAIKey, strings.TrimSpace(req.APIKey)); err != nil {
			writeErr(w, http.StatusInternalServerError, "写入密钥失败")
			return
		}
	}
	// 备用接口与切换状态：改配置等于重新起步，清掉故障期让它先试主接口。
	if err := s.st.SetSetting(ctx, settingAIBackupBaseURL, strings.TrimSpace(req.BackupBaseURL)); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入备用配置失败")
		return
	}
	if err := s.st.SetSetting(ctx, settingAIBackupModel, strings.TrimSpace(req.BackupModel)); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入备用配置失败")
		return
	}
	if strings.TrimSpace(req.BackupAPIKey) != "" {
		if err := s.st.SetSetting(ctx, settingAIBackupKey, strings.TrimSpace(req.BackupAPIKey)); err != nil {
			writeErr(w, http.StatusInternalServerError, "写入备用密钥失败")
			return
		}
	}
	if err := s.st.SetSetting(ctx, settingAIBackup2BaseURL, strings.TrimSpace(req.Backup2BaseURL)); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入第二备用配置失败")
		return
	}
	if err := s.st.SetSetting(ctx, settingAIBackup2Model, strings.TrimSpace(req.Backup2Model)); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入第二备用配置失败")
		return
	}
	if strings.TrimSpace(req.Backup2APIKey) != "" {
		if err := s.st.SetSetting(ctx, settingAIBackup2Key, strings.TrimSpace(req.Backup2APIKey)); err != nil {
			writeErr(w, http.StatusInternalServerError, "写入第二备用密钥失败")
			return
		}
	}
	if err := s.st.SetSetting(ctx, settingAISummaryEndpoint, summaryEndpoint); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入摘要接口配置失败")
		return
	}
	_ = s.st.SetSetting(ctx, settingAIFailoverAt, "0")
	// 概览按查询缓存；换了优先接口后旧缓存还挂着，管理员会以为「改了没生效」。
	s.clearOverviewCache()
	c := s.aiClient(ctx)
	slog.Info("ai config changed", "model", c.Model(), "ready", c.Ready(),
		"summary_endpoint", summaryEndpoint)
	writeJSON(w, http.StatusOK, map[string]any{"ready": c.Ready(), "summary_endpoint": summaryEndpoint})
}

// classifyResult 单个站点的判定结果（前端逐条显示）。
type classifyResult struct {
	ID       int64    `json:"id"`
	Domain   string   `json:"domain"`
	Category string   `json:"category"` // 新的分类 key
	Label    string   `json:"label"`    // 分类中文名，前端直接用
	Labels   []string `json:"labels"`   // 站点级标签 key
	LabelTxt []string `json:"label_texts"`
	Error    string   `json:"error,omitempty"`
}

// labelTexts 把标签 key 转成中文名（前端直接拼提示用）。
func labelTexts(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, labels.Text(k))
	}
	return out
}

// handleClassifySites POST /admin/ai/classify {ids:[1,2,3]}
//
// 手动批量判定：**总是执行**（包括管理员手改过分类的站），因为这是显式要求。
// 判完写回 category/labels 并按域名重建索引，用户点完筛选立刻有结果。
func (s *Server) handleClassifySites(w http.ResponseWriter, r *http.Request) {
	if !s.FeatureEnabled(r.Context(), "ai_labeling") {
		writeErr(w, http.StatusBadRequest, "请先在功能开关里打开「AI 站点分类」")
		return
	}
	maxBody(w, r)
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "没有选中站点")
		return
	}
	if len(req.IDs) > classifyMaxPerCall {
		req.IDs = req.IDs[:classifyMaxPerCall]
	}
	client := s.aiClient(r.Context())
	if !client.Ready() {
		writeErr(w, http.StatusBadRequest, "请先配置 AI 接口地址与模型")
		return
	}

	sites, skipped := s.loadSites(r.Context(), req.IDs)
	outcomes := s.classifySites(client, sites)

	results := make([]classifyResult, 0, len(outcomes)+len(skipped))
	var done []string
	for _, o := range outcomes {
		res := classifyResult{ID: o.site.ID, Domain: o.site.Domain}
		if o.err != nil {
			res.Error = o.err.Error()
		} else {
			res.Category = o.result.Category
			res.Label = sitecat.Label(o.result.Category)
			res.Labels = o.result.Labels
			res.LabelTxt = labelTexts(o.result.Labels)
			docs, _ := s.st.DomainDocCount(context.Background(), o.site.Domain)
			if err := s.st.ApplySiteClassify(context.Background(), o.site.ID, o.result.Category, o.result.Labels, docs); err != nil {
				res.Error = "写入分类失败：" + err.Error()
			} else {
				done = append(done, o.site.Domain)
			}
		}
		results = append(results, res)
	}
	results = append(results, skipped...)

	ok, failed := 0, 0
	for _, r := range results {
		if r.Error != "" {
			failed++
		} else {
			ok++
		}
	}
	reindexed := s.reindexAfterClassify(done)

	slog.Info("ai classify done", "ok", ok, "failed", failed, "reindexed", reindexed, "model", client.Model())
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results, "ok": ok, "failed": failed, "reindexed": reindexed,
	})
}

// loadSites 按 id 批量取站点。失败的不静默跳过：
// 「点了没反应」是黑箱，把原因带回前端，用户至少知道该找谁。
func (s *Server) loadSites(ctx context.Context, ids []int64) (sites []store.WhitelistSite, skipped []classifyResult) {
	for _, id := range ids {
		site, ok, err := s.st.SiteByID(ctx, id)
		if err != nil || !ok {
			msg := "站点不存在或已删除"
			if err != nil {
				msg = "读取站点失败：" + err.Error()
			}
			slog.Warn("classify skip site", "id", id, "err", err)
			skipped = append(skipped, classifyResult{ID: id, Error: msg})
			continue
		}
		sites = append(sites, site)
	}
	return sites, skipped
}

// classifyOutcome 单站判定结果（成功或失败）。
type classifyOutcome struct {
	site   store.WhitelistSite
	result classify.Result
	err    error
	// used 这次实际用的接口（primary/backup），空表示判定失败、一个接口都没通。
	used string
}

// classifySites 并发判定一批站点（并发度 classifyWorkers）。
// 每个请求独立超时：一个站卡住不能拖住整批。
func (s *Server) classifySites(client *classify.Client, sites []store.WhitelistSite) []classifyOutcome {
	out := make([]classifyOutcome, len(sites))
	sem := make(chan struct{}, classifyWorkers)
	var wg sync.WaitGroup
	for i, site := range sites {
		wg.Add(1)
		go func(i int, site store.WhitelistSite) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			// 语料构造（读快照 + 抽正文）与模型调用共用这个超时：
			// 3 篇 readabilty 解析在慢盘上也能吃掉几秒。
			in := s.siteInput(ctx, site)
			got, err := client.Site(ctx, in)
			out[i] = classifyOutcome{site: site, result: got, err: err, used: got.Endpoint}
			// 接口切换要在并发里就地记：等到整批结束再判断，
			// 会漏掉「前几个站用主、后几个站用备」这类中途切换。
			if got.Endpoint != "" {
				s.noteAIEndpoint(ctx, got.Endpoint, got.Failures)
			}
		}(i, site)
	}
	wg.Wait()
	return out
}

// reindexAfterClassify 按域名重建索引。分类是索引里的 keyword 字段，
// 不重建的话「刚打完标却筛不出东西」会被当成功能坏了。
func (s *Server) reindexAfterClassify(domains []string) bool {
	if len(domains) == 0 || s.ReindexDomains == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := s.ReindexDomains(ctx, domains); err != nil {
		slog.Warn("reindex after classify failed", "err", err, "domains", len(domains))
		return false
	}
	return true
}

// ---- 自动判定：触发时机 ----
//
// 内容在长，站点性质也会变：一个博客做大了会引入多个作者（personal → platform），
// 一个文档站加了博客板块会变味。但「每篇新文章都重判一次」纯属烧钱，
// 所以按**数量级**重判：30 篇（够看出主题分布）→ 300 篇（够看出是否有大量作者）
// → 之后每 +3000 篇（增长一个量级才值得再看一次）。
const (
	classifyStep1 = 30
	classifyStep2 = 300
	classifyStep3 = 3000
	// autoClassifyPerRun 一轮自动判定的上限：一轮抓取后可能有几十个站同时到门槛，
	// 全判要跑十几分钟并与下一轮抓取抢模型配额。超出的留到下一轮——游标没动，还会命中。
	autoClassifyPerRun = 20
)

// nextClassifyDocs 下一次该在文档数达到多少时重判。
func nextClassifyDocs(last int) int {
	switch {
	case last < classifyStep1:
		return classifyStep1
	case last < classifyStep2:
		return classifyStep2
	default:
		return last + classifyStep3
	}
}

// classifyDue 是否该自动判这个站。
//
// 首判条件（用户定的）：**分类为空**才「有内容就判」——站点已经有分类说明有人
// 表过态（提交时填的或老的默认值），没必要花钱重猜一遍。
// 之后按门槛：文档数跨过 30 → 300 → 每 +3000。已经判过的站，游标从当次数起算。
// 管理员手改过的站在上游（Store.ClassifyCandidates）就被排除了，这里不用再判一次。
func classifyDue(category string, aiDocs, docs int) bool {
	if docs <= 0 {
		return false // 一页都没有，判出来也只能是猜
	}
	if strings.TrimSpace(category) == "" {
		return true
	}
	return docs >= nextClassifyDocs(aiDocs)
}

// AutoClassify 抓取结束后调用：把本次抓过的域名里「到点该判」的站点交给模型。
//
// 任何一步不具备条件（开关关着、模型没配、没有候选）都安静返回 0：
// 自动任务不该因为旁路能力没配好就往抓取日志里刷错误。
func (s *Server) AutoClassify(ctx context.Context, domains []string) (int, error) {
	// domains 为空表示看全表（定时抓取那一轮就是这种情况：门槛按每站自己的
	// 文档数算，与这轮抓了哪些站无关）。
	if !s.FeatureEnabled(ctx, "ai_labeling") {
		return 0, nil
	}
	client := s.aiClient(ctx)
	if !client.Ready() {
		return 0, nil
	}
	cands, err := s.st.ClassifyCandidates(ctx, domains)
	if err != nil {
		return 0, err
	}
	due := make([]store.ClassifyCandidate, 0, len(cands))
	for _, c := range cands {
		if classifyDue(c.Category, c.AIClassifiedDocs, c.Docs) {
			due = append(due, c)
		}
	}
	if len(due) == 0 {
		return 0, nil
	}
	if len(due) > autoClassifyPerRun {
		due = due[:autoClassifyPerRun]
	}
	sites := make([]store.WhitelistSite, 0, len(due))
	docs := make(map[string]int, len(due))
	for _, c := range due {
		sites = append(sites, store.WhitelistSite{ID: c.ID, Domain: c.Domain, Category: c.Category})
		docs[c.Domain] = c.Docs
	}

	outcomes := s.classifySites(client, sites)
	var done []string
	failed := 0
	for _, o := range outcomes {
		if o.err != nil {
			// 失败不写游标：下一轮抓取还会命中同一个站（自然退避）。
			failed++
			slog.Warn("auto classify failed", "domain", o.site.Domain, "err", o.err)
			continue
		}
		if err := s.st.ApplySiteClassify(ctx, o.site.ID, o.result.Category, o.result.Labels, docs[o.site.Domain]); err != nil {
			failed++
			slog.Warn("auto classify write failed", "domain", o.site.Domain, "err", err)
			continue
		}
		done = append(done, o.site.Domain)
	}
	s.reindexAfterClassify(done)
	slog.Info("auto classify done", "due", len(due), "ok", len(done), "failed", failed)
	return len(done), nil
}

// handleTestAI POST /admin/ai/test：用一个固定样例验证配置是否可用。
// 不拿真实站点去试——测配置不该产生业务数据。
func (s *Server) handleTestAI(w http.ResponseWriter, r *http.Request) {
	client := s.aiClient(r.Context())
	if !client.Ready() {
		writeErr(w, http.StatusBadRequest, "请先配置 AI 接口地址与模型")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	got, err := client.Site(ctx, classify.Input{
		Domain:      "github.com",
		Description: "code hosting and collaboration platform",
		Titles:      []string{"GitHub · Build and ship software"},
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "调用失败："+err.Error())
		return
	}
	// 测试也会触发切换判定：主接口挂了的话，这条测试就是第一个发现者。
	s.noteAIEndpoint(r.Context(), got.Endpoint, got.Failures)
	msg := "连通正常，示例站点判定为「" + sitecat.Label(got.Category) + "」"
	if len(got.Labels) > 0 {
		msg += "，标签：" + strings.Join(labelTexts(got.Labels), "、")
	}
	if got.Endpoint != endpointPrimary && got.Endpoint != "" {
		msg += "（本次走" + endpointLabel(got.Endpoint) + "）"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"category": got.Category, "label": sitecat.Label(got.Category),
		"labels": got.Labels, "label_texts": labelTexts(got.Labels),
		"model": client.Model(), "endpoint": got.Endpoint, "message": msg,
	})
}
