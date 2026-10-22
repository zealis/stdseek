// Package store 提供 SQLite（WAL 模式）元数据层，是白名单/黑名单/审核队列/文档的单一事实源。
// 对应规划书「存储与索引」节：元数据全部落 SQLite，零外部依赖。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"voidxo/internal/urlkey"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，免 CGO（Windows 单二进制关键）
)

// Document 文档元数据（正文内容同时进 Bleve，这里存结构化字段与指纹）。
type Document struct {
	ID           int64     `json:"id"`
	URL          string    `json:"url"`
	Domain       string    `json:"domain"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Labels       string    `json:"labels"`        // JSON 数组，如 ["ai_suspect","ugc_platform"]，索引期预计算
	LabelReasons string    `json:"label_reasons"` // JSON：标签 -> 理由，供可解释展示与误判反馈
	WordCount    int       `json:"word_count"`
	Simhash      uint64    `json:"simhash"`
	PublishedAt  *int64    `json:"published_at"` // 可空；缺失时检索降级用 FetchedAt 并如实标记
	FetchedAt    int64     `json:"fetched_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// WhitelistSite 白名单站点，爬虫种子的唯一来源。
type WhitelistSite struct {
	ID            int64     `json:"id"`
	Domain        string    `json:"domain"`
	Category      string    `json:"category"` // official/opensource/blog/platform/media，供 no个人/no平台 标签复用
	Enabled       bool      `json:"enabled"`
	CrawlInterval int       `json:"crawl_interval"` // 秒，该站两次抓取的最小间隔
	LastCrawledAt *int64    `json:"last_crawled_at"`
	LastStatus    string    `json:"last_status"`
	LastError     string    `json:"last_error"`
	CreatedAt     time.Time `json:"created_at"`
}

// BlacklistSite 黑名单站点，提交审核与爬取两层拦截。
type BlacklistSite struct {
	ID           int64     `json:"id"`
	Domain       string    `json:"domain"`
	Reason       string    `json:"reason"`
	RejectCount  int       `json:"reject_count"`  // 用户提交被拒次数
	BlockedCount int       `json:"blocked_count"` // 爬取被拦截次数
	CreatedAt    time.Time `json:"created_at"`
}

// ReviewItem 审核队列条目（用户提交的网站）。
type ReviewItem struct {
	ID         int64     `json:"id"`
	URL        string    `json:"url"`
	Domain     string    `json:"domain"`
	Category   string    `json:"category"`
	Reason     string    `json:"reason"`   // 用户填写的一句话理由
	Precheck   string    `json:"precheck"` // JSON：可达性/robots/内容类型/黑名单历史，见 internal/precheck.Result
	PrecheckAt *int64    `json:"precheck_at"`
	Status     string    `json:"status"` // pending/approved/rejected
	CreatedAt  time.Time `json:"created_at"`
	ReviewedAt *int64    `json:"reviewed_at"`
}

// Store 包装 *sql.DB。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）SQLite 数据库并执行迁移。
// 连接串强制 WAL + 外键 + 单写者（busy_timeout），符合规划书「SQLite WAL 模式」。
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite 单写者：限制连接数避免 SQLITE_BUSY
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// migrate 建表。字段与规划书功能 I/H/G 对齐。
func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT NOT NULL UNIQUE,
			domain TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			labels TEXT NOT NULL DEFAULT '[]',
			label_reasons TEXT NOT NULL DEFAULT '{}',
			word_count INTEGER NOT NULL DEFAULT 0,
			simhash INTEGER NOT NULL DEFAULT 0,
			published_at INTEGER,
			fetched_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_documents_domain ON documents(domain)`,
		`CREATE TABLE IF NOT EXISTS whitelist (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			domain TEXT NOT NULL UNIQUE,
			category TEXT NOT NULL DEFAULT 'blog',
			enabled INTEGER NOT NULL DEFAULT 1,
			crawl_interval INTEGER NOT NULL DEFAULT 86400,
			last_crawled_at INTEGER,
			last_status TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS blacklist (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			domain TEXT NOT NULL UNIQUE,
			reason TEXT NOT NULL DEFAULT '',
			reject_count INTEGER NOT NULL DEFAULT 0,
			blocked_count INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS review_queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT NOT NULL,
			domain TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'blog',
			reason TEXT NOT NULL DEFAULT '',
			precheck TEXT NOT NULL DEFAULT '{}',
			status TEXT NOT NULL DEFAULT 'pending',
			created_at INTEGER NOT NULL,
			reviewed_at INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_review_status ON review_queue(status)`,
		`CREATE TABLE IF NOT EXISTS outlinks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			from_url TEXT NOT NULL,
			from_domain TEXT NOT NULL,
			to_url TEXT NOT NULL,
			to_domain TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			UNIQUE(from_url, to_url)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_outlinks_to_url ON outlinks(to_url)`,
		`CREATE INDEX IF NOT EXISTS idx_outlinks_to_domain ON outlinks(to_domain)`,
		`CREATE TABLE IF NOT EXISTS prosperity (
			key TEXT PRIMARY KEY,
			score REAL NOT NULL,
			ref_domains INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w\nSQL: %s", err, q)
		}
	}
	// 老库加列（SQLite 不支持 IF NOT EXISTS 加列，重复执行报错忽略）
	for _, q := range []string{
		`ALTER TABLE documents ADD COLUMN label_reasons TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE documents ADD COLUMN word_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE review_queue ADD COLUMN precheck_at INTEGER`,
	} {
		s.db.Exec(q) //nolint:errcheck // duplicate column 为预期错误
	}
	return nil
}

// DomainCategories 批量返回域名分类（含父域匹配），未收录域名不出现在结果中。
// 白名单是策展表（几十到几百行），一次性读入内存做后缀匹配，避免搜索时的 N+1 查询。
func (s *Store) DomainCategories(ctx context.Context, domains []string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain, category FROM whitelist WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type entry struct{ domain, category string }
	var list []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.domain, &e.category); err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make(map[string]string, len(domains))
	for _, d := range domains {
		if _, ok := out[d]; ok {
			continue
		}
		best := ""
		bestLen := -1
		for _, e := range list {
			if d == e.domain || strings.HasSuffix(d, "."+e.domain) {
				if len(e.domain) > bestLen { // 最长后缀优先（子站覆盖父域分类）
					best, bestLen = e.category, len(e.domain)
				}
			}
		}
		if best != "" {
			out[d] = best
		}
	}
	return out, nil
}

