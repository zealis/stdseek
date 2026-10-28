// Package store 提供 SQLite（WAL 模式）元数据层，是白名单/黑名单/审核队列/文档的单一事实源。
// 对应规划书「存储与索引」节：元数据全部落 SQLite，零外部依赖。
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
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
	ShingleHash  uint64    `json:"shingle_hash"` // 正文 shingle 指纹，跨站转载判定用
	PublishedAt  *int64    `json:"published_at"` // 可空；缺失时检索降级用 FetchedAt 并如实标记
	FetchedAt    int64     `json:"fetched_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// WhitelistSite 白名单站点，爬虫种子的唯一来源。
type WhitelistSite struct {
	ID       int64  `json:"id"`
	Domain   string `json:"domain"`
	Category string `json:"category"` // official/opensource/blog/platform/media，供 no个人/no平台 标签复用
	// SeedPath 抓取入口路径（默认 "/"）。平台站首页以导航和站务链接为主，
	// 指向文章列表页（如 /pick/、/blogs）才能抓到真正的技术内容。
	SeedPath string `json:"seed_path"`
	// ArticlePattern 文章 URL 路径的正则（空 = 不限制）。
	// 平台站的频道页/标签页/空间页/作者页正文是摘要列表，链接密度拦不住，
	// 只能按 URL 形态收口；种子页本身不受此约束。
	ArticlePattern string `json:"article_pattern"`
	Enabled        bool   `json:"enabled"`
	CrawlInterval  int    `json:"crawl_interval"` // 秒，该站两次抓取的最小间隔
	LastCrawledAt  *int64 `json:"last_crawled_at"`
	LastStatus     string `json:"last_status"`
	LastError      string `json:"last_error"`
	// FailCount 连续失败次数：成功清零，失败累加；到期计算据此做指数退避。
	FailCount int       `json:"fail_count"`
	CreatedAt time.Time `json:"created_at"`
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
	ID       int64  `json:"id"`
	URL      string `json:"url"`
	Domain   string `json:"domain"`
	Category string `json:"category"`
	Reason   string `json:"reason"` // 用户填写的一句话理由
	// Ticket 是提交凭证（公开查询用）。自增 id 可枚举，会让任何人都能翻别人提交的站点，
	// 所以对外只认这个随机串；后台仍用 id。
	Ticket     string    `json:"ticket,omitempty"`
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
			shingle_hash INTEGER NOT NULL DEFAULT 0,
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
			site_name TEXT NOT NULL DEFAULT '',
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
		// 标签误判反馈：规划书红线要求 ai_suspect 等概率性标签「必须可解释 + 可反馈」。
		// 一行 = 一个用户对 (页面, 标签) 的一次判断；client 是 IP 的哈希，用于去重且不存原 IP。
		`CREATE TABLE IF NOT EXISTS label_feedback (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT NOT NULL,
			domain TEXT NOT NULL DEFAULT '',
			label TEXT NOT NULL,
			verdict TEXT NOT NULL,
			note TEXT NOT NULL DEFAULT '',
			client TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE(url, label, client)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_label_feedback_label ON label_feedback(label, verdict)`,
		// 运行期开关：key-value 存后台可改的功能开关与阈值。
		// 不用配置文件的理由——单二进制部署时改开关不该要求重启，
		// 而重启会中断正在进行的抓取与索引。
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL
		)`,
		// 账号：只有一个管理员角色的极简体系，但表结构留了扩展位。
		// 邮箱统一小写存取（归一化在 auth 层做）——大小写混存会让「登录失败但确实注册过」变成常态。
		`CREATE TABLE IF NOT EXISTS users (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			email          TEXT NOT NULL UNIQUE,
			password_hash  TEXT NOT NULL,
			role           TEXT NOT NULL DEFAULT 'member',
			status         INTEGER NOT NULL DEFAULT 1,
			email_verified INTEGER NOT NULL DEFAULT 0,
			totp_secret    TEXT NOT NULL DEFAULT '',
			totp_enabled   INTEGER NOT NULL DEFAULT 0,
			created_at     INTEGER NOT NULL,
			last_login     INTEGER NOT NULL DEFAULT 0,
			last_login_ip  TEXT NOT NULL DEFAULT '',
			reg_ip         TEXT NOT NULL DEFAULT ''
		)`,
		// IP 封禁：pattern 支持通配（1.2.3.*），expires_at=0 表示永久。
		// 命中在中间件里拦（login/register/submit/feedback 这些写入口），首页只读不受影响。
		`CREATE TABLE IF NOT EXISTS ip_bans (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			pattern    TEXT NOT NULL UNIQUE,
			reason     TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL DEFAULT 0
		)`,
		// 归属地缓存：IP → 文本 + 信誉数据。解析走用户配置的外部接口，同一个 IP 只查一次。
		`CREATE TABLE IF NOT EXISTS geo_cache (
			ip          TEXT PRIMARY KEY,
			label       TEXT NOT NULL DEFAULT '',
			resolved_at INTEGER NOT NULL,
			asn         INTEGER NOT NULL DEFAULT 0,
			score       INTEGER NOT NULL DEFAULT 0,
			status      TEXT NOT NULL DEFAULT '',
			risk        TEXT NOT NULL DEFAULT '[]'
		)`,
		// 维基百科摘要缓存：搜索「单个词」时右栏展示解释用。同一词只查一次上游。
		`CREATE TABLE IF NOT EXISTS wiki_cache (
			q        TEXT PRIMARY KEY,
			title    TEXT NOT NULL DEFAULT '',
			extract  TEXT NOT NULL DEFAULT '',
			url      TEXT NOT NULL DEFAULT '',
			ts       INTEGER NOT NULL
		)`,
		// 登录事件：每次登录/注册成功记一条，用于「最近登录的设备」与同 IP 多账号聚合。
		// 和 sessions 的区别：sessions 是「当前有效的凭证」，这里留的是历史轨迹，
		// 登出/过期后仍然保留（用户要看的是「谁在什么时候从哪登过」）。
		`CREATE TABLE IF NOT EXISTS login_events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			uid        INTEGER NOT NULL,
			ip         TEXT NOT NULL DEFAULT '',
			ua         TEXT NOT NULL DEFAULT '',
			kind       TEXT NOT NULL DEFAULT 'login',
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_login_events_uid ON login_events(uid, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_login_events_ip ON login_events(ip)`,
		// 通行密钥（WebAuthn）。credential_id 用 base64url 存，public_key 是 COSE 公钥原始字节。
		// 一个账号可以有多把（手机 + 笔记本各一把），所以不建唯一约束在 uid 上。
		`CREATE TABLE IF NOT EXISTS passkeys (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			uid           INTEGER NOT NULL,
			credential_id TEXT NOT NULL UNIQUE,
			public_key    BLOB NOT NULL,
			sign_count    INTEGER NOT NULL DEFAULT 0,
			-- flags 是认证器标志位的位掩码（UP/UV/BE/BS）。必须存：
			-- 登录校验时库里还原出来的凭证如果和认证器报的 BE/BS 不一致，
			-- go-webauthn 会直接判定 "Backup Eligible flag inconsistency" 并失败。
			flags         INTEGER NOT NULL DEFAULT 0,
			aaguid        TEXT NOT NULL DEFAULT '',
			name          TEXT NOT NULL DEFAULT '',
			created_at    INTEGER NOT NULL,
			last_used     INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_passkeys_uid ON passkeys(uid)`,
		// 会话：库里只存 token 的 sha256，明文只出现在 HttpOnly cookie 里。
		// 这样即使数据库泄露，也不能直接拿去登录。
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash TEXT PRIMARY KEY,
			uid        INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			ip         TEXT NOT NULL DEFAULT '',
			ua         TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_uid ON sessions(uid)`,
		// 邮件一次性令牌：kind 区分「验证邮箱」与「重置密码」，用后置 used=1 而不是删除，
		// 便于排查「链接点了没反应」是过期还是已被用过。
		`CREATE TABLE IF NOT EXISTS email_tokens (
			token_hash TEXT PRIMARY KEY,
			uid        INTEGER NOT NULL,
			kind       TEXT NOT NULL,
			expires_at INTEGER NOT NULL,
			used       INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_email_tokens_uid ON email_tokens(uid, kind)`,
		// 每日用量：按天累计检索/抓取/入库/摘要次数。
		// 只为「看趋势」服务，不做账务——所以丢了也不影响检索，允许内存缓冲后批量落盘。
		// 用量维度：按天聚合「谁在调 API」与「请求来自哪个地区」。
		// 用 kind 区分维度而不是建两张表——以后加「UA 分布」之类不必再改表结构。
		`CREATE TABLE IF NOT EXISTS usage_dim (
			day   TEXT NOT NULL,
			kind  TEXT NOT NULL,
			name  TEXT NOT NULL,
			count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (day, kind, name)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_daily (
			day       TEXT PRIMARY KEY,
			searches  INTEGER NOT NULL DEFAULT 0,
			succeeded INTEGER NOT NULL DEFAULT 0,
			api       INTEGER NOT NULL DEFAULT 0,
			hits      INTEGER NOT NULL DEFAULT 0,
			crawls    INTEGER NOT NULL DEFAULT 0,
			indexed   INTEGER NOT NULL DEFAULT 0,
			summaries INTEGER NOT NULL DEFAULT 0
		)`,
	}
	// 旧库补列：SQLite 的 ADD COLUMN 对已存在的列会报错，忽略即可
	// （新建库由上面的 CREATE TABLE 直接带上该列）。
	for _, alter := range []string{
		`ALTER TABLE documents ADD COLUMN shingle_hash INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_daily ADD COLUMN succeeded INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_daily ADD COLUMN api INTEGER NOT NULL DEFAULT 0`,
	} {
		_, _ = s.db.Exec(alter)
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
		`ALTER TABLE whitelist ADD COLUMN fail_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE passkeys ADD COLUMN flags INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE geo_cache ADD COLUMN asn INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE geo_cache ADD COLUMN score INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE geo_cache ADD COLUMN status TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE geo_cache ADD COLUMN risk TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE geo_cache ADD COLUMN isp TEXT NOT NULL DEFAULT ''`,
		// seed_path 把抓取入口从「站点首页」改成「站点内某个列表页」。
		// 平台站（博客园/思否/InfoQ）首页几乎全是导航与站务链接，
		// 真实语料里抓回的是「服务协议」「关于我们」「作者主页」这类噪声页。
		`ALTER TABLE whitelist ADD COLUMN seed_path TEXT NOT NULL DEFAULT '/'`,
		// article_pattern 只收文章页。平台站的频道/标签/空间/作者页（
		// developer.aliyun.com/group/mse、cloud.tencent.com/developer/tag/18107）
		// 正文是摘要列表，链接密度拦不住，会把抓取配额吃光——
		// 真实数据：阿里云 23 篇入库文档里 0 篇是文章。
		`ALTER TABLE whitelist ADD COLUMN article_pattern TEXT NOT NULL DEFAULT ''`,
		// ticket 是用户提交后拿到的查询凭证。用自增 id 做查询会让所有提交可被枚举，
		// 随机串不可猜，也不需要账号体系。
		`ALTER TABLE review_queue ADD COLUMN ticket TEXT NOT NULL DEFAULT ''`,
		// 部分索引：历史行 ticket 为空，空串不能参与唯一约束。
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_review_ticket ON review_queue(ticket) WHERE ticket <> ''`,
		// site_name：爬虫从站点首页提取的网站名称（og:site_name，缺省 <title>），
		// 搜索结果用它替代裸域名展示。首页没爬到之前是空串，结果页回退域名。
		`ALTER TABLE whitelist ADD COLUMN site_name TEXT NOT NULL DEFAULT ''`,
		// 内容去重：dup_of 指向正本 URL（空表示自己是正本），dup_count 记录被折叠的数量。
		// 只折叠不删除：副本的 URL 仍然要能查（用户可能直接搜到它），只是不入索引。
		`ALTER TABLE documents ADD COLUMN dup_of TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE documents ADD COLUMN dup_count INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS idx_documents_dup_of ON documents(dup_of)`,
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
		`SELECT id, domain, seed_path, article_pattern, category, enabled, crawl_interval, last_crawled_at, last_status, last_error, fail_count, created_at
		 FROM whitelist
		 WHERE enabled = 1 AND (
		   last_crawled_at IS NULL OR
		   last_crawled_at + crawl_interval *
		     CASE fail_count WHEN 0 THEN 1 WHEN 1 THEN 2 WHEN 2 THEN 4 WHEN 3 THEN 8 ELSE 16 END <= ?
		 )
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
		if err := rows.Scan(&w.ID, &w.Domain, &w.SeedPath, &w.ArticlePattern, &w.Category, &enabled, &w.CrawlInterval,
			&w.LastCrawledAt, &w.LastStatus, &w.LastError, &w.FailCount, &created); err != nil {
			return nil, err
		}
		w.Enabled = enabled == 1
		w.CreatedAt = time.Unix(created, 0)
		out = append(out, w)
	}
	return out, rows.Err()
}

// SiteCategory 返回站点在白名单中的分类（含父域匹配）；未收录返回空串。
// SiteNames 批量返回域名 → 网站名称（含父域匹配，同 DomainCategories 的后缀规则）。
// site_name 由爬虫抓站点首页时提取（og:site_name），未爬到为空串、不出现在结果里。
func (s *Store) SiteNames(ctx context.Context, domains []string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain, site_name FROM whitelist WHERE enabled = 1 AND site_name <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type entry struct{ domain, name string }
	var list []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.domain, &e.name); err != nil {
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
				if len(e.domain) > bestLen {
					best, bestLen = e.name, len(e.domain)
				}
			}
		}
		if best != "" {
			out[d] = best
		}
	}
	return out, nil
}

// UpdateSiteName 记录爬虫从站点首页提取的网站名称（仅当当前为空时写入，
// 避免后续某次首页改版/抓取失败把已有名称抹掉）。
func (s *Store) UpdateSiteName(ctx context.Context, domain, name string) error {
	if domain == "" || name == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET site_name = ? WHERE domain = ? AND site_name = ''`, name, domain)
	return err
}

// AllSiteCategories 返回全部启用站点的域名 → 分类。
// 调用方（reindex）自己复刻 SiteCategory 的最长后缀匹配，
// 避免每篇文档一次 SQL 往返——whitelist 只有几十行，全量加载一次就够。
func (s *Store) AllSiteCategories(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT domain, category FROM whitelist WHERE enabled = 1`)
	if err != nil {
		return nil, fmt.Errorf("site categories: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var d, c string
		if err := rows.Scan(&d, &c); err != nil {
			return nil, err
		}
		out[d] = c
	}
	return out, rows.Err()
}

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

// DeleteDocument 删除文档元数据行，返回是否命中（配合索引/KV 一起下架坏文档）。
func (s *Store) DeleteDocument(ctx context.Context, rawURL string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM documents WHERE url = ?`, rawURL)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetDocument 按 URL 读取文档元数据；不存在返回 found=false。
func (s *Store) GetDocument(ctx context.Context, url string) (Document, bool, error) {
	var d Document
	var created int64
	// simhash 是 64 位无符号哈希，SQLite INTEGER 只能存有符号 64 位，
	// 这里按位模式转换（int64 ↔ uint64），不影响汉明距离计算。
	var simhash int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, url, domain, title, description, labels, label_reasons, word_count,
		 simhash, published_at, fetched_at, created_at FROM documents WHERE url = ?`, url).
		Scan(&d.ID, &d.URL, &d.Domain, &d.Title, &d.Description, &d.Labels, &d.LabelReasons,
			&d.WordCount, &simhash, &d.PublishedAt, &d.FetchedAt, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, false, nil
	}
	if err != nil {
		return Document{}, false, err
	}
	d.Simhash = uint64(simhash)
	d.CreatedAt = time.Unix(created, 0)
	return d, true, nil
}

// Fingerprint 去重所需的文档指纹与选正本依据。
type Fingerprint struct {
	URL     string
	Domain  string
	Simhash uint64
	// ShingleHash 正文 3-gram shingle 指纹（去标题、去停用词）。
	// 与 Simhash 并存：同站近重复用 Simhash（已按版本镜像调好阈值），
	// 跨站转载用 ShingleHash——词袋指纹在跨站场景下无关页面距离只有 7~10，
	// 不敢放宽阈值，等于只能认出「逐字节相同」的搬运。
	ShingleHash uint64
	WordCount   int
	PublishedAt *int64
	FetchedAt   int64
	// DupOf / DupCount 上一轮的结论，用于判断标记是否已过期
	DupOf    string
	DupCount int
}

// Fingerprints 返回全部文档的指纹，供 dedup 聚类。
func (s *Store) Fingerprints(ctx context.Context) ([]Fingerprint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT url, domain, simhash, shingle_hash, word_count, published_at, fetched_at, dup_of, dup_count
		 FROM documents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fingerprint
	for rows.Next() {
		var f Fingerprint
		var simhash, shingle int64
		if err := rows.Scan(&f.URL, &f.Domain, &simhash, &shingle, &f.WordCount, &f.PublishedAt,
			&f.FetchedAt, &f.DupOf, &f.DupCount); err != nil {
			return nil, err
		}
		f.Simhash = uint64(simhash)
		f.ShingleHash = uint64(shingle)
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetDupInfo 写入去重结论：dupOf 为空表示自己是正本，dupCount 是被折叠的数量。
func (s *Store) SetDupInfo(ctx context.Context, url, dupOf string, dupCount int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE documents SET dup_of = ?, dup_count = ?, updated_at = ? WHERE url = ?`,
		dupOf, dupCount, time.Now().Unix(), url)
	return err
}

