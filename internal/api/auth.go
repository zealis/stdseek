package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	qrcode "github.com/skip2/go-qrcode"

	"voidxo/internal/auth"
	"voidxo/internal/store"
)

// authMaxBody 认证接口的请求体上限。这些接口只需要几十字节的 JSON，
// 给 8KB 是给「以后加了字段」留余量。
const authMaxBody = 8 << 10

// ---- 通用 ----

// currentUser 取当前登录账号：优先会话 cookie。
func (s *Server) currentUser(r *http.Request) (*store.User, bool) {
	if s.Auth == nil {
		return nil, false
	}
	c, err := r.Cookie(auth.SessionCookie)
	if err != nil || c.Value == "" {
		return nil, false
	}
	u, err := s.Auth.SessionUser(r.Context(), c.Value)
	if err != nil {
		return nil, false
	}
	return u, true
}

// secureRequest 判断是否走 HTTPS（含反代）。
func secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// setSessionCookie 写会话 cookie。
//
// HttpOnly 挡住 XSS 读取；SameSite=Lax 让跨站 POST 不带 cookie（配合下面的
// 同源校验形成两道防线）；Secure 只在 HTTPS 下加，否则本地 http 调试会写不进去。
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.SessionTTL.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// sameOrigin 状态变更接口的同源校验。
//
// SameSite=Lax 已经挡住跨站 POST，但 Lax 对「顶级导航的 GET」是放行的，
// 所以这里再对写接口做一次 Origin 比对。没有 Origin 头说明不是浏览器
// （curl、服务端调用），放行——真正的防护是 cookie 本身。
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// decodeAuthBody 解析 JSON 体并做同源校验。
func decodeAuthBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "跨站请求被拒绝")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, authMaxBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return false
	}
	return true
}

// authErr 把 auth 包的哨兵错误映射成 HTTP 状态与文案。
//
// 401 与 429 的区别很重要：锁定要能被告知（否则用户以为是密码错了），
// 但「账号不存在」和「密码错」统一成 401 的同一句文案，避免枚举。
func authErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrLocked):
		writeErr(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, auth.ErrDisabled):
		writeErr(w, http.StatusForbidden, err.Error())
	case errors.Is(err, auth.ErrTOTPRequired), errors.Is(err, auth.ErrTOTPInvalid):
		writeErr(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, auth.ErrInvalidEmail), errors.Is(err, auth.ErrWeakPassword),
		errors.Is(err, auth.ErrLongPassword), errors.Is(err, auth.ErrEmailTaken):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrBadCreds):
		writeErr(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, auth.ErrCaptcha):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrBadToken), errors.Is(err, auth.ErrNoSuchUser):
		writeErr(w, http.StatusBadRequest, err.Error())
	// 通行密钥的两类「用户可理解」的失败：没登记过、验证没过。
	// 漏在这里会落到 default → 500「服务暂时不可用」，把原因藏起来。
	case errors.Is(err, auth.ErrNoPasskey), errors.Is(err, auth.ErrPasskeyFailed),
		errors.Is(err, auth.ErrPasskeyNeedsDomain):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error("auth internal error", "err", err)
		writeErr(w, http.StatusInternalServerError, "服务暂时不可用")
	}
}

// ---- 注册 / 登录 / 登出 ----

type authCredentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TOTP     string `json:"totp"`
	// 人机验证：题面与答案一起提交（题面带签名，服务端无状态校验）
	Captcha *auth.Challenge `json:"captcha"`
	Answer  string          `json:"answer"`
	// Turnstile 组件回调放进来的 token（provider=turnstile 时用）
	TurnstileToken string `json:"turnstile_token"`
}

// 配置存在 settings 表里，后台改完即时生效（不需要重启）。
const (
	settingCaptchaProvider = "captcha.provider"
	settingCaptchaConfig   = "captcha.config"
)

// captchaConfig 读当前人机验证配置。
//
// 配置在 DB 里，所以后台改完下一次请求就生效——这是「让用户自己选」的前提，
// 塞在启动参数里等于每次换方案都要重启。
func (s *Server) captchaConfig(ctx context.Context) auth.ProviderConfig {
	prov, err := s.st.GetSetting(ctx, settingCaptchaProvider)
	if err != nil || strings.TrimSpace(prov) == "" {
		return auth.ProviderConfig{Provider: auth.ProviderNone}
	}
	cfg := auth.ProviderConfig{Provider: auth.ProviderKind(prov)}
	if raw, err := s.st.GetSetting(ctx, settingCaptchaConfig); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
		// provider 以单独那个 key 为准：避免 JSON 里残留旧值把类型改回去
		cfg.Provider = auth.ProviderKind(prov)
	}
	return cfg
}