// DueSites 返回当前到期待抓的启用站点：
// 从未抓取（last_crawled_at IS NULL），或距上次抓取已超过 crawl_interval 秒。
func (s *Store) DueSites(ctx context.Context, now int64) ([]WhitelistSite, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, domain, category, enabled, crawl_interval, last_crawled_at, last_status, last_error, created_at
		 FROM whitelist
		 WHERE enabled = 1 AND (last_crawled_at IS NULL OR last_crawled_at + crawl_interval <= ?)
		 ORDER BY COALESCE(last_crawled_at, 0) ASC`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WhitelistSite
	for rows.Next() {
		var w WhitelistSite
		var enabled int
		var created int64
		if err := rows.Scan(&w.ID, &w.Domain, &w.Category, &enabled, &w.CrawlInterval,
			&w.LastCrawledAt, &w.LastStatus, &w.LastError, &created); err != nil {
			return nil, err
		}
		w.Enabled = enabled == 1
		w.CreatedAt = time.Unix(created, 0)
		out = append(out, w)
	}
	return out, rows.Err()
}

// SiteCategory 返回站点在白名单中的分类（含父域匹配）；未收录返回空串。
func (s *Store) SiteCategory(ctx context.Context, domain string) (string, error) {
	var cat string
	err := s.db.QueryRowContext(ctx,
		`SELECT category FROM whitelist WHERE enabled = 1 AND (? = domain OR ? LIKE '%.' || domain)
		 ORDER BY LENGTH(domain) DESC LIMIT 1`,
		domain, domain).Scan(&cat)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return cat, nil
}

// GetDocument 按 URL 读取文档元数据；不存在返回 found=false。
func (s *Store) GetDocument(ctx context.Context, url string) (Document, bool, error) {
	var d Document
	var created int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, url, domain, title, description, labels, label_reasons, word_count,
		 simhash, published_at, fetched_at, created_at FROM documents WHERE url = ?`, url).
		Scan(&d.ID, &d.URL, &d.Domain, &d.Title, &d.Description, &d.Labels, &d.LabelReasons,
			&d.WordCount, &d.Simhash, &d.PublishedAt, &d.FetchedAt, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, false, nil
	}
	if err != nil {
		return Document{}, false, err
	}
	d.CreatedAt = time.Unix(created, 0)
	return d, true, nil
}

// DocumentURLs 返回 documents 全表的「归一化 key → 原始 URL」映射，
// 供索引重建时保留 http/https 原始协议（KV 只存归一化 key）。
func (s *Store) DocumentURLs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT url FROM documents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out[urlkey.Key(u)] = u
	}
	return out, rows.Err()
}

