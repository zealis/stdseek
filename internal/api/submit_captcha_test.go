package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zealis/stdseek/internal/auth"
	"github.com/zealis/stdseek/internal/precheck"
)

// 提交站点是唯一的匿名写入口：它**不吃「人机验证」开关**。
// 关掉登录/注册的验证不该顺带把提交敞开；没配第三方验证时用内置算术题兜底。
func TestSubmitAlwaysRequiresCaptcha(t *testing.T) {
	e := newTestEnv(t, testOpts{
		submit: true,
		checkFn: func(_ context.Context, raw string) precheck.Result {
			return precheck.Result{URL: raw, Reachable: true, StatusCode: 200, ContentType: "text/html"}
		},
	})
	e.srv.Captcha = auth.NewCaptcha()

	// 开关默认关（feature.require_captcha 未写入），但 for=submit 仍应给出验证方式
	req := httptest.NewRequest(http.MethodGet, "/api/auth/captcha?for=submit", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("captcha 接口 status=%d", rec.Code)
	}
	var cap struct {
		Enabled   bool            `json:"enabled"`
		Provider  string          `json:"provider"`
		Challenge *auth.Challenge `json:"challenge"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cap); err != nil {
		t.Fatal(err)
	}
	if !cap.Enabled || cap.Provider != "math" || cap.Challenge == nil {
		t.Fatalf("提交必须始终要求验证，got %+v", cap)
	}

	submit := func(body string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/submit", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		return w.Code
	}

	// 1) 不带验证 → 拒绝
	if code := submit(`{"url":"https://example.com/"}`); code != http.StatusBadRequest {
		t.Fatalf("无验证提交应被拒，status=%d", code)
	}

	// 2) 答对 → 通过
	ch := cap.Challenge
	body := fmt.Sprintf(
		`{"url":"https://example.com/","captcha":{"a":%d,"b":%d,"exp":%d,"sig":%q},"answer":%q}`,
		ch.A, ch.B, ch.Exp, ch.Sig, fmt.Sprint(ch.A+ch.B))
	if code := submit(body); code != http.StatusOK {
		t.Fatalf("答对后应通过，status=%d body=%s", code, rec.Body.String())
	}
}

// 登录/注册的验证仍由开关控制：开关关着时 for 参数不给，就应返回 enabled=false。
func TestCaptchaDefaultStillFollowsFeatureFlag(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	e.srv.Captcha = auth.NewCaptcha()

	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/captcha", nil))
	var got struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("开关关闭时登录/注册不应要求验证")
	}
}