// handleCaptcha GET /api/auth/captcha：告诉前端要不要验证、用哪种 provider。
func (s *Server) handleCaptcha(w http.ResponseWriter, r *http.Request) {
	if s.Captcha == nil || !s.FeatureEnabled(r.Context(), "require_captcha") {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	cfg := s.captchaConfig(r.Context())
	if !cfg.Ready() {
		// 配了 provider 但 key 不全：当作不启用，不能把所有人挡在门外
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	out := cfg.PublicView()
	if cfg.Provider == auth.ProviderMath {
		out["challenge"] = s.Captcha.New()
	}
	writeJSON(w, http.StatusOK, out)
}

// checkCaptcha 开关开启时必须过验证；关闭时一律放行。
func (s *Server) checkCaptcha(r *http.Request, ch *auth.Challenge, answer, turnstileToken string) error {
	if s.Captcha == nil || !s.FeatureEnabled(r.Context(), "require_captcha") {
		return nil
	}
	cfg := s.captchaConfig(r.Context())
	if !cfg.Ready() {
		return nil
	}
	if cfg.Provider == auth.ProviderMath {
		if ch == nil || !s.Captcha.Verify(*ch, answer) {
			return auth.ErrCaptcha
		}
		return nil
	}
	ok, err := auth.VerifyToken(r.Context(), cfg, turnstileToken, clientIP(r), nil)
	if err != nil {
		// 第三方验证服务自身故障（连不上、5xx、配置错）时**放行**。
		//
		// 权衡：人机验证是防机器人的，它挂掉时如果拒绝所有人，等于把「第三方宕机」
		// 放大成「全站无法登录」。这类事故比"漏过几个机器人"严重得多。
		// 注意只对 err 放行——token 校验不通过（ok=false）仍然是拒绝。
		slog.Warn("captcha provider unavailable, allowing this attempt",
			"provider", cfg.Provider, "err", err)
		return nil
	}
	if !ok {
		return auth.ErrCaptcha
	}
	return nil
}

// handleRegister POST /api/auth/register
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req authCredentials
	if !decodeAuthBody(w, r, &req) {
		return
	}
	if err := s.checkCaptcha(r, req.Captcha, req.Answer, req.TurnstileToken); err != nil {
		authErr(w, err)
		return
	}
	u, err := s.Auth.Register(r.Context(), req.Email, req.Password, clientIP(r))
	if err != nil {
		authErr(w, err)
		return
	}
	// 验证邮件要尽早发：后面无论走不走自动登录，用户都得先有这封信
	s.maybeSendVerifyEmail(r, u)

	// 开了邮箱验证：不给会话。注册完必须先去邮箱点链接，
	// 否则「验证邮箱」就成了登录时才拦的一道马后炮。
	if s.FeatureEnabled(r.Context(), "require_email_verify") {
		writeJSON(w, http.StatusCreated, map[string]any{"user": u, "need_verify": true})
		return
	}

	// 注册即登录：带着账号去看后台/设置，不用再输一次密码
	_, token, err := s.Auth.Login(r.Context(), req.Email, req.Password, "", clientIP(r), r.UserAgent())
	if err != nil {
		slog.Warn("auto login after register failed", "uid", u.ID, "err", err)
		writeJSON(w, http.StatusCreated, map[string]any{"user": u})
		return
	}
	setSessionCookie(w, r, token)
	writeJSON(w, http.StatusCreated, map[string]any{"user": u})
}

// handleLogin POST /api/auth/login
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req authCredentials
	if !decodeAuthBody(w, r, &req) {
		return
	}
	if err := s.checkCaptcha(r, req.Captcha, req.Answer, req.TurnstileToken); err != nil {
		authErr(w, err)
		return
	}
	u, token, err := s.Auth.Login(r.Context(), req.Email, req.Password, req.TOTP, clientIP(r), r.UserAgent())
	if err != nil {
		authErr(w, err)
		return
	}
	// 邮箱验证开关打开后，未验证的账号不许登录。
	// 这里已经在 auth.Login 里建了会话，所以要撤掉再报错——否则 cookie 已经在响应里了。
	if s.FeatureEnabled(r.Context(), "require_email_verify") && !u.EmailVerified {
		_ = s.Auth.Logout(r.Context(), token)
		clearSessionCookie(w, r)
		writeErr(w, http.StatusForbidden, "邮箱未验证，请先点击验证邮件里的链接")
		return
	}
	setSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

// handleLogout POST /api/auth/logout
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil && c.Value != "" {
		_ = s.Auth.Logout(r.Context(), c.Value)
	}
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleMe GET /api/auth/me：未登录返回 200 + user:null，让前端不用处理 401。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"user": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

// ---- 改密码 / 两步验证 ----

// handleChangePassword POST /api/auth/password（需登录）
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var req struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	if err := s.Auth.ChangePassword(r.Context(), u.ID, req.Current, req.Next); err != nil {
		authErr(w, err)
		return
	}
	// 改密码会清空所有会话，包括当前这条——前端需要重新登录
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleChangeEmail POST /api/auth/email（需登录）
func (s *Server) handleChangeEmail(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var req struct {
		Password string `json:"password"`
		Email    string `json:"email"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	if err := s.Auth.ChangeEmail(r.Context(), u.ID, req.Password, req.Email); err != nil {
		authErr(w, err)
		return
	}
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"message": "邮箱已更新，请用新邮箱重新登录"})
}

// handleTOTPSetup POST /api/auth/totp/setup：生成密钥，返回 otpauth 链接（前端本地画二维码）。
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "请先登录")
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成密钥失败")
		return
	}
	otpauth := auth.OtpauthURL("voidxo", u.Email, secret)
	// 二维码在服务端画成 PNG data URI 直接给前端：省掉一个前端依赖，
	// 也不会把密钥交给任何第三方的二维码服务。
	qr := ""
	if png, err := qrcode.Encode(otpauth, qrcode.Medium, 240); err == nil {
		qr = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret":  secret,
		"otpauth": otpauth,
		"qr":      qr,
	})
}

// handleTOTPEnable POST /api/auth/totp/enable：带一次性验证码确认，防止把密钥配错锁死自己。
func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var req struct {
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	if err := s.Auth.EnableTOTP(r.Context(), u.ID, strings.TrimSpace(req.Secret), req.Code); err != nil {
		authErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleTOTPDisable POST /api/auth/totp/disable：需要当前密码。
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	if err := s.Auth.DisableTOTP(r.Context(), u.ID, req.Password); err != nil {
		authErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- 找回密码 / 邮箱验证 ----

// handleForgot POST /api/auth/forgot
//
// 无论邮箱是否注册都返回同样的成功响应：否则这个接口就成了账号枚举器。
func (s *Server) handleForgot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email          string          `json:"email"`
		Captcha        *auth.Challenge `json:"captcha"`
		Answer         string          `json:"answer"`
		TurnstileToken string          `json:"turnstile_token"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	// 这个接口会触发发信：不设防就是现成的邮件轰炸入口。
	if err := s.checkCaptcha(r, req.Captcha, req.Answer, req.TurnstileToken); err != nil {
		authErr(w, err)
		return
	}
	const okMsg = "如果该邮箱已注册，重置邮件已经发出"
	uid, token, err := s.Auth.StartPasswordReset(r.Context(), req.Email)
	if err != nil {
		// 账号不存在也走这条路径，只是不发信
		slog.Debug("forgot password: no such user", "email", auth.NormalizeEmail(req.Email))
		writeJSON(w, http.StatusOK, map[string]string{"message": okMsg})
		return
	}
	link := s.baseURL(r) + "/reset?token=" + url.QueryEscape(token)
	body := "有人请求重置 voidxo 维空 的登录密码。\n\n" +
		"1 小时内打开下面的链接即可设置新密码：\n" + link + "\n\n" +
		"如果不是你本人操作，忽略这封邮件即可，密码不会改变。"
	if err := s.Mail.Send(r.Context(), req.Email, "重置 voidxo 维空 登录密码", body); err != nil {
		slog.Warn("send reset mail failed", "uid", uid, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": okMsg})
}

// handleReset POST /api/auth/reset
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	if err := s.Auth.FinishPasswordReset(r.Context(), req.Token, req.Password); err != nil {
		authErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "密码已更新，请用新密码登录"})
}

// handleVerifyEmail GET /api/auth/verify?token=xxx
//
// 走 GET 是因为它来自邮件里的链接。验证成功重定向回登录页，
// 失败也给 200 文本——不要给用户一个 JSON 页面。
func (s *Server) handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if err := s.Auth.FinishEmailVerify(r.Context(), token); err != nil {
		http.Redirect(w, r, "/login?verify=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login?verify=ok", http.StatusSeeOther)
}

// maybeSendVerifyEmail 注册后发验证信。失败只记日志，不阻断注册。
func (s *Server) maybeSendVerifyEmail(r *http.Request, u *store.User) {
	token, err := s.Auth.StartEmailVerify(r.Context(), u.ID)
	if err != nil {
		slog.Warn("create verify token failed", "uid", u.ID, "err", err)
		return
	}
	link := s.baseURL(r) + "/api/auth/verify?token=" + url.QueryEscape(token)
	body := "欢迎使用 voidxo 维空。\n\n打开下面的链接完成邮箱验证：\n" + link + "\n\n" +
		"链接 24 小时内有效。没有注册过请忽略这封邮件。"
	if err := s.Mail.Send(r.Context(), u.Email, "验证你的 voidxo 维空 邮箱", body); err != nil {
		slog.Warn("send verify mail failed", "uid", u.ID, "err", err)
	}
}

// baseURL 拼邮件里的绝对链接。优先用配置的公开地址，否则从请求推断
// （反代场景看 X-Forwarded-Proto / Host）。
func (s *Server) baseURL(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

// ---- 后台：账号管理 ----

// handleListUsers GET /admin/users?page=1&size=20
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	page, size := queryInt(r, "page", 1), queryInt(r, "size", 20)
	users, total, err := s.st.ListUsers(r.Context(), page, size, r.URL.Query().Get("kw"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取账号失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "items": users, "page": page})
}

// handleSetUserRole POST /admin/users/{id}/role {role}
//
// 不允许把最后一个管理员降级：降完就没人能进后台了（和服务「删掉最后一个管理员」同理）。
func (s *Server) handleSetUserRole(w http.ResponseWriter, r *http.Request) {
	id := pathInt(r, "id")
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.Role != "admin" && req.Role != "member" {
		writeErr(w, http.StatusBadRequest, "角色只能是 admin 或 member")
		return
	}
	target, err := s.st.UserByID(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if target.Role == "admin" && req.Role == "member" && s.countAdmins(r) <= 1 {
		writeErr(w, http.StatusBadRequest, "这是最后一个管理员，不能降级")
		return
	}
	if err := s.st.SetUserRole(r.Context(), id, req.Role); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleSetUserStatus POST /admin/users/{id}/status {status}
func (s *Server) handleSetUserStatus(w http.ResponseWriter, r *http.Request) {
	id := pathInt(r, "id")
	var req struct {
		Status int `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.Status != 0 && req.Status != 1 {
		writeErr(w, http.StatusBadRequest, "status 只能是 0 或 1")
		return
	}
	if me, ok := s.currentUser(r); ok && me.ID == id && req.Status == 0 {
		writeErr(w, http.StatusBadRequest, "不能禁用自己")
		return
	}
	if err := s.st.SetUserStatus(r.Context(), id, req.Status); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleBatchUsers POST /admin/users/batch {ids, action, value}
//
// 批量比逐个调用更安全：先在整批上做一次护栏判断（不会出现「降级到一半发现是
// 最后一个管理员」的半成品状态）。
func (s *Server) handleBatchUsers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
		Value  string  `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "没有选中任何账号")
		return
	}
	me, _ := s.currentUser(r)

	switch req.Action {
	case "role":
		if req.Value != "admin" && req.Value != "member" {
			writeErr(w, http.StatusBadRequest, "角色只能是 admin 或 member")
			return
		}
		// 降级前算一遍：整批降完还剩不剩管理员
		if req.Value == "member" {
			admins := 0
			users, _, _ := s.st.ListUsers(r.Context(), 1, 1000, "")
			for _, u := range users {
				if u.Role == "admin" && u.Status == 1 {
					admins++
				}
			}
			selected := 0
			for _, u := range users {
				if u.Role == "admin" && u.Status == 1 {
					for _, id := range req.IDs {
						if u.ID == id {
							selected++
						}
					}
				}
			}
			if selected >= admins {
				writeErr(w, http.StatusBadRequest, "不能把管理员全部降级，至少要留一个")
				return
			}
		}
		for _, id := range req.IDs {
			if err := s.st.SetUserRole(r.Context(), id, req.Value); err != nil {
				writeErr(w, http.StatusInternalServerError, "部分账号更新失败")
				return
			}
		}
	case "status":
		disable := req.Value == "0"
		if disable {
			for _, id := range req.IDs {
				if me != nil && me.ID == id {
					writeErr(w, http.StatusBadRequest, "不能禁用自己")
					return
				}
			}
		}
		status := 1
		if disable {
			status = 0
		}
		for _, id := range req.IDs {
			if err := s.st.SetUserStatus(r.Context(), id, status); err != nil {
				writeErr(w, http.StatusInternalServerError, "部分账号更新失败")
				return
			}
		}
	default:
		writeErr(w, http.StatusBadRequest, "action 只能是 role 或 status")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// countAdmins 管理员数量（降级/禁用前的护栏）。
func (s *Server) countAdmins(r *http.Request) int {
	users, _, err := s.st.ListUsers(r.Context(), 1, 1000, "")
	if err != nil {
		return 0
	}
	n := 0
	for _, u := range users {
		if u.Role == "admin" && u.Status == 1 {
			n++
		}
	}
	return n
}

// pathInt 取 URL 路径参数（chi 的 {id}）并转成 int64，非法值返回 0。
func pathInt(r *http.Request, key string) int64 {
	n, err := strconv.ParseInt(chi.URLParam(r, key), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// queryInt 取整数查询参数，非法时用默认值。
func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
		if n > 1_000_000 {
			return def
		}
	}
	if n <= 0 {
		return def
	}
	return n
}

// 保证 time 包被使用（SessionTTL 换算处引用）
var _ = time.Second

// ---- 后台：人机验证配置 ----

// handleGetCaptchaConfig GET /admin/captcha
//
// secret 只回「是否已设置」，不下发明文：后台页面不需要看到它，
// 而一旦下发，它就会出现在浏览器历史、开发者工具和任何截图里。
func (s *Server) handleGetCaptchaConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.captchaConfig(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"provider":   cfg.Provider,
		"site_key":   cfg.SiteKey,
		"endpoint":   cfg.Endpoint,
		"has_secret": cfg.Secret != "",
		"ready":      cfg.Ready(),
	})
}

// handleTestCaptcha POST /admin/captcha/test：检查当前配置的 provider 是否可用。
func (s *Server) handleTestCaptcha(w http.ResponseWriter, r *http.Request) {
	cfg := s.captchaConfig(r.Context())
	msg, err := auth.Probe(r.Context(), cfg, nil)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// handleSetCaptchaConfig POST /admin/captcha {provider, site_key, secret, endpoint}
func (s *Server) handleSetCaptchaConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		SiteKey  string `json:"site_key"`
		Secret   string `json:"secret"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	switch auth.ProviderKind(req.Provider) {
	case auth.ProviderNone, auth.ProviderMath, auth.ProviderTurnstile,
		auth.ProviderGeetest, auth.ProviderVAPTCHA, auth.ProviderCap:
	default:
		writeErr(w, http.StatusBadRequest, "不支持的人机验证类型")
		return
	}

	cfg := s.captchaConfig(r.Context())
	// secret 留空＝保持原值。前端拿不到明文，不能要求它每次重填。
	if strings.TrimSpace(req.Secret) == "" {
		req.Secret = cfg.Secret
	}
	next := auth.ProviderConfig{
		Provider: auth.ProviderKind(req.Provider),
		SiteKey:  strings.TrimSpace(req.SiteKey),
		Secret:   strings.TrimSpace(req.Secret),
		Endpoint: strings.TrimSpace(req.Endpoint),
	}
	raw, _ := json.Marshal(next)
	if err := s.st.SetSetting(r.Context(), settingCaptchaProvider, string(next.Provider)); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	if err := s.st.SetSetting(r.Context(), settingCaptchaConfig, string(raw)); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider": next.Provider,
		"ready":    next.Ready(),
	})
}
