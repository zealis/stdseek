// Cloudflare Turnstile 服务端校验。
//
// 相比内置算术题，它是「真」人机验证：托管在 Cloudflare，风控模型持续更新，
// 用户通常只需勾一下甚至无感通过。代价是要去 Cloudflare 拿一对 key，
// 并且页面上会加载一段第三方脚本（对「无追踪」的承诺是个例外，README 里要写清）。
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// Turnstile Turnstile 校验器。
type Turnstile struct {
	secret string
	client *http.Client
}

// NewTurnstile 构造。secret 为空时返回 nil，调用方据此判断「没配」。
func NewTurnstile(secret string) *Turnstile {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil
	}
	return &Turnstile{secret: secret, client: &http.Client{Timeout: 5 * time.Second}}
}

// Verify 校验前端拿到的 token。
//
// 失败原因分两类：网络/配置问题（返回 error）与「token 无效」（返回 false, nil）。
// 前者应该记日志，后者只是用户没过验证，不必告警。
func (t *Turnstile) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	form := url.Values{}
	form.Set("secret", t.secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnstileVerifyURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK {
		return false, errors.New("turnstile: status " + resp.Status)
	}

	var out struct {
		Success    bool     `json:"success"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, err
	}
	return out.Success, nil
}
