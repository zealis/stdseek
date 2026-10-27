// 人机验证的 provider 抽象。
//
// 设计目标：后台能选、能填各自的 key，切换不需要重启。
// 所以配置存 DB（settings 表），而不是启动参数——启动参数只作为首次部署的默认值。
//
// 四家云服务的验证流程各不相同，但对外都是同一个动作：
// 「前端拿到一个 token → 服务端拿 token 去问厂商 / 自己算 → 通过与否」。
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProviderKind 人机验证的实现。
type ProviderKind string

const (
	// ProviderNone 关闭（不需要验证）
	ProviderNone ProviderKind = "none"
	// ProviderMath 内置算术题（零依赖降级方案）
	ProviderMath ProviderKind = "math"
	// ProviderTurnstile Cloudflare Turnstile
	ProviderTurnstile ProviderKind = "turnstile"
	// ProviderGeetest 极验 Geetest v4
	ProviderGeetest ProviderKind = "geetest"
	// ProviderVAPTCHA VAPTCHA（华为旗下）
	ProviderVAPTCHA ProviderKind = "vaptcha"
	// ProviderCap Cap（自托管 PoW，需要用户自己部署一个 cap 服务）
	ProviderCap ProviderKind = "cap"
)

// ProviderConfig 一套 provider 配置。
//
// SiteKey 会下发给前端（公开），Secret 只留在服务端。
// Endpoint 只有 Cap 用：它是自托管方案，没有统一的服务地址。
type ProviderConfig struct {
	Provider ProviderKind `json:"provider"`
	SiteKey  string       `json:"site_key"`
	Secret   string       `json:"secret"`
	Endpoint string       `json:"endpoint"`
}

// Enabled 是否需要验证。
func (c ProviderConfig) Enabled() bool {
	return c.Provider != "" && c.Provider != ProviderNone
}

// Ready 配置是否完整到能工作（缺 key 时应当视为未启用，而不是让所有人登录失败）。
func (c ProviderConfig) Ready() bool {
	switch c.Provider {
	case ProviderMath:
		return true
	case ProviderTurnstile, ProviderGeetest, ProviderVAPTCHA:
		return c.SiteKey != "" && c.Secret != ""
	case ProviderCap:
		return c.SiteKey != "" && c.Secret != "" && c.Endpoint != ""
	}
	return false
}

// PublicView 下发给前端的部分（绝不包含 Secret）。
func (c ProviderConfig) PublicView() map[string]any {
	out := map[string]any{
		"enabled":  c.Ready(),
		"provider": string(c.Provider),
		"site_key": c.SiteKey,
	}
	if c.Provider == ProviderCap {
		out["endpoint"] = c.Endpoint
	}
	return out
}

// VerifyToken 按 provider 校验前端提交的 token。
//
// 返回 (ok, error)：ok=false 是「用户没过验证」，error 非空是「我们这边出了问题」
// （网络、配置错误）。两者的日志级别不同，调用方要区分。
func VerifyToken(ctx context.Context, cfg ProviderConfig, token, remoteIP string, httpc *http.Client) (bool, error) {
	token = strings.TrimSpace(token)
	if !cfg.Ready() {
		return true, nil // 没配好等于不启用，不能把人挡在外面
	}
	if token == "" {
		return false, nil
	}
	if httpc == nil {
		httpc = &http.Client{Timeout: 5 * time.Second}
	}
	switch cfg.Provider {
	case ProviderTurnstile:
		return verifyTurnstile(ctx, httpc, cfg.Secret, token, remoteIP)
	case ProviderGeetest:
		// Geetest v4 的 token 是一个 JSON 对象（前端整体提交），单独解析
		return verifyGeetest(ctx, httpc, cfg.Secret, token)
	case ProviderVAPTCHA:
		return verifyVaptcha(ctx, httpc, cfg.SiteKey, cfg.Secret, token, remoteIP)
	case ProviderCap:
		return verifyCap(ctx, httpc, cfg.Endpoint, cfg.Secret, token)
	}
	// math 走另一条路（题面 + 答案，不是 token）
	return false, errors.New("未知的人机验证类型")
}