// InboundCounts 批量读取「URL → 语料内反向链接数」（有多少个已收录页面链到它）。
//
// 为什么不用 prosperity 表：该表按 URL 树节点累加后做了归一化，在策展白名单里
// 几乎饱和——294 篇入索引文档中 204 篇没有路径级条目，其余全都在最低档，
// 区分度为 0。而这里的原始计数有明显差异（0 ~ 25），是「深网发现」唯一可用的
// 冷热信号：引用越少，越可能是被埋住的内容。
func (s *Store) InboundCounts(ctx context.Context, urls []string) (map[string]int, error) {
	out := map[string]int{}
	if len(urls) == 0 {
		return out, nil
	}
	seen := map[string]bool{}
	var args []interface{}
	for _, u := range urls {
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		args = append(args, u)
	}
	// COUNT(*) 而不是 COUNT(DISTINCT from_url)：表上有 UNIQUE(from_url, to_url)，
	// 同一 (from, to) 组合只有一行，所以对固定 to_url 来说 from_url 不可能重复。
	// 实测（12 万行 outlinks、50 个 to_url）DISTINCT 版 2.5~20ms，
	// 占四个信号查询的六到九成——去重结构是白建的。
	rows, err := s.db.QueryContext(ctx,
		`SELECT to_url, COUNT(*) FROM outlinks WHERE to_url IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`) GROUP BY to_url`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		var n int
		if err := rows.Scan(&u, &n); err != nil {
			return nil, err
		}
		out[u] = n
	}
	return out, rows.Err()
}

// DupCountsByURLs 批量读取「URL → 被折叠的副本数」，结果页用来解释「已聚合」。
func (s *Store) DupCountsByURLs(ctx context.Context, urls []string) (map[string]int, error) {
	out := make(map[string]int, len(urls))
	if len(urls) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(urls)), ",")
	args := make([]interface{}, len(urls))
	for i, u := range urls {
		args[i] = u
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT url, dup_count FROM documents WHERE dup_count > 0 AND url IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		var n int
		if err := rows.Scan(&u, &n); err != nil {
			return nil, err
		}
		out[u] = n
	}
	return out, rows.Err()
}

// LabelReasonsByURLs 批量返回「URL → 标签理由」，用于结果页解释为什么打这个标签。
// 索引里只存标签本身（查询期只做过滤），理由留在 SQLite；结果页最多十几条，
// 一次 IN 查询即可，不必为它重建索引。
func (s *Store) LabelReasonsByURLs(ctx context.Context, urls []string) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string, len(urls))
	if len(urls) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(urls)), ",")
	args := make([]interface{}, len(urls))
	for i, u := range urls {
		args[i] = u
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT url, label_reasons FROM documents WHERE url IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u, raw string
		if err := rows.Scan(&u, &raw); err != nil {
			return nil, err
		}
		m := map[string]string{}
		if raw != "" {
			_ = json.Unmarshal([]byte(raw), &m) // 历史数据损坏时静默降级为空理由
		}
		out[u] = m
	}
	return out, rows.Err()
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

// DedupeDocuments 按归一化 URL key 去重 documents 表，每组保留 id 最大的一条。
// 索引层早已按 key 去重，但元数据表会把同一页面的锚点变体各写一行
// （真实语料：InfoQ 一个页面 6 行），清理后两边的口径才一致。
func (s *Store) DedupeDocuments(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, url FROM documents`)
	if err != nil {
		return 0, err
	}
	type entry struct {
		id  int64
		url string
	}
	var all []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.url); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	keyOf := func(u string) string {
		if k := urlkey.Key(u); k != "" {
			return k
		}
		return u
	}
	keep := make(map[string]int64, len(all))
	for _, e := range all {
		k := keyOf(e.url)
		if e.id > keep[k] {
			keep[k] = e.id
		}
	}

	removed := 0
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `DELETE FROM documents WHERE id = ?`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, e := range all {
		if keep[keyOf(e.url)] == e.id {
			continue
		}
		if _, err := stmt.ExecContext(ctx, e.id); err != nil {
			return 0, err
		}
		removed++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
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

// SeedURLs 返回启用白名单站点的种子 URL（https://domain + seed_path）。
// 红线：爬虫种子的唯一来源是 whitelist 表，禁止开放 BFS 入口。
// WhitelistDomains 返回所有启用站点的域名（爬虫预加载繁荣度用）。
func (s *Store) WhitelistDomains(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT domain FROM whitelist WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("whitelist domains: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) SeedURLs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT domain, seed_path FROM whitelist WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("seed urls: %w", err)
	}
	defer rows.Close()
	var seeds []string
	for rows.Next() {
		var d, p string
		if err := rows.Scan(&d, &p); err != nil {
			return nil, err
		}
		seeds = append(seeds, "https://"+d+NormalizeSeedPath(p))
	}
	return seeds, rows.Err()
}

// NormalizeSeedPath 归一化抓取入口路径：空值回退 "/"，缺前导斜杠补上。
func NormalizeSeedPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// IsWhitelisted 判断域名（含父域匹配）是否在启用白名单内，供出链跟进过滤。
func (s *Store) IsWhitelisted(ctx context.Context, domain string) (bool, error) {
	var n int
	// 第三条子句处理「白名单域名带显式端口」的情况（自建站 localhost:8080、
	// 测试服务器 127.0.0.1:39481）：按主机名匹配，端口不同的同一主机仍算已收录。
	// 真实站点都不带端口，这条对它们不生效。
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM whitelist WHERE enabled = 1 AND
		 (? = domain OR ? LIKE '%.' || domain OR domain LIKE ? || ':%')`,
		domain, domain, domain).Scan(&n)
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
		`SELECT id, domain, seed_path, article_pattern, category, enabled, crawl_interval, last_crawled_at, last_status, last_error, fail_count, created_at
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
		if err := rows.Scan(&w.ID, &w.Domain, &w.SeedPath, &w.ArticlePattern, &w.Category, &enabled, &w.CrawlInterval,
			&w.LastCrawledAt, &w.LastStatus, &w.LastError, &w.FailCount, &created); err != nil {
			return nil, 0, err
		}
		w.Enabled = enabled == 1
		w.CreatedAt = time.Unix(created, 0)
		out = append(out, w)
	}
	return out, total, rows.Err()
}

