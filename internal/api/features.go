// 功能开关：后台可改、即时生效、落 SQLite。
//
// 设计取舍：
//   - 白名单而非自由 KV。后台只能改这里登记过的 key，避免一个写接口变成
//     "任意配置随便灌"的入口（拼错的 key 不会静默产生一条永远读不到的记录）。
//   - 标题与说明放在服务端：新加一个开关只改 Go 侧一处，前端自动显示，
//     不需要"后端加字段 + 前端加一行"两处同步。
//   - 默认值写死在代码里而不是建表时插入：没写过 = 用默认值，
//     这样改默认值不需要迁移已有数据库。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"voidxo/internal/ua"
)

// featureDef 一个可由后台开关的功能。
type featureDef struct {
	Key     string // 对外 key，也是前端用的标识
	DBKey   string // 落库 key（加前缀，避免与其他 settings 项撞名）
	Label   string
	Desc    string
	Default bool
	// Danger 为 true 时前端用告警色渲染：开启会带来成本或风险。
	Danger bool
}

// featureDefs 开关登记表。新增开关只改这里 + 前端不用动（列表接口会带出去）。
var featureDefs = []featureDef{
	{
		Key:   "ai_summary",
		DBKey: "feature.ai_summary",
		Label: "原文摘要",
		Desc: "在结果页为每条结果摘录原文要点，只依据这一页的正文。" +
			"默认抽取式（直接摘原文句子，不调用模型）；部署时用 -llm-* 配置摘要模型才会调用。" +
			"每条结论都挂原文引用，没有可靠原文时不生成。",
	},
	{
		Key:   "ai_overview",
		DBKey: "feature.ai_overview",
		Label: "AI 概览",
		Desc: "在搜索结果上方生成一段 AI 回答，只依据本次搜索到的页面。" +
			"只有提问式查询（如「X 是什么」「如何…」）才生成，普通关键词不调用模型；" +
			"首次生成消耗一次模型调用，结果缓存 6 小时，重复搜索不再调用。",
		Danger: true,
	},
	{
		Key:   "ai_labeling",
		DBKey: "feature.ai_labeling",
		Label: "AI 站点分类",
		Desc: "调用大模型判断白名单站点属于哪一类（官方/新闻阅读/开发工具…），" +
			"结果写回站点分类，搜索结果可按分类过滤。需先在「AI 接口」里填好地址与模型；" +
			"每判定一个站点消耗一次模型调用。",
		Danger: true,
	},
	{
		Key:     "related_links",
		DBKey:   "feature.related_links",
		Label:   "相关搜索",
		Desc:    "结果页右栏展示「深入了解 X」与 Bing 联想词。关闭后前端不再请求，也不显示该模块",
		Default: true,
	},
	{
		Key:     "wiki_panel",
		DBKey:   "feature.wiki_panel",
		Label:   "维基百科知识面板",
		Desc:    "搜索单个词时，在结果右栏展示该词的维基百科解释。需要服务端能访问 zh.wikipedia.org，默认关闭",
		Default: false,
	},
	{
		Key:   "require_email_verify",
		DBKey: "feature.require_email_verify",
		Label: "邮箱验证",
		Desc:  "开启后，未验证邮箱的账号无法登录。必须先配好 SMTP，否则会把所有人锁在门外。",
	},
	{
		Key:   "require_captcha",
		DBKey: "feature.require_captcha",
		Label: "人机验证",
		Desc: "注册与登录时要求通过下方选定的人机验证方式。能挡住批量脚本，但挡不住定向攻击——" +
			"真正的防护是同 IP 限流。",
	},
	{
		Key:   "allow_user_auth",
		DBKey: "feature.allow_user_auth",
		Label: "普通用户注册与登录",
		// 关掉只是关「入口」，不是关「账号」：已登录的会话继续有效，
		// 管理员账号始终能登录——否则一关就把自己也锁在门外。
		Desc:    "关闭后新用户无法注册、普通用户无法登录，只有管理员账号能进；已登录的会话不受影响。",
		Default: true,
	},
	{
		Key:    "browser_ua",
		DBKey:  ua.BrowserFeatureKey,
		Label:  "浏览器 UA",
		Desc:   "抓取与预检优先用 Chrome 的 User-Agent。",
		Danger: true,
	},
}

// featureByKey 按对外 key 查登记项。
func featureByKey(key string) (featureDef, bool) {
	for _, f := range featureDefs {
		if f.Key == key {
			return f, true
		}
	}
	return featureDef{}, false
}

