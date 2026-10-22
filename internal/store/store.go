// Package store 提供 SQLite（WAL 模式）元数据层，是白名单/黑名单/审核队列/文档的单一事实源。
// 对应规划书「存储与索引」节：元数据全部落 SQLite，零外部依赖。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，免 CGO（Windows 单二进制关键）
)

// Document 文档元数据（正文内容同时进 Bleve，这里存结构化字段与指纹）。
type Document struct {
	ID          int64     `json:"id"`
	URL         string    `json:"url"`
	Domain      string    `json:"domain"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Labels      string    `json:"labels"` // JSON 数组，如 ["ai_suspect","ugc_platform"]，索引期预计算
	Simhash     uint64    `json:"simhash"`
	PublishedAt *int64    `json:"published_at"` // 可空；缺失时检索降级用 FetchedAt 并如实标记
	FetchedAt   int64     `json:"fetched_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// WhitelistSite 白名单站点，爬虫种子的唯一来源。
type WhitelistSite struct {
	ID            int64      `json:"id"`
	Domain        string     `json:"domain"`
	Category      string     `json:"category"` // official/opensource/blog/platform/media，供 no个人/no平台 标签复用
	Enabled       bool       `json:"enabled"`
	CrawlInterval int        `json:"crawl_interval"` // 秒，该站两次抓取的最小间隔
	LastCrawledAt *int64     `json:"last_crawled_at"`
	LastStatus    string     `json:"last_status"`
	LastError     string     `json:"last_error"`
	CreatedAt     time.Time  `json:"created_at"`
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
	Reason     string    `json:"reason"`  // 用户填写的一句话理由
	Precheck   string    `json:"precheck"` // JSON：可达性/robots/内容类型初判/黑名单历史
	Status     string    `json:"status"`  // pending/approved/rejected
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
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w\nSQL: %s", err, q)
		}
	}
	return nil
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
		item.Precheck = `{"blacklist_history":true}`
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO review_queue (url, domain, category, reason, precheck, status, created_at)
		 VALUES (?, ?, ?, ?, ?, 'pending', ?)`,
		item.URL, item.Domain, item.Category, item.Reason, item.Precheck, time.Now().Unix())
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
		`SELECT id, url, domain, category, reason, precheck, status, created_at, reviewed_at
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
			&r.Precheck, &r.Status, &created, &r.ReviewedAt); err != nil {
			return nil, 0, err
		}
		r.CreatedAt = time.Unix(created, 0)
		out = append(out, r)
	}
	return out, total, rows.Err()
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
		`INSERT INTO documents (url, domain, title, description, labels, simhash, published_at, fetched_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(url) DO UPDATE SET
		 title=excluded.title, description=excluded.description, labels=excluded.labels,
		 simhash=excluded.simhash, published_at=excluded.published_at,
		 fetched_at=excluded.fetched_at, updated_at=excluded.updated_at`,
		doc.URL, doc.Domain, doc.Title, doc.Description, doc.Labels, doc.Simhash,
		doc.PublishedAt, doc.FetchedAt, now, now)
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