// AddWhitelist 管理员直接添加站点进白名单（抓取入口为站点首页）。
func (s *Store) AddWhitelist(ctx context.Context, domain, category string, crawlInterval int) error {
	return s.AddWhitelistWithPath(ctx, domain, category, crawlInterval, "")
}

// AddWhitelistWithPath 同上，但可指定抓取入口路径。
// 平台站首页以导航和站务链接为主，指向文章列表页（如 /pick/、/blogs）才能抓到技术内容。
func (s *Store) AddWhitelistWithPath(ctx context.Context, domain, category string, crawlInterval int, seedPath string) error {
	now := time.Now().Unix()
	if crawlInterval <= 0 {
		crawlInterval = 86400
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO whitelist (domain, category, seed_path, enabled, crawl_interval, created_at, updated_at)
		 VALUES (?, ?, ?, 1, ?, ?, ?)
		 ON CONFLICT(domain) DO UPDATE SET category=excluded.category, seed_path=excluded.seed_path,
		 enabled=1, crawl_interval=excluded.crawl_interval, updated_at=excluded.updated_at`,
		domain, category, NormalizeSeedPath(seedPath), crawlInterval, now, now)
	return err
}

// SetWhitelistSeedPath 修改站点的抓取入口路径（后台可调，无需重编译）。
func (s *Store) SetWhitelistSeedPath(ctx context.Context, id int64, seedPath string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET seed_path = ?, updated_at = ? WHERE id = ?`,
		NormalizeSeedPath(seedPath), time.Now().Unix(), id)
	return err
}

