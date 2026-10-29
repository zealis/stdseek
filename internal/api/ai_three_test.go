package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 后台配了三组接口时，分类客户端按主 → 备用 → 第二备用排列；
// 摘要的优先接口只影响摘要，不改变分类的主备顺序。
func TestAIThreeEndpointsAndSummaryPreference(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	ctx := context.Background()
	set := func(k, v string) {
		if err := e.st.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	set(settingAIBaseURL, "https://p.example.com/v1")
	set(settingAIModel, "p")
	set(settingAIBackupBaseURL, "https://b.example.com/v1")
	set(settingAIBackupModel, "b")
	set(settingAIBackup2BaseURL, "https://b2.example.com/v1")
	set(settingAIBackup2Model, "b2")

	if got := strings.Join(e.srv.AIClient(ctx).EndpointNames(), ","); got != "primary,backup,backup2" {
		t.Fatalf("分类接口顺序应为主→备用→第二备用，got %s", got)
	}

	names := func() string {
		var out []string
		for _, ep := range e.srv.summaryEndpoints(ctx) {
			out = append(out, ep.Name)
		}
		return strings.Join(out, ",")
	}

	set(settingAISummaryEndpoint, endpointBackup2)
	if got := names(); got != "backup2,primary,backup" {
		t.Fatalf("摘要优先接口应排在最前，got %s", got)
	}

	// 选中的接口没配好：退回自然顺序，不能让整个摘要不可用。
	set(settingAIBackup2BaseURL, "")
	set(settingAIBackup2Model, "")
	if got := names(); got != "primary,backup" {
		t.Fatalf("优先接口未配置时应退回自然顺序，got %s", got)
	}
}

// 三组接口的配置读写：密钥只回 has_key，摘要优先接口要能保存并回读。
func TestAdminAIThirdEndpointAndSummaryPreference(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})

	body := `{"base_url":"https://p.example.com/v1","model":"pm","api_key":"secret-p",` +
		`"backup_base_url":"https://b.example.com/v1","backup_model":"bm","backup_api_key":"secret-b",` +
		`"backup2_base_url":"https://b2.example.com/v1","backup2_model":"b2m","backup2_api_key":"secret-b2",` +
		`"summary_endpoint":"backup2"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/ai", strings.NewReader(body))
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存失败 status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/ai", nil)
	req.Header.Set("X-Admin-Token", "tok")
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("读取失败 status=%d body=%s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	for _, secret := range []string{"secret-p", "secret-b", "secret-b2"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("后台接口不应下发密钥明文，泄漏了 %s：%s", secret, raw)
		}
	}
	var out struct {
		Backup2Ready    bool   `json:"backup2_ready"`
		Backup2HasKey   bool   `json:"backup2_has_key"`
		SummaryEndpoint string `json:"summary_endpoint"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Backup2Ready || !out.Backup2HasKey {
		t.Fatalf("第二备用应显示已配置，got ready=%v has_key=%v", out.Backup2Ready, out.Backup2HasKey)
	}
	if out.SummaryEndpoint != endpointBackup2 {
		t.Fatalf("摘要优先接口应回读 backup2，got %q", out.SummaryEndpoint)
	}

	// 不在词表里的优先接口要挡住，免得存成一个永远匹配不上的值。
	req = httptest.NewRequest(http.MethodPost, "/admin/ai", strings.NewReader(`{"summary_endpoint":"third"}`))
	req.Header.Set("X-Admin-Token", "tok")
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法优先接口应 400，got %d body=%s", rec.Code, rec.Body.String())
	}
}

// 主接口与备用都挂时，分类请求仍要能落到第二备用。
func TestAITestFallsBackToSecondBackup(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	ctx := context.Background()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
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
	set(settingAIBaseURL, bad.URL)
	set(settingAIModel, "p")
	set(settingAIBackupBaseURL, bad.URL)
	set(settingAIBackupModel, "b")
	set(settingAIBackup2BaseURL, good.URL)
	set(settingAIBackup2Model, "b2")

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
	if out.Endpoint != endpointBackup2 {
		t.Fatalf("主与备用都挂了应走第二备用，got %q", out.Endpoint)
	}
	if !e.srv.AIFailoverState(ctx) {
		t.Fatal("走第二备用后也应记录故障期")
	}
}
