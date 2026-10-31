// 域名 WHOIS：注册时间是排序质量分的信任度信号（老域名小幅加权）。
//
// 数据获取约定：
//   - 默认接口 xxapi.cn（v2.xxapi.cn/api/whois?domain=），管理员可在后台换成
//     自定义接口（settings: whois.api_url，URL 里用 {domain} 占位）；
//   - 自定义接口取不到（网络/格式/超时）自动退回默认接口——一个坏配置
//     不该让整条 whois 数据断供；
//   - 查询结果写进 whitelist.whois_created_at，90 天才复查一次；
//     查不到的域名也推进复查时间，否则每轮扫描都会打一遍上游。
//
// 扫描节奏：WhoisSweep 由调用方定时驱动（main 里 30 分钟一轮，每轮最多 5 个域名），
// 首次部署 25 个域名的冷启动在几个小时内自然补齐，不与抓取抢带宽。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"voidxo/internal/whois"
)

const (
	settingWhoisAPIURL = "whois.api_url"
	// WhoisStaleAfter 注册时间的复查间隔：whois 数据几乎不变，
	// 90 天复查只为捕捉域名易主这种极小概率事件。
	WhoisStaleAfter = 90 * 24 * time.Hour
)

// WhoisDefaultURL 默认接口（xxapi v2）。
const WhoisDefaultURL = whois.DefaultURL

// whoisURL 生效的查询接口：后台自定义优先，空/读失败回落默认。
func (s *Server) whoisURL(ctx context.Context) string {
	if v, err := s.st.GetSetting(ctx, settingWhoisAPIURL); err == nil {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return whois.DefaultURL
}

// handleGetWhoisConfig GET /admin/whois：当前配置与默认值。
func (s *Server) handleGetWhoisConfig(w http.ResponseWriter, r *http.Request) {
	custom := ""
	if v, err := s.st.GetSetting(r.Context(), settingWhoisAPIURL); err == nil {
		custom = strings.TrimSpace(v)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"api_url":     custom,
		"default_url": whois.DefaultURL,
	})
}

// handleSetWhoisConfig POST /admin/whois {api_url}：保存自定义接口。
// 空串 = 恢复默认。校验只看形态（http(s) + {domain} 占位），接口通不通
// 由 sweep 实际取数时验证——失效自动退回默认，不用管理员盯着。
func (s *Server) handleSetWhoisConfig(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		APIURL string `json:"api_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	apiURL := strings.TrimSpace(req.APIURL)
	if apiURL != "" {
		if !strings.Contains(apiURL, "{domain}") {
			writeErr(w, http.StatusBadRequest, "自定义接口必须包含 {domain} 占位符")
			return
		}
		if !strings.HasPrefix(apiURL, "http://") && !strings.HasPrefix(apiURL, "https://") {
			writeErr(w, http.StatusBadRequest, "接口地址必须是 http(s) URL")
			return
		}
	}
	if err := s.st.SetSetting(r.Context(), settingWhoisAPIURL, apiURL); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入配置失败")
		return
	}
	slog.Info("whois api url updated", "custom", apiURL != "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// WhoisSweep 扫一轮需要查询的域名（每轮最多 limit 个），查询并写回注册时间。
// 由 main 的定时器驱动；每次都读当前生效接口，自定义失效自动退回默认。
func (s *Server) WhoisSweep(ctx context.Context, limit int) {
	staleBefore := time.Now().Add(-WhoisStaleAfter).Unix()
	domains, err := s.st.DomainsNeedingWhois(ctx, staleBefore, limit)
	if err != nil {
		slog.Warn("whois sweep: list failed", "err", err)
		return
	}
	if len(domains) == 0 {
		return
	}
	apiURL := s.whoisURL(ctx)
	custom := apiURL != whois.DefaultURL
	ok, fail := 0, 0
	var failed []string
	for _, d := range domains {
		created, err := whois.Fetch(ctx, nil, apiURL, d)
		if err != nil && custom {
			// 自定义接口失效：退回默认接口再试一次。
			slog.Warn("whois custom api failed, fallback to default", "domain", d, "err", err)
			created, err = whois.Fetch(ctx, nil, whois.DefaultURL, d)
		}
		if err != nil {
			fail++
			failed = append(failed, d)
			// 失败的域名 6 小时后重试：限流/超时这类瞬时故障
			// 不该被 90 天的复查间隔锁死；真没有记录的域名也只是每 6 小时打一次。
			retryAt := time.Now().Add(-WhoisStaleAfter + 6*time.Hour).Unix()
			if err := s.st.SetWhoisCheckedAt(ctx, d, retryAt); err != nil {
				slog.Warn("whois sweep: persist retry failed", "domain", d, "err", err)
			}
			continue
		}
		if err := s.st.SetWhoisCreated(ctx, d, created.Unix()); err != nil {
			slog.Warn("whois sweep: persist failed", "domain", d, "err", err)
			continue
		}
		ok++
		slog.Info("whois sweep: domain resolved", "domain", d, "registered", created.Format("2006-01-02"))
	}
	if fail > 0 {
		slog.Warn("whois sweep done with failures, retry in 6h", "total", len(domains), "ok", ok, "failed", fail, "domains", strings.Join(failed, ","))
		return
	}
	slog.Info("whois sweep done", "total", len(domains), "ok", ok)
}
