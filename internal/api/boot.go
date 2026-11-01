package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// 首屏要用的最小状态，直接写进 index.html。
//
// 原来这两样要各发一次请求（/api/features、/api/status），它们由 main.js 发起，
// 于是形成「HTML → main.js → XHR → 页面才稳定」的关键链：Lighthouse 会把这条链
// 算成 1.2 秒左右的关键路径。而这两个值在服务端渲染 HTML 时本来就知道，
// 没有理由让浏览器再问一次。
//
// 只放「首屏判断用得到」的东西：功能位（决定注册入口是否出现）与文档数
// （决定空库引导是否出现）。其余统计仍然走 /api/status。
type bootSeed struct {
	Features map[string]bool `json:"features"`
	// Documents 只有查成功才下发；查失败时整个 boot 不注入，
	// 前端退回原来的「发请求再更新」路径，避免把一次数据库抖动放大成「显示空库」。
	Documents int64 `json:"documents"`
}

// bootDocTTL 文档数的缓存时长。
//
// 每次 HTML 请求都 COUNT 一遍是可以，但首页是唯一的高频 HTML 入口，
// 而「文档数」差几十条对首屏判断没有影响（只用来区分「0 篇」和「有内容」）。
const bootDocTTL = 30 * time.Second

// bootSeed 组装首屏状态；文档数查不到时返回 false。
func (s *Server) bootSeed(ctx context.Context) (bootSeed, bool) {
	docs, err := s.bootDocuments(ctx)
	if err != nil {
		slog.Warn("boot seed: count documents failed", "err", err)
		return bootSeed{}, false
	}
	return bootSeed{Features: s.FeatureSnapshot(ctx), Documents: docs}, true
}

// bootDocuments 带缓存的文档数。
func (s *Server) bootDocuments(ctx context.Context) (int64, error) {
	s.bootMu.Lock()
	if !s.bootAt.IsZero() && time.Since(s.bootAt) < bootDocTTL {
		n := s.bootDocs
		s.bootMu.Unlock()
		return n, nil
	}
	s.bootMu.Unlock()

	n, err := s.st.CountDocuments(ctx)
	if err != nil {
		return 0, err
	}

	s.bootMu.Lock()
	s.bootDocs, s.bootAt = n, time.Now()
	s.bootMu.Unlock()
	return n, nil
}

// injectBoot 把首屏状态写进 HTML（在 </head> 之前）。
func injectBoot(page []byte, seed bootSeed) []byte {
	raw, err := json.Marshal(seed)
	if err != nil {
		return page
	}
	// json.Marshal 会把 < > & 转义，混进 </script> 也逃不出去。
	script := []byte("<script>window.__STDSEEK_BOOT__=" + string(raw) + ";</script>")
	return bytes.Replace(page, []byte("</head>"), append(script, []byte("</head>")...), 1)
}