// FeatureEnabled 读取开关当前状态。读失败时返回默认值并记录日志——
// 配置读不到不该让搜索/摘要链路直接 500，退回"默认关闭"是最安全的方向。
func (s *Server) FeatureEnabled(ctx context.Context, key string) bool {
	def, ok := featureByKey(key)
	if !ok {
		return false
	}
	v, err := s.st.Setting(ctx, def.DBKey, "")
	if err != nil {
		slog.Warn("read feature flag failed", "key", key, "err", err)
		return def.Default
	}
	if v == "" {
		return def.Default
	}
	on, err := strconv.ParseBool(v)
	if err != nil {
		return def.Default
	}
	return on
}

// FeatureSnapshot 全部开关的当前状态（启动日志与健康检查用）。
func (s *Server) FeatureSnapshot(ctx context.Context) map[string]bool {
	out := make(map[string]bool, len(featureDefs))
	for _, def := range featureDefs {
		out[def.Key] = s.FeatureEnabled(ctx, def.Key)
	}
	return out
}

// featureView 后台看到的一项开关。
type featureView struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Desc      string `json:"desc"`
	Enabled   bool   `json:"enabled"`
	Danger    bool   `json:"danger,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"` // 从没改过为 0
}

// handleListFeatures GET /admin/settings：列出全部开关及当前状态。
func (s *Server) handleListFeatures(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.Settings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取配置失败")
		return
	}
	items := make([]featureView, 0, len(featureDefs))
	for _, def := range featureDefs {
		on := def.Default
		var updated int64
		if row, ok := rows[def.DBKey]; ok {
			if v, perr := strconv.ParseBool(row.Value); perr == nil {
				on = v
			}
			updated = row.UpdatedAt
		}
		items = append(items, featureView{
			Key: def.Key, Label: def.Label, Desc: def.Desc,
			Enabled: on, Danger: def.Danger, UpdatedAt: updated,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"features": items})
}

// handleSetFeature POST /admin/settings/feature {key, enabled}
// 只允许改登记过的开关；key 未知直接 400，不静默忽略。
func (s *Server) handleSetFeature(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		Key     string `json:"key"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	def, ok := featureByKey(req.Key)
	if !ok {
		writeErr(w, http.StatusBadRequest, "未知的开关："+req.Key)
		return
	}
	// 护栏：没有 SMTP 就要求邮箱验证，等于把所有人永久锁在门外
	// （验证信发不出去，谁也验证不了）。宁可不让开，也不能让人自己把自己锁死。
	if req.Key == "require_email_verify" && req.Enabled && !s.Mail.Enabled() {
		writeErr(w, http.StatusBadRequest, "请先配置 SMTP 并发送测试邮件成功，再开启邮箱验证")
		return
	}
	if err := s.st.SetSetting(r.Context(), def.DBKey, strconv.FormatBool(req.Enabled)); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入配置失败")
		return
	}
	// 开关变动必须留痕：事后排查"为什么昨天开始有摘要"时，这行是唯一线索。
	slog.Info("feature flag changed", "key", def.Key, "enabled", req.Enabled, "at", time.Now().Format(time.RFC3339))
	writeJSON(w, http.StatusOK, map[string]interface{}{"key": def.Key, "enabled": req.Enabled})
}

// handlePublicFeatures GET /api/features：前端据此决定是否渲染原文摘要入口。
// 只返回 key→bool，不暴露开关说明与更新时间（后台信息不外泄）。
func (s *Server) handlePublicFeatures(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"features": s.FeatureSnapshot(r.Context())})
}

// handleStatus GET /api/status 运行状态：给前端与运维看「有没有数据可用」。
//
// 与 /healthz 的分工：healthz 只答"进程活着"（给探活用），
// status 答"索引里有多少东西、开了哪些功能"（给首页与人工排查用）。
// 首次部署最容易踩的坑就是 exe 起来了但索引是空的，首页搜什么都是 0 条，
// 用户会以为程序坏了——首页拿这个数字做引导。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	docs, err := s.st.CountDocuments(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取文档数失败")
		return
	}
	sites := 0
	if _, total, err := s.st.ListWhitelist(r.Context(), "", 0, 0); err == nil {
		sites = total
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"documents": docs,
		"sites":     sites,
		"features":  s.FeatureSnapshot(r.Context()),
		// 缓存命中率：判断"缓存是否真的有用"，也为将来调容量提供依据
		"search_cache":  s.scache.Stats(),
		"snippet_cache": s.snippet.Stats(),
	})
}
