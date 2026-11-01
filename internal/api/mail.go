package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/zealis/stdseek/internal/mail"
)

// settingMailConfig SMTP 配置在 settings 表里的 key。
//
// 存 DB 而不是只认启动参数：邮箱验证和找回密码都依赖它，后台必须有地方改。
// 启动参数只在「首次部署」时作为初始值写进来，之后一切以后台为准。
const settingMailConfig = "mail.config"

// handleGetMailConfig GET /admin/mail
//
// 密码只回「是否已设置」，不下发明文——后台页面不需要看到它。
func (s *Server) handleGetMailConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.Mail.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"host":     cfg.Host,
		"port":     cfg.Port,
		"user":     cfg.User,
		"from":     cfg.From,
		"secure":   cfg.Secure,
		"has_pass": cfg.Pass != "",
		"enabled":  cfg.Host != "",
	})
}

// handleSetMailConfig POST /admin/mail {host, port, user, pass, from, secure}
func (s *Server) handleSetMailConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host   string `json:"host"`
		Port   int    `json:"port"`
		User   string `json:"user"`
		Pass   string `json:"pass"`
		From   string `json:"from"`
		Secure bool   `json:"secure"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.Port < 0 || req.Port > 65535 {
		writeErr(w, http.StatusBadRequest, "端口不合法")
		return
	}
	// 密码留空＝保持原值。前端拿不到明文，不能要求它每次重填。
	// 但「清空服务器地址」是明确的关闭动作，此时密码也要一起清掉，
	// 否则会留一个看不出归属的凭据。
	if strings.TrimSpace(req.Host) == "" {
		req.Pass = ""
	} else if strings.TrimSpace(req.Pass) == "" {
		req.Pass = s.Mail.Config().Pass
	}
	cfg := mail.Config{
		Host:   strings.TrimSpace(req.Host),
		Port:   req.Port,
		User:   strings.TrimSpace(req.User),
		Pass:   req.Pass,
		From:   strings.TrimSpace(req.From),
		Secure: req.Secure,
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	if err := s.st.SetSetting(r.Context(), settingMailConfig, string(raw)); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	// 即时生效：不重启也能发信
	s.Mail.Reload(cfg)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": cfg.Host != ""})
}

// handleTestMail POST /admin/mail/test {to}
//
// 单独给一个「发测试信」的入口：SMTP 参数对不对只有真发一封才知道，
// 否则要等到用户注册失败才能发现配置写错。
func (s *Server) handleTestMail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To string `json:"to"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	to := strings.TrimSpace(req.To)
	if to == "" {
		writeErr(w, http.StatusBadRequest, "请填写收件地址")
		return
	}
	if !s.Mail.Enabled() {
		writeErr(w, http.StatusBadRequest, "还没有配置 SMTP，填完再试")
		return
	}
	body := "这是一封测试邮件。\n\n收到它说明 SMTP 配置可用，" +
		"邮箱验证与找回密码的邮件也能正常发出。"
	if err := s.Mail.Send(r.Context(), to, "StdSeek · SMTP 测试", body); err != nil {
		writeErr(w, http.StatusBadGateway, "发送失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "已发送，请查收 " + to})
}