// CountDocuments 返回文档总数（启动时写指标用）。
func (s *Store) CountDocuments(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM documents`).Scan(&n)
	return n, err
}

// ---------- 出链 ----------

// ReplaceOutlinks 以「页面 → 出链」为单位整体替换，避免重复累加。
func (s *Store) ReplaceOutlinks(ctx context.Context, fromURL, fromDomain string, targets []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM outlinks WHERE from_url = ?`, fromURL); err != nil {
		return err
	}
	now := time.Now().Unix()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO outlinks (from_url, from_domain, to_url, to_domain, created_at)
		 VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, t := range targets {
		dom := domainFromURL(t)
		if dom == "" || t == fromURL {
			continue
		}
		if _, err := stmt.ExecContext(ctx, fromURL, fromDomain, t, dom, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// OutlinkPair 一条出链的源域名与目标 URL。
type OutlinkPair struct {
	ToURL      string
	FromDomain string
}

// AllOutlinks 返回全部出链（繁荣度统计的输入）。
func (s *Store) AllOutlinks(ctx context.Context) ([]OutlinkPair, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT to_url, from_domain FROM outlinks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutlinkPair
	for rows.Next() {
		var p OutlinkPair
		if err := rows.Scan(&p.ToURL, &p.FromDomain); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------- 繁荣度 ----------

// ProsperityRow 繁荣度记录。
type ProsperityRow struct {
	Key        string
	Score      float64
	RefDomains int
}

// ReplaceProsperity 用新结果整体替换繁荣度表。
func (s *Store) ReplaceProsperity(ctx context.Context, rows []ProsperityRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM prosperity`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR REPLACE INTO prosperity (key, score, ref_domains, updated_at) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().Unix()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Key, r.Score, r.RefDomains, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ProsperityScores 批量读取繁荣度（key 为域名或 host/path 形式的 URL key）。
func (s *Store) ProsperityScores(ctx context.Context, keys []string) (map[string]float64, error) {
	out := map[string]float64{}
	if len(keys) == 0 {
		return out, nil
	}
	seen := map[string]bool{}
	var args []interface{}
	var placeholders []string
	for _, k := range keys {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		placeholders = append(placeholders, "?")
		args = append(args, k)
	}
	if len(args) == 0 {
		return out, nil
	}
	q := `SELECT key, score FROM prosperity WHERE key IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v float64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ---------- 爬虫种子 ----------

// SeedURLs 返回启用白名单站点的种子 URL（https://domain/）。
// 红线：爬虫种子的唯一来源是 whitelist 表，禁止开放 BFS 入口。
func (s *Store) SeedURLs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT domain FROM whitelist WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("seed urls: %w", err)
	}
	defer rows.Close()
	var seeds []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		seeds = append(seeds, "https://"+d+"/")
	}
	return seeds, rows.Err()
}

// IsWhitelisted 判断域名（含父域匹配）是否在启用白名单内，供出链跟进过滤。
func (s *Store) IsWhitelisted(ctx context.Context, domain string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM whitelist WHERE enabled = 1 AND (? = domain OR ? LIKE '%.' || domain)`,
		domain, domain).Scan(&n)
	return n > 0, err
}

// IsBlacklisted 判断域名是否在黑名单内（爬虫拦截层）。
func (s *Store) IsBlacklisted(ctx context.Context, domain string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM blacklist WHERE (? = domain OR ? LIKE '%.' || domain)`,
		domain, domain).Scan(&n)
	return n > 0, err
}

// ---------- 白名单管理 ----------

// ListWhitelist 分页列出白名单（kw 模糊匹配 domain/category）。
func (s *Store) ListWhitelist(ctx context.Context, kw string, limit, offset int) ([]WhitelistSite, int, error) {
	where := `WHERE (? = '' OR domain LIKE '%' || ? || '%' OR category LIKE '%' || ? || '%')`
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM whitelist `+where, kw, kw, kw).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, domain, category, enabled, crawl_interval, last_crawled_at, last_status, last_error, created_at
		 FROM whitelist `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		kw, kw, kw, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []WhitelistSite
	for rows.Next() {
		var w WhitelistSite
		var enabled int
		var created int64
		if err := rows.Scan(&w.ID, &w.Domain, &w.Category, &enabled, &w.CrawlInterval,
			&w.LastCrawledAt, &w.LastStatus, &w.LastError, &created); err != nil {
			return nil, 0, err
		}
		w.Enabled = enabled == 1
		w.CreatedAt = time.Unix(created, 0)
		out = append(out, w)
	}
	return out, total, rows.Err()
}

// AddWhitelist 管理员直接添加站点进白名单。已存在则更新分类与启用态。
func (s *Store) AddWhitelist(ctx context.Context, domain, category string, crawlInterval int) error {
	now := time.Now().Unix()
	if crawlInterval <= 0 {
		crawlInterval = 86400
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO whitelist (domain, category, enabled, crawl_interval, created_at, updated_at)
		 VALUES (?, ?, 1, ?, ?, ?)
		 ON CONFLICT(domain) DO UPDATE SET category=excluded.category, enabled=1,
		 crawl_interval=excluded.crawl_interval, updated_at=excluded.updated_at`,
		domain, category, crawlInterval, now, now)
	return err
}

// SetWhitelistEnabled 启用/禁用白名单站点。
func (s *Store) SetWhitelistEnabled(ctx context.Context, id int64, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET enabled = ?, updated_at = ? WHERE id = ?`, v, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// TouchCrawlResult 回写站点最后抓取状态（爬虫用）。
func (s *Store) TouchCrawlResult(ctx context.Context, domain, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET last_crawled_at = ?, last_status = ?, last_error = ?, updated_at = ?
		 WHERE domain = ?`,
		time.Now().Unix(), status, errMsg, time.Now().Unix(), domain)
	return err
}

// ---------- 黑名单管理 ----------

// ListBlacklist 分页列出黑名单。
func (s *Store) ListBlacklist(ctx context.Context, kw string, limit, offset int) ([]BlacklistSite, int, error) {
	where := `WHERE (? = '' OR domain LIKE '%' || ? || '%' OR reason LIKE '%' || ? || '%')`
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM blacklist `+where, kw, kw, kw).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, domain, reason, reject_count, blocked_count, created_at
		 FROM blacklist `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		kw, kw, kw, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []BlacklistSite
	for rows.Next() {
		var b BlacklistSite
		var created int64
		if err := rows.Scan(&b.ID, &b.Domain, &b.Reason, &b.RejectCount, &b.BlockedCount, &created); err != nil {
			return nil, 0, err
		}
		b.CreatedAt = time.Unix(created, 0)
		out = append(out, b)
	}
	return out, total, rows.Err()
}

// AddBlacklist 添加黑名单域名。
func (s *Store) AddBlacklist(ctx context.Context, domain, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO blacklist (domain, reason, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(domain) DO UPDATE SET reason=excluded.reason`,
		domain, reason, time.Now().Unix())
	return err
}

// RemoveBlacklist 移除黑名单域名。
func (s *Store) RemoveBlacklist(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM blacklist WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ---------- 审核队列 ----------

// CreateSubmission 用户提交网站进审核队列。命中黑名单历史则 reject_count+1。
// 注意：/api/submit 的开放由 API 层开关控制（红线：首批 20-50 站验证前不开放）。
func (s *Store) CreateSubmission(ctx context.Context, item ReviewItem) (int64, error) {
	black, err := s.IsBlacklisted(ctx, item.Domain)
	if err != nil {
		return 0, err
	}
	if black {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE blacklist SET reject_count = reject_count + 1 WHERE domain = ?`,
			item.Domain); err != nil {
			return 0, err
		}
		// 保留调用方写入的可达性/robots 预检结果，只补上黑名单历史标记
		item.Precheck = mergeBlacklistFlag(item.Precheck)
	}
	now := time.Now().Unix()
	var precheckAt interface{}
	if item.Precheck != "" {
		precheckAt = now
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO review_queue (url, domain, category, reason, precheck, precheck_at, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'pending', ?)`,
		item.URL, item.Domain, item.Category, item.Reason, item.Precheck, precheckAt, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListReview 按状态分页列出审核队列。
func (s *Store) ListReview(ctx context.Context, status, kw string, limit, offset int) ([]ReviewItem, int, error) {
	where := `WHERE (? = '' OR status = ?) AND (? = '' OR domain LIKE '%' || ? || '%')`
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM review_queue `+where, status, status, kw, kw).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, url, domain, category, reason, precheck, precheck_at, status, created_at, reviewed_at
		 FROM review_queue `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		status, status, kw, kw, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []ReviewItem
	for rows.Next() {
		var r ReviewItem
		var created int64
		if err := rows.Scan(&r.ID, &r.URL, &r.Domain, &r.Category, &r.Reason,
			&r.Precheck, &r.PrecheckAt, &r.Status, &created, &r.ReviewedAt); err != nil {
			return nil, 0, err
		}
		r.CreatedAt = time.Unix(created, 0)
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// GetReview 按 id 读取审核条目；不存在返回 sql.ErrNoRows。
func (s *Store) GetReview(ctx context.Context, id int64) (ReviewItem, error) {
	var r ReviewItem
	var created int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, url, domain, category, reason, precheck, precheck_at, status, created_at, reviewed_at
		 FROM review_queue WHERE id = ?`, id).
		Scan(&r.ID, &r.URL, &r.Domain, &r.Category, &r.Reason,
			&r.Precheck, &r.PrecheckAt, &r.Status, &created, &r.ReviewedAt)
	if err != nil {
		return ReviewItem{}, err
	}
	r.CreatedAt = time.Unix(created, 0)
	return r, nil
}

// SetReviewPrecheck 写入预检结果（JSON）与检查时间，供审核队列展示。
func (s *Store) SetReviewPrecheck(ctx context.Context, id int64, precheck string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE review_queue SET precheck = ?, precheck_at = ? WHERE id = ?`,
		precheck, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// mergeBlacklistFlag 在已有预检 JSON 上补 blacklist_history=true，保留其它字段。
func mergeBlacklistFlag(pre string) string {
	m := map[string]interface{}{}
	if pre != "" {
		_ = json.Unmarshal([]byte(pre), &m)
	}
	m["blacklist_history"] = true
	b, err := json.Marshal(m)
	if err != nil {
		return `{"blacklist_history":true}`
	}
	return string(b)
}

// ApproveReview 审核通过：审核条目置 approved，域名进白名单。
func (s *Store) ApproveReview(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var domain, category string
	var status string
	err = tx.QueryRowContext(ctx,
		`SELECT domain, category, status FROM review_queue WHERE id = ?`, id).
		Scan(&domain, &category, &status)
	if err != nil {
		return fmt.Errorf("review item: %w", err)
	}
	if status != "pending" {
		return fmt.Errorf("review item %d 状态为 %s，不能重复审核", id, status)
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`UPDATE review_queue SET status = 'approved', reviewed_at = ? WHERE id = ?`, now, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO whitelist (domain, category, enabled, crawl_interval, created_at, updated_at)
		 VALUES (?, ?, 1, 86400, ?, ?)
		 ON CONFLICT(domain) DO UPDATE SET enabled = 1, updated_at = excluded.updated_at`,
		domain, category, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

// RejectReview 审核拒绝：条目置 rejected，域名进黑名单。
func (s *Store) RejectReview(ctx context.Context, id int64, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var domain, status string
	err = tx.QueryRowContext(ctx,
		`SELECT domain, status FROM review_queue WHERE id = ?`, id).Scan(&domain, &status)
	if err != nil {
		return fmt.Errorf("review item: %w", err)
	}
	if status != "pending" {
		return fmt.Errorf("review item %d 状态为 %s，不能重复审核", id, status)
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`UPDATE review_queue SET status = 'rejected', reviewed_at = ? WHERE id = ?`, now, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO blacklist (domain, reason, reject_count, created_at) VALUES (?, ?, 1, ?)
		 ON CONFLICT(domain) DO UPDATE SET reject_count = reject_count + 1, reason = excluded.reason`,
		domain, reason, now); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertDocument 写入/更新文档元数据（爬虫索引用）。
func (s *Store) UpsertDocument(ctx context.Context, doc Document) (int64, error) {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO documents (url, domain, title, description, labels, label_reasons, word_count,
		 simhash, published_at, fetched_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(url) DO UPDATE SET
		 title=excluded.title, description=excluded.description, labels=excluded.labels,
		 label_reasons=excluded.label_reasons, word_count=excluded.word_count,
		 simhash=excluded.simhash, published_at=excluded.published_at,
		 fetched_at=excluded.fetched_at, updated_at=excluded.updated_at`,
		doc.URL, doc.Domain, doc.Title, doc.Description, doc.Labels, doc.LabelReasons,
		doc.WordCount, doc.Simhash, doc.PublishedAt, doc.FetchedAt, now, now)
	if err != nil {
		return 0, err
	}
	if id, err := res.LastInsertId(); err == nil && id > 0 {
		return id, nil
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM documents WHERE url = ?`, doc.URL).Scan(&id)
	return id, err
}

// domainFromURL 提取主机名（内部工具，避免上层重复实现）。
func domainFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