// ---- 各家实现 ----

func verifyTurnstile(ctx context.Context, c *http.Client, secret, token, ip string) (bool, error) {
	form := url.Values{"secret": {secret}, "response": {token}}
	if ip != "" {
		form.Set("remoteip", ip)
	}
	return postFormBool(ctx, c, "https://challenges.cloudflare.com/turnstile/v0/siteverify", form, "success")
}

// verifyGeetest 极验 v4：token 是前端 onSuccess 拿到的四个字段组成的 JSON。
//
// 签名规则（官方）：sign_token = HMAC-SHA256(lot_number, captcha_key)，十六进制小写。
func verifyGeetest(ctx context.Context, c *http.Client, secret, token string) (bool, error) {
	var payload struct {
		LotNumber     string `json:"lot_number"`
		CaptchaOutput string `json:"captcha_output"`
		PassToken     string `json:"pass_token"`
		GenTime       string `json:"gen_time"`
	}
	if err := json.Unmarshal([]byte(token), &payload); err != nil {
		return false, nil // 前端传错格式：当作没过，不报内部错误
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload.LotNumber))
	signToken := hex.EncodeToString(mac.Sum(nil))

	form := url.Values{
		"lot_number":     {payload.LotNumber},
		"captcha_output": {payload.CaptchaOutput},
		"pass_token":     {payload.PassToken},
		"gen_time":       {payload.GenTime},
		"sign_token":     {signToken},
	}
	// 请求地址要带 captcha_id：前端把它一起塞进了 token JSON（见 GeetestBox）
	endpoint := "https://gcaptcha4.geetest.com/validate?captcha_id=" + url.QueryEscape(siteKeyFromToken(token))
	return postFormBool(ctx, c, endpoint, form, "result")
}

// siteKeyFromToken 从 Geetest 的 token 里取 captcha_id（前端会带上）。
func siteKeyFromToken(token string) string {
	var payload struct {
		CaptchaID string `json:"captcha_id"`
	}
	_ = json.Unmarshal([]byte(token), &payload)
	return payload.CaptchaID
}

// verifyVaptcha：POST api.vaptcha.com/v2/validate，success=1 通过。
func verifyVaptcha(ctx context.Context, c *http.Client, vid, key, token, ip string) (bool, error) {
	form := url.Values{
		"id":        {vid},
		"secretkey": {key},
		"scene":     {"1"},
		"token":     {token},
	}
	if ip != "" {
		form.Set("ip", ip)
	}
	return postFormBool(ctx, c, "https://api.vaptcha.com/v2/validate", form, "success")
}

// verifyCap：Cap 是自托管方案，验证端点在用户的实例上。
func verifyCap(ctx context.Context, c *http.Client, endpoint, secret, token string) (bool, error) {
	body, _ := json.Marshal(map[string]string{"secret": secret, "response": token})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(endpoint, "/")+"/siteverify", strings.NewReader(string(body)))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return false, err
	}
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, err
	}
	return out.Success, nil
}

// postFormBool 提交表单并按指定字段判定成败。
//
// 各家的成功字段名不同（success / result / status），但形态一致，
// 用一个通用函数避免四份几乎相同的代码。
// 注意：Turnstile 的 success 是布尔，Geetest 的是字符串 "success"，
// 这里统一按 JSON 原始值判断。
func postFormBool(ctx context.Context, c *http.Client, endpoint string, form url.Values, field string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("captcha: status %s", resp.Status)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, err
	}
	switch v := out[field].(type) {
	case bool:
		return v, nil
	case string:
		return v == "success" || v == "1", nil
	case float64:
		return v == 1, nil
	}
	return false, nil
}