// SetWhitelistArticlePattern 修改站点的文章 URL 正则（空串表示不限制）。
func (s *Store) SetWhitelistArticlePattern(ctx context.Context, id int64, pattern string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET article_pattern = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(pattern), time.Now().Unix(), id)
	return err
}

// SiteArticlePattern 返回站点配置的文章 URL 正则（含父域匹配）；未配置返回空串。
func (s *Store) SiteArticlePattern(ctx context.Context, domain string) (string, error) {
	var p string
	err := s.db.QueryRowContext(ctx,
		`SELECT article_pattern FROM whitelist WHERE enabled = 1 AND (? = domain OR ? LIKE '%.' || domain)
		 ORDER BY LENGTH(domain) DESC LIMIT 1`,
		domain, domain).Scan(&p)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return p, nil
}

// SiteByID 按 id 取一条白名单记录（AI 打标要拿域名 + 分类回写）。
func (s *Store) SiteByID(ctx context.Context, id int64) (WhitelistSite, bool, error) {
	var w WhitelistSite
	var crawl int
	var last sql.NullInt64
	var enabled int
	var created int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, domain, seed_path, article_pattern, category, enabled, crawl_interval,
		        last_crawled_at, last_status, last_error, created_at
		   FROM whitelist WHERE id = ?`, id).
		Scan(&w.ID, &w.Domain, &w.SeedPath, &w.ArticlePattern, &w.Category, &enabled,
			&crawl, &last, &w.LastStatus, &w.LastError, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return WhitelistSite{}, false, nil
	}
	if err != nil {
		return WhitelistSite{}, false, err
	}
	w.Enabled = enabled == 1
	w.CrawlInterval = crawl
	w.CreatedAt = time.Unix(created, 0)
	if last.Valid {
		v := last.Int64
		w.LastCrawledAt = &v
	}
	return w, true, nil
}

// SetWhitelistCategory 写回站点分类（人工改或 AI 判定）。
func (s *Store) SetWhitelistCategory(ctx context.Context, id int64, category string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET category = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(category), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DomainTitles 取该域名下最近的若干条文档标题，作为 AI 判定的上下文。
//
// 只看域名和站点名不够：`example.com` 这种名字什么都说明不了，
// 而它下面的文章标题会直接暴露站点是干什么的。
func (s *Store) DomainTitles(ctx context.Context, domain string, limit int) ([]string, error) {
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT title FROM documents WHERE domain = ? AND title <> '' ORDER BY id DESC LIMIT ?`,
		domain, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
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
// TouchCrawlResult 回写最后抓取结果。
// status=ok 视为成功并清零失败计数；其它状态（no-document/error）失败计数 +1，
// 供 DueSites 做指数退避——失败站点不该按原间隔反复打。
func (s *Store) TouchCrawlResult(ctx context.Context, domain, status, errMsg string) error {
	now := time.Now().Unix()
	failed := 1
	if status == "ok" {
		failed = 0
	}
	// WHERE 与 IsWhitelisted 保持同一套端口宽容语义：白名单域名带显式端口时
	// （自建站 localhost:8080），按主机名回写状态。否则 last_crawled_at 永远
	// 更新不到那一行，DueSites 会把它当「从未抓取」反复重排。
	_, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET last_crawled_at = ?, last_status = ?, last_error = ?,
		   fail_count = CASE WHEN ? = 0 THEN 0 ELSE fail_count + 1 END,
		   updated_at = ?
		 WHERE domain = ? OR domain LIKE ? || ':%'`,
		now, status, errMsg, failed, now, domain, domain)
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

// CreateSubmission 用户提交网站进审核队列，返回记录 id 与查询凭证 ticket。
// 命中黑名单历史则 reject_count+1。
// 注意：/api/submit 的开放由 API 层开关控制（红线：首批 20-50 站验证前不开放）。
func (s *Store) CreateSubmission(ctx context.Context, item ReviewItem) (int64, string, error) {
	black, err := s.IsBlacklisted(ctx, item.Domain)
	if err != nil {
		return 0, "", err
	}
	if black {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE blacklist SET reject_count = reject_count + 1 WHERE domain = ?`,
			item.Domain); err != nil {
			return 0, "", err
		}
		// 保留调用方写入的可达性/robots 预检结果，只补上黑名单历史标记
		item.Precheck = mergeBlacklistFlag(item.Precheck)
	}
	ticket, err := newTicket()
	if err != nil {
		return 0, "", err
	}
	now := time.Now().Unix()
	var precheckAt interface{}
	if item.Precheck != "" {
		precheckAt = now
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO review_queue (url, domain, category, reason, ticket, precheck, precheck_at, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
		item.URL, item.Domain, item.Category, item.Reason, ticket, item.Precheck, precheckAt, now)
	if err != nil {
		return 0, "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, "", err
	}
	return id, ticket, nil
}

