package summarize

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Endpoint 一个 OpenAI 兼容接口。Name 只用于日志与失败原因（primary/backup/…）。
type Endpoint struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
}

// MultiProvider 按给定顺序逐个尝试接口，任意一个成功即返回。
//
// 顺序由调用方决定：摘要可以让管理员指定「优先用哪一个」，
// 失败后仍按配置顺序把其余接口试完，全部失败才交给 Summarize 降级为抽取式。
// 每次请求构造一个实例，所以 used/model 不需要加锁。
type MultiProvider struct {
	endpoints []Endpoint
	timeout   time.Duration
	used      string
	model     string
}

// NewMultiProvider 过滤掉没填地址或模型的接口；一个都不剩时返回 nil。
func NewMultiProvider(eps []Endpoint, timeout time.Duration) *MultiProvider {
	out := make([]Endpoint, 0, len(eps))
	for _, e := range eps {
		if e.BaseURL == "" || e.Model == "" {
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil
	}
	return &MultiProvider{endpoints: out, timeout: timeout}
}

func (m *MultiProvider) Name() string {
	if m == nil {
		return "extractive"
	}
	model := m.model
	if model == "" && len(m.endpoints) > 0 {
		model = m.endpoints[0].Model
	}
	if model == "" {
		return "extractive"
	}
	return "openai-compat:" + model
}

// UsedEndpoint 成功后实际使用的接口名（空表示还没调用或全部失败）。
func (m *MultiProvider) UsedEndpoint() string {
	if m == nil {
		return ""
	}
	return m.used
}

func (m *MultiProvider) Summarize(ctx context.Context, in Input) ([]Conclusion, error) {
	if m == nil || len(m.endpoints) == 0 {
		return nil, fmt.Errorf("没有可用的模型接口")
	}
	failures := make([]string, 0, len(m.endpoints))
	for _, e := range m.endpoints {
		p := &OpenAICompat{BaseURL: e.BaseURL, APIKey: e.APIKey, Model: e.Model, Timeout: m.timeout}
		cs, err := p.Summarize(ctx, in)
		if err == nil {
			m.used = e.Name
			m.model = e.Model
			return cs, nil
		}
		name := e.Name
		if name == "" {
			name = e.Model
		}
		failures = append(failures, fmt.Sprintf("%s：%v", name, err))
		// 请求已被取消（用户关掉页面/超时）时，继续试下一个只会白等。
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("所有 AI 接口都失败（%s）", strings.Join(failures, "；"))
}
