// AI 站点分类：配置接口 + 批量判定接口。
//
// 为什么配置落 DB 而不是启动参数：换模型/换供应商是常态，
// 需要重启才能改的配置等于实际上没人会改（同人机验证那条教训）。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"voidxo/internal/classify"
	"voidxo/internal/sitecat"
)

const (
	settingAIBaseURL = "ai.base_url"
	settingAIKey     = "ai.api_key"
	settingAIModel   = "ai.model"

	// classifyMaxPerCall 单次请求最多判几个站：每个站一次模型调用，
	// 一次点太多会让请求跑几分钟、前端只能干等。
	classifyMaxPerCall = 20
	// classifyWorkers 并发度：模型接口按 QPS 限流的多，压太高只会 429。
	classifyWorkers = 4
)

// aiClient 每次按当前配置构造（一次 SQL 主键查询）。
// 不缓存客户端的原因是「改完立即生效」比省一次查询重要得多。
func (s *Server) aiClient(ctx context.Context) *classify.Client {
	base, _ := s.st.Setting(ctx, settingAIBaseURL, "")
	key, _ := s.st.Setting(ctx, settingAIKey, "")
	model, _ := s.st.Setting(ctx, settingAIModel, "")
	return classify.New(classify.Config{BaseURL: base, APIKey: key, Model: model, Timeout: 30 * time.Second})
}

// aiConfigView GET /admin/ai 的返回。secret 不下发明文，只报 has_key。
type aiConfigView struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	HasKey  bool   `json:"has_key"`
	Ready   bool   `json:"ready"`
}

func (s *Server) handleGetAIConfig(w http.ResponseWriter, r *http.Request) {
	base, _ := s.st.Setting(r.Context(), settingAIBaseURL, "")
	model, _ := s.st.Setting(r.Context(), settingAIModel, "")
	key, _ := s.st.Setting(r.Context(), settingAIKey, "")
	c := s.aiClient(r.Context())
	writeJSON(w, http.StatusOK, aiConfigView{
		Enabled: s.FeatureEnabled(r.Context(), "ai_labeling"),
		BaseURL: base,
		Model:   model,
		HasKey:  key != "",
		Ready:   c.Ready(),
	})
}

// handleSetAIConfig POST /admin/ai {base_url, model, api_key}
// api_key 留空表示不修改——页面拿不到明文，就不能要求用户每次重填。
func (s *Server) handleSetAIConfig(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		APIKey  string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
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
	c := s.aiClient(ctx)
	slog.Info("ai config changed", "model", c.Model(), "ready", c.Ready())
	writeJSON(w, http.StatusOK, map[string]any{"ready": c.Ready()})
}

// classifyResult 单个站点的判定结果（前端逐条显示）。
type classifyResult struct {
	ID       int64  `json:"id"`
	Domain   string `json:"domain"`
	Category string `json:"category"` // 新的分类 key
	Label    string `json:"label"`    // 中文名，前端直接用
	Error    string `json:"error,omitempty"`
}

// handleClassifySites POST /admin/ai/classify {ids:[1,2,3]}
//
// 只写回白名单的 category 字段；索引里的 category 在下次抓取/重建时跟上，
// 不做同步回写——那会让这个接口变成"顺手重建索引"的重活。
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

	// 先取站点信息（串行、单连接），再并发调模型。
	type task struct {
		id     int64
		domain string
		input  classify.Input
	}
	tasks := make([]task, 0, len(req.IDs))
	skipped := make([]classifyResult, 0)
	for _, id := range req.IDs {
		site, ok, err := s.st.SiteByID(r.Context(), id)
		if err != nil || !ok {
			// 静默跳过会让「点了没反应」变成黑箱：把原因带回前端，用户至少知道该找谁。
			msg := "站点不存在或已删除"
			if err != nil {
				msg = "读取站点失败：" + err.Error()
			}
			slog.Warn("classify skip site", "id", id, "err", err)
			skipped = append(skipped, classifyResult{ID: id, Error: msg})
			continue
		}
		titles, _ := s.st.DomainTitles(r.Context(), site.Domain, 8)
		names, _ := s.st.SiteNames(r.Context(), []string{site.Domain})
		tasks = append(tasks, task{
			id: site.ID, domain: site.Domain,
			input: classify.Input{Domain: site.Domain, Name: names[site.Domain], Titles: titles},
		})
	}

	results := make([]classifyResult, len(tasks))
	sem := make(chan struct{}, classifyWorkers)
	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		go func(i int, t task) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			res := classifyResult{ID: t.id, Domain: t.domain}
			key, err := client.Site(ctx, t.input)
			if err != nil {
				res.Error = err.Error()
				res.Category = ""
			} else {
				res.Category = key
				res.Label = sitecat.Label(key)
				if serr := s.st.SetWhitelistCategory(context.Background(), t.id, key); serr != nil {
					res.Error = "写入分类失败：" + serr.Error()
				}
			}
			results[i] = res
		}(i, t)
	}
	wg.Wait()

	results = append(results, skipped...)
	ok, failed := 0, 0
	for _, r := range results {
		if r.Error != "" {
			failed++
		} else {
			ok++
		}
	}
	// 分类改了 SQLite，但搜索结果筛的是索引里的 category 字段，两者会短暂不一致。
	// 按域名小范围重建一次，用户点完筛选立刻有结果，不用等下一轮抓取。
	reindexed := false
	if ok > 0 && s.ReindexDomains != nil {
		domains := make([]string, 0, len(tasks))
		for _, r := range results {
			if r.Error == "" && r.Category != "" {
				domains = append(domains, r.Domain)
			}
		}
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.ReindexDomains(rctx, domains); err != nil {
			slog.Warn("reindex after classify failed", "err", err, "domains", len(domains))
		} else {
			reindexed = true
		}
	}

	slog.Info("ai classify done", "ok", ok, "failed", failed, "reindexed", reindexed, "model", client.Model())
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results, "ok": ok, "failed": failed, "reindexed": reindexed,
	})
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
	key, err := client.Site(ctx, classify.Input{
		Domain:      "github.com",
		Description: "code hosting and collaboration platform",
		Titles:      []string{"GitHub · Build and ship software"},
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "调用失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"category": key, "label": sitecat.Label(key), "model": client.Model(),
		"message": "连通正常，示例站点判定为「" + sitecat.Label(key) + "」",
	})
}