// newTicket 生成查询凭证：vx + base32(10 字节随机) 小写，50 bit 熵。
// 不含易混淆字符的处理交给调用方展示，这里只保证不可猜。
func newTicket() (string, error) {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("ticket rand: %w", err)
	}
	return "vx" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)), nil
}

// GetSubmissionByTicket 按查询凭证读取提交记录；不存在返回 sql.ErrNoRows。
// 只用于公开状态查询，不返回预检内部细节。
func (s *Store) GetSubmissionByTicket(ctx context.Context, ticket string) (ReviewItem, error) {
	var r ReviewItem
	var created int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, url, domain, category, ticket, status, created_at, reviewed_at
		 FROM review_queue WHERE ticket = ? AND ticket <> ''`, ticket).
		Scan(&r.ID, &r.URL, &r.Domain, &r.Category, &r.Ticket, &r.Status, &created, &r.ReviewedAt)
	if err != nil {
		return ReviewItem{}, err
	}
	r.CreatedAt = time.Unix(created, 0)
	return r, nil
}

// ---------- 标签误判反馈 ----------

// 反馈结论取值。false_positive = 标签打错了；confirmed = 打得对（用于看规则精确率）。
const (
	VerdictFalsePositive = "false_positive"
	VerdictConfirmed     = "confirmed"
)

// LabelFeedback 一行标签反馈。
type LabelFeedback struct {
	ID        int64     `json:"id"`
	URL       string    `json:"url"`
	Domain    string    `json:"domain"`
	Label     string    `json:"label"`
	Verdict   string    `json:"verdict"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UpsertLabelFeedback 记录一次标签判断。同一 client 对同一 (页面, 标签) 只有一条，
// 重复提交即改判——用户点错了不应该在后台刷出两条相反记录。
func (s *Store) UpsertLabelFeedback(ctx context.Context, fb LabelFeedback, client string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO label_feedback (url, domain, label, verdict, note, client, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(url, label, client) DO UPDATE SET verdict=excluded.verdict,
		   note=excluded.note, domain=excluded.domain, updated_at=excluded.updated_at`,
		fb.URL, fb.Domain, fb.Label, fb.Verdict, fb.Note, client, now, now)
	return err
}

// ListLabelFeedback 分页列出反馈。kw 匹配页面地址/域名/备注，verdict 为空表示全部。
func (s *Store) ListLabelFeedback(ctx context.Context, kw, verdict string, limit, offset int) ([]LabelFeedback, int, error) {
	where := `WHERE (? = '' OR verdict = ?)
	          AND (? = '' OR url LIKE '%' || ? || '%' OR domain LIKE '%' || ? || '%' OR note LIKE '%' || ? || '%')`
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM label_feedback `+where, verdict, verdict, kw, kw, kw, kw).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, url, domain, label, verdict, note, created_at, updated_at
		 FROM label_feedback `+where+` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`,
		verdict, verdict, kw, kw, kw, kw, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]LabelFeedback, 0, limit)
	for rows.Next() {
		var fb LabelFeedback
		var created, updated int64
		if err := rows.Scan(&fb.ID, &fb.URL, &fb.Domain, &fb.Label, &fb.Verdict, &fb.Note, &created, &updated); err != nil {
			return nil, 0, err
		}
		fb.CreatedAt = time.Unix(created, 0)
		fb.UpdatedAt = time.Unix(updated, 0)
		out = append(out, fb)
	}
	return out, total, rows.Err()
}

// LabelFalsePositiveCounts 按标签统计「误判」条数，用于判断哪类规则该收紧。
// 只统计 false_positive：confirmed 说明规则有效，不影响调参决策。
func (s *Store) LabelFalsePositiveCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT label, COUNT(1) FROM label_feedback WHERE verdict = ? GROUP BY label`,
		VerdictFalsePositive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var label string
		var n int
		if err := rows.Scan(&label, &n); err != nil {
			return nil, err
		}
		out[label] = n
	}
	return out, rows.Err()
}

