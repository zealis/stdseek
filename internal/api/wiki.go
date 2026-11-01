// 维基百科摘要代理。
//
// 目的：用户搜「一个词」（不是句子）时，在结果右栏顶部展示该词的维基百科解释，
// 类似谷歌的知识面板。懒加载：结果先出，卡片异步补上，失败静默不显示。
//
// 为什么走服务端代理：浏览器直连 zh.wikipedia.org 在国内不可达；服务端请求
// 会遵循进程的系统代理设置（海外部署或本机有代理时可用），失败时前端降级为
// 不显示，绝不阻塞搜索主流程。
//
// 缓存：同一个词只查一次（SQLite wiki_cache，7 天过期——词条可能更新）。
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zealis/stdseek/internal/store"
)

// wikiSource 维基百科 REST 摘要接口。返回 200 时 body 里 extract 是纯文本摘要。
const wikiSource = "https://zh.wikipedia.org/api/rest_v1/page/summary/"

// wikiTTL 缓存有效期。
const wikiTTL = 7 * 24 * time.Hour

// handleWiki GET /api/wiki?q=词
//
// 「一个词」的定义：非空、不含空白、长度 ≤ 30 字符。句子、长串一律 404——
// 前端据此决定不显示卡片，而不是展示一段错误的解释。
func (s *Server) handleWiki(w http.ResponseWriter, r *http.Request) {
	// 开关默认关闭：不查上游、不写缓存，直接让前端静默。
	if !s.FeatureEnabled(r.Context(), "wiki_panel") {
		writeErr(w, http.StatusNotFound, "未开启维基百科面板")
		return
	}
	// 先去不可见字符再判断「是不是一个词」：零宽字符能骗过空白检查，
	// 然后被拼进上游 URL 与本地缓存键里。
	q := stripInvisible(r.URL.Query().Get("q"))
	if q == "" || strings.ContainsAny(q, " \t\n") || utf8.RuneCountInString(q) > 30 {
		writeErr(w, http.StatusNotFound, "仅支持单个词")
		return
	}

	// 缓存命中且未过期直接返回
	if s.st != nil {
		if hit, err := s.st.WikiCacheGet(r.Context(), q); err == nil &&
			time.Since(time.Unix(hit.ResolvedAt, 0)) < wikiTTL {
			writeJSON(w, http.StatusOK, hit)
			return
		}
	}

	info, err := fetchWiki(r.Context(), q)
	if err != nil {
		// 上游失败是常态（国内可达性），降级为 404 让前端静默，不打断搜索
		slog.Debug("wiki fetch failed", "q", q, "err", err)
		writeErr(w, http.StatusNotFound, "没有找到维基百科条目")
		return
	}

	if s.st != nil {
		if err := s.st.WikiCachePut(r.Context(), q, info); err != nil {
			slog.Debug("wiki cache put failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, info)
}

// fetchWiki 请求维基 REST 摘要并提取需要的字段。
func fetchWiki(ctx context.Context, q string) (store.WikiEntry, error) {
	var out store.WikiEntry

	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		wikiSource+url.PathEscape(q), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Accept", "application/json")
	// 维基对没有 UA 的请求可能拒绝
	req.Header.Set("User-Agent", "github.com/zealis/stdseek/1.0 (search engine; +https://github.com/zealis/stdseek)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, errUpstream{status: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return out, err
	}

	var d struct {
		Title       string `json:"title"`
		Extract     string `json:"extract"`
		Type        string `json:"type"`
		ContentURLs struct {
			Desktop struct {
				Page string `json:"page"`
			} `json:"desktop"`
		} `json:"content_urls"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return out, err
	}
	// type=disambiguation 是消歧义页，没有实质解释，不展示
	if d.Type == "disambiguation" || strings.TrimSpace(d.Extract) == "" {
		return out, errUpstream{status: http.StatusNotFound}
	}

	out.Title = d.Title
	out.Extract = d.Extract
	out.URL = d.ContentURLs.Desktop.Page
	if out.URL == "" {
		out.URL = "https://zh.wikipedia.org/wiki/" + url.PathEscape(d.Title)
	}
	return out, nil
}

// errUpstream 上游返回了非 200。
type errUpstream struct{ status int }

func (e errUpstream) Error() string { return "upstream status " + http.StatusText(e.status) }

// ensureWikiTable 建缓存表（放 store 初始化之外的惰性执行会引入竞态，
// 这里在 Server 启动时调用一次）。
func ensureWikiTable(db *sql.DB) {
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS wiki_cache (
		q        TEXT PRIMARY KEY,
		title    TEXT NOT NULL DEFAULT '',
		extract  TEXT NOT NULL DEFAULT '',
		url      TEXT NOT NULL DEFAULT '',
		ts       INTEGER NOT NULL
	)`)
}