// DeleteLabelFeedback 批量删除反馈（后台清理误提交/测试数据）。
func (s *Store) DeleteLabelFeedback(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM label_feedback WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
		 simhash, shingle_hash, published_at, fetched_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(url) DO UPDATE SET
		 title=excluded.title, description=excluded.description, labels=excluded.labels,
		 label_reasons=excluded.label_reasons, word_count=excluded.word_count,
		 simhash=excluded.simhash, shingle_hash=excluded.shingle_hash,
		 published_at=excluded.published_at,
		 fetched_at=excluded.fetched_at, updated_at=excluded.updated_at`,
		doc.URL, doc.Domain, doc.Title, doc.Description, doc.Labels, doc.LabelReasons,
		doc.WordCount, int64(doc.Simhash), int64(doc.ShingleHash), doc.PublishedAt, doc.FetchedAt, now, now)
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

// ---------- 运行期开关（settings） ----------

// SettingSnapshotEnabled 是否在抓取时保存原文快照。
// 放在 store 而不是 api：爬虫要读它，而 api 依赖 crawler，反向 import 会成环。
const SettingSnapshotEnabled = "snapshot.enabled"

// Setting 读取一个配置项。key 不存在时返回 def 且 err 为 nil——
// 调用方关心的是「开关是否打开」，不该为「从没写过」额外写一段 sql.ErrNoRows 分支。
func (s *Store) Setting(ctx context.Context, key, def string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	return v, nil
}

// UsageRow 一天的用量计数（后台「每日用量」页与 /admin/usage 共用）。
type UsageRow struct {
	Day string `json:"day"`
	// Searches 检索请求总数；Succeeded 其中命中至少一条的次数；API 其中来自
	// 非网页端（带 X-Voidxo-Client: web 以外的调用方）的请求数。
	Searches  int64 `json:"searches"`
	Succeeded int64 `json:"succeeded"`
	API       int64 `json:"api"`
	Hits      int64 `json:"hits"`
	Crawls    int64  `json:"crawls"`
	Indexed   int64  `json:"indexed"`
	Summaries int64  `json:"summaries"`
}

// AddUsage 累加某天的用量。
//
// 用 UPSERT 而不是「先读再写」：并发抓取与前台检索会在同一秒内累加，
// 读-改-写会丢计数（这里没有事务保护整个读改写过程）。
func (s *Store) AddUsage(ctx context.Context, r UsageRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_daily (day, searches, succeeded, api, hits, crawls, indexed, summaries)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(day) DO UPDATE SET
			searches  = searches  + excluded.searches,
			succeeded = succeeded + excluded.succeeded,
			api       = api       + excluded.api,
			hits      = hits      + excluded.hits,
			crawls    = crawls    + excluded.crawls,
			indexed   = indexed   + excluded.indexed,
			summaries = summaries + excluded.summaries`,
		r.Day, r.Searches, r.Succeeded, r.API, r.Hits, r.Crawls, r.Indexed, r.Summaries)
	return err
}

// ListUsage 返回 sinceDay（含）之后的每日用量，按日期升序。
func (s *Store) ListUsage(ctx context.Context, sinceDay string) ([]UsageRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT day, searches, succeeded, api, hits, crawls, indexed, summaries
		FROM usage_daily WHERE day >= ? ORDER BY day`, sinceDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.Day, &r.Searches, &r.Succeeded, &r.API, &r.Hits,
			&r.Crawls, &r.Indexed, &r.Summaries); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DimRow 一个维度取值及其累计次数（如某个来源域名、某个国家）。
type DimRow struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// AddUsageDims 累加某天某个维度的计数。整批走一个事务：
// 一次 flush 可能带上几十个来源域名，逐条提交会让 SQLite 频繁 fsync。
func (s *Store) AddUsageDims(ctx context.Context, day, kind string, counts map[string]int64) error {
	if len(counts) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for name, n := range counts {
		if n <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_dim (day, kind, name, count) VALUES (?, ?, ?, ?)
			ON CONFLICT(day, kind, name) DO UPDATE SET count = count + excluded.count`,
			day, kind, name, n); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TopUsageDims 返回某个维度在时间窗内的前 limit 名，按次数降序。
func (s *Store) TopUsageDims(ctx context.Context, sinceDay, kind string, limit int) ([]DimRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, SUM(count) AS c FROM usage_dim
		WHERE kind = ? AND day >= ?
		GROUP BY name ORDER BY c DESC, name LIMIT ?`, kind, sinceDay, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DimRow, 0, limit)
	for rows.Next() {
		var r DimRow
		if err := rows.Scan(&r.Name, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetSetting 读单个配置项，不存在时返回空串（不返回 ErrNoRows：
// 配置项天然允许「没配过」，让调用方到处判错没意义）。
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting 写入/更新一个配置项（upsert）。
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().Unix())
	return err
}

// Settings 返回全部配置项（含 updated_at），供后台一次性展示。
func (s *Store) Settings(ctx context.Context) (map[string]SettingRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, updated_at FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]SettingRow)
	for rows.Next() {
		var r SettingRow
		if err := rows.Scan(&r.Key, &r.Value, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out[r.Key] = r
	}
	return out, rows.Err()
}

// SettingRow 一行配置（后台展示用）。
type SettingRow struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt int64  `json:"updated_at"`
}

// UpdateLabelFeedback 后台修正一条反馈的标签或结论。
//
// 用途：用户点错标签时管理员直接改对，而不是让它污染误判率统计。
func (s *Store) UpdateLabelFeedback(ctx context.Context, id int64, label, verdict string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE label_feedback SET label = ?, verdict = ?, updated_at = ? WHERE id = ?`,
		label, verdict, time.Now(), id)
	return err
}

// ---- 维基百科摘要缓存 ----

// WikiEntry 一个词的维基百科解释。
type WikiEntry struct {
	Q          string `json:"q"`
	Title      string `json:"title"`
	Extract    string `json:"extract"`
	URL        string `json:"url"`
	ResolvedAt int64  `json:"-"` // 缓存写入时间（Unix 秒）
}

// WikiCacheGet 读缓存（未命中返回零值 + sql.ErrNoRows 转换后的错误）。
func (s *Store) WikiCacheGet(ctx context.Context, q string) (WikiEntry, error) {
	var e WikiEntry
	var ts int64
	err := s.db.QueryRowContext(ctx,
		`SELECT q, title, extract, url, ts FROM wiki_cache WHERE q = ?`, q).
		Scan(&e.Q, &e.Title, &e.Extract, &e.URL, &ts)
	if err != nil {
		return WikiEntry{}, err
	}
	e.ResolvedAt = ts
	return e, nil
}

// WikiCachePut 写缓存。
func (s *Store) WikiCachePut(ctx context.Context, q string, e WikiEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO wiki_cache (q, title, extract, url, ts) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(q) DO UPDATE SET
			title = excluded.title, extract = excluded.extract,
			url = excluded.url, ts = excluded.ts`,
		q, e.Title, e.Extract, e.URL, time.Now().Unix())
	return err
}
