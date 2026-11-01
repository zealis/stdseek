// Package store 提供 SQLite（WAL 模式）元数据层，是白名单/黑名单/审核队列/文档的单一事实源。
// 对应规划书「存储与索引」节：元数据全部落 SQLite，零外部依赖。
package store

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zealis/stdseek/internal/sitecat"
	"github.com/zealis/stdseek/internal/urlkey"

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
	// LabelsSource 标签是谁定的：'' = 规则引擎，'manual' = 管理员按用户反馈改过。
	// manual 的标签在重新抓取时不会被规则结果覆盖——否则改完过一轮就悄悄变回去。
	LabelsSource string `json:"labels_source"`
}

// LabelSourceManual 人工改过的标签，重新抓取时不覆盖。
const LabelSourceManual = "manual"

// WhitelistSite 白名单站点，爬虫种子的唯一来源。
type WhitelistSite struct {
	ID       int64  `json:"id"`
	Domain   string `json:"domain"`
	Category string `json:"category"` // official/opensource/blog/platform/media，供 no个人/no平台 标签复用
	// Labels 站点级标签（AI 或人工按整站判的，见 internal/labels）：
	// 写索引时会并进每篇文档的页级标签，所以用户排除「UGC 平台」时整站都不出现。
	Labels []string `json:"labels"`
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

	// ---- AI 站点分类相关（见 internal/api/ai.go）----
	//
	// CategorySource 分类是谁定的：空/seed 是提交时填的默认值，manual 是管理员手改，
	// ai 是模型判的。自动重判跳过 manual。
	CategorySource string `json:"category_source"`
	// AIClassifiedAt 上一次 AI 判定的时间（Unix 秒，0 = 从未判过）。
	AIClassifiedAt int64 `json:"ai_classified_at"`
	// AIClassifiedDocs 判定当时该站的文档数，用来算下一道增量门槛（30 → 300 → +3000）。
	AIClassifiedDocs int `json:"ai_classified_docs"`
	// SiteDesc 站点首页的 meta description/og:description（AI 判定输入，后台也展示）。
	SiteDesc string `json:"site_desc"`
	// SiteName 爬虫从首页提取的站点名称（og:site_name / title）。
	SiteName string `json:"site_name"`
	// DocCount 该站当前入库文档数（列表页子查询算出，只读）。
	DocCount int `json:"doc_count"`
}

// 分类来源常量。空串按 seed 处理（老数据没有这一列）。
const (
	CategorySourceSeed   = "seed"   // 提交/默认值
	CategorySourceManual = "manual" // 管理员手改，自动重判不覆盖
	CategorySourceAI     = "ai"     // 模型判定
)

// EffectiveCategorySource 空串归一成 seed，供判断与展示使用。
func EffectiveCategorySource(s string) string {
	if s == "" {
		return CategorySourceSeed
	}
	return s
}

// 抓取异常类别（后台「异常名单」的三类）。三种问题的修复动作完全不同：
// 站点下线要找站方、robots 变了要换入口或移出白名单、结构变了要调正文选择器。
const (
	// CrawlIssueUnreachable 无法访问：DNS/连接失败、超时、HTTP 4xx/5xx。
	CrawlIssueUnreachable = "unreachable"
	// CrawlIssueRobots robots.txt 不再允许抓取（站点改过爬虫规则）。
	CrawlIssueRobots = "robots"
	// CrawlIssueStructure 抓到了页面但没有可索引正文（结构变化/解析失败）。
	CrawlIssueStructure = "structure"
)

// CrawlIssue 异常名单的一条记录：一个域名一行，只保留最近一次异常。
//
// 为什么不是失败流水：后台要回答的是「现在哪些站坏了、坏成什么样」，
// 流水会让人在几百条历史里找现状。次数与首次发现时间足够判断严重程度。
type CrawlIssue struct {
	ID          int64     `json:"id"`
	Domain      string    `json:"domain"`
	Kind        string    `json:"kind"`
	Detail      string    `json:"detail"`
	Occurrences int       `json:"occurrences"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
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
// 数据目录下的库文件名。
//
// 2026-11-01 品牌从 voidxo 改成 StdSeek 时换成了 stdseek.db。**旧名必须继续能打开**：
// 升级后直接找不到库，用户看到的是「数据没了」。所以只改默认值，不做自动重命名——
// 万一下次回滚到旧二进制，它还得能找到原来的文件。
const (
	DefaultDBName = "stdseek.db"
	LegacyDBName  = "voidxo.db"
)

// dbPath 返回应使用的库文件：新名优先，只有新名不存在而旧名存在时用旧的。
func dbPath(dataDir string) string {
	p := filepath.Join(dataDir, DefaultDBName)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		legacy := filepath.Join(dataDir, LegacyDBName)
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
	}
	return p
}

// OpenDefault 打开数据目录下的库（取名规则见 dbPath）。
func OpenDefault(dataDir string) (*Store, error) { return Open(dbPath(dataDir)) }

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

// ephemeralTables 备份时要清掉的表：会话令牌、验证码、缓存与统计。
//
// 不备份它们的理由：sessions / email_tokens 是活凭据，放进对象存储等于把
// 「现在能登录的令牌」抄一份到桶里；缓存与用量统计可以从日志或重新抓取重建。
// 账号、通行密钥（公钥）、白名单、反馈、设置这些**必须**留。
var ephemeralTables = []string{
	"sessions", "email_tokens", "geo_cache", "wiki_cache",
	"usage_dim", "usage_daily", "overview_cache",
}

// BackupTo 用 VACUUM INTO 生成一份一致性快照。
//
// 为什么不用「拷 .db 文件」：库是 WAL 模式且随时在写，直接拷会得到半个事务。
// VACUUM INTO 是 SQLite 自己保证一致性的导出，且顺带压实（去碎片）。
//
// 导出后打开副本清掉瞬时表——注意是清副本，不是清线上库。
func (s *Store) BackupTo(ctx context.Context, path string) (int64, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return 0, fmt.Errorf("vacuum into %s: %w", path, err)
	}
	// 清副本：单独开连接，不碰线上库的会话。
	copyDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return 0, err
	}
	for _, t := range ephemeralTables {
		if _, err := copyDB.ExecContext(ctx, "DELETE FROM "+t); err != nil {
			copyDB.Close()
			return 0, fmt.Errorf("clean %s: %w", t, err)
		}
	}
	// 压实一次，把 DELETE 留下的空闲页还回去（否则备份文件里仍带着旧页）。
	if _, err := copyDB.ExecContext(ctx, `VACUUM`); err != nil {
		copyDB.Close()
		return 0, err
	}
	if err := copyDB.Close(); err != nil {
		return 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

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
		// 白名单建表语句里没有 whois 两列——旧库靠下面的 ALTER 补，新库也是先建再补，
		// 与 fail_count 等列同一套做法（SQLite 加列不支持 IF NOT EXISTS）。
		// 抓取异常名单：只记「当前坏了」的站点，恢复正常就删行。
		// 不留历史是有意的——白名单只有几十到几百个站，名单要能一眼看完；
		// 恢复记录在 whitelist.last_status / last_crawled_at 里，本来就查得到。
		`CREATE TABLE IF NOT EXISTS crawl_issues (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			domain        TEXT NOT NULL UNIQUE,
			kind          TEXT NOT NULL,
			detail        TEXT NOT NULL DEFAULT '',
			occurrences   INTEGER NOT NULL DEFAULT 0,
			first_seen_at INTEGER NOT NULL,
			last_seen_at  INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_crawl_issues_kind ON crawl_issues(kind, last_seen_at)`,
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
			kind INTEGER NOT NULL DEFAULT 0,
			category TEXT NOT NULL DEFAULT '',
			suggest TEXT NOT NULL DEFAULT '',
			no_label INTEGER NOT NULL DEFAULT 0,
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
		// AI 概览的持久缓存：同一问题第二次搜索不再调模型。
		// 放 SQLite 而不是只在内存，是因为「热门问题」的价值要跨进程重启；
		// 搜索关键词不落库，无法预知热门词，只能首次生成后缓存。
		`CREATE TABLE IF NOT EXISTS overview_cache (
			query_key TEXT PRIMARY KEY,
			answer    TEXT NOT NULL,
			sources   TEXT NOT NULL DEFAULT '[]',
			provider  TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_overview_expires ON overview_cache(expires_at)`,
		// AI 概览的按 IP 每日配额。概览是唯一会真金白银调模型的公开接口，
		// 缓存挡得住「同一个问题问两遍」，挡不住「每次换一种问法」。
		// day 用本地日期的文本（2026-10-30），字典序即时间序，清理直接用 < 比较。
		`CREATE TABLE IF NOT EXISTS overview_quota (
			day        TEXT NOT NULL,
			ip         TEXT NOT NULL,
			n          INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (day, ip)
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
		// whois：域名注册时间（排序的信任度信号）。created_at 是域名注册日（unix 秒），
		// checked_at 是上次查询时间——whois 数据几乎不变，90 天才复查一次。
		`ALTER TABLE whitelist ADD COLUMN whois_created_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE whitelist ADD COLUMN whois_checked_at INTEGER NOT NULL DEFAULT 0`,
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
		// labels：AI（或人工）给**站点级**打的标签，逗号分隔。
		// 页级标签由 processor 在索引期按内容判定；站点级的是「这站整体如此」
		// （整个站都是 UGC 平台 / 个人站），两者在写索引时合并。
		`ALTER TABLE whitelist ADD COLUMN labels TEXT NOT NULL DEFAULT ''`,
		// 内容去重：dup_of 指向正本 URL（空表示自己是正本），dup_count 记录被折叠的数量。
		// 只折叠不删除：副本的 URL 仍然要能查（用户可能直接搜到它），只是不入索引。
		`ALTER TABLE documents ADD COLUMN dup_of TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE documents ADD COLUMN dup_count INTEGER NOT NULL DEFAULT 0`,
		// category_source：分类是谁定的。空/'seed' = 提交时填的默认值，'manual' = 管理员手改，
		// 'ai' = AI 判定。自动重判只碰前两种——手改过的分类不能被半夜的定时任务悄悄改掉。
		`ALTER TABLE whitelist ADD COLUMN category_source TEXT NOT NULL DEFAULT ''`,
		// ai_classified_at / ai_classified_docs：上一次 AI 判定的时间与当时的文档数。
		// 后者是「增量触发」的游标：30 → 300 → 之后每 +3000，靠它算下一道门槛。
		`ALTER TABLE whitelist ADD COLUMN ai_classified_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE whitelist ADD COLUMN ai_classified_docs INTEGER NOT NULL DEFAULT 0`,
		// site_desc：站点首页的 meta description/og:description，AI 判定的主要输入之一。
		// 单独存一列而不是每次去翻正文：首页描述是「网站自己怎么介绍自己」，信号最干净。
		`ALTER TABLE whitelist ADD COLUMN site_desc TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_documents_dup_of ON documents(dup_of)`,
		// 反馈分行类型：kind=0 是标签反馈（label 必填），kind=1 是分类反馈（label 恒为空）。
		// 合一张表而不是另开一张，是因为后台要在同一个列表里看、用同一套改判与删除；
		// 前台也必须一次请求提交完——人机验证的 token 是一次性的，拆两个请求第二条必定失败。
		`ALTER TABLE label_feedback ADD COLUMN kind INTEGER NOT NULL DEFAULT 0`,
		// category 是反馈当时的站点分类，suggest 是用户认为正确的分类（可以不说）。
		// 记「当时」而不是只存建议：站点分类会被 AI 重判，事后再看就分不清在说哪个。
		`ALTER TABLE label_feedback ADD COLUMN category TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE label_feedback ADD COLUMN suggest TEXT NOT NULL DEFAULT ''`,
		// no_label：这条标签反馈成立，而且该页面**不该有任何标签**（没有别的标签可换）。
		// 单独一列而不是把 label 置空：label 是「用户报的是哪个标签」，
		// 是误判率统计的归属；抹掉它等于这个数据点从 ai_suspect 的计数里消失。
		`ALTER TABLE label_feedback ADD COLUMN no_label INTEGER NOT NULL DEFAULT 0`,
		// labels_source：页级标签是不是人工改过。'manual' 时 UpsertDocument 不再用
		// 规则引擎的结果覆盖——否则管理员按反馈改完，下次抓取一夜回到原样。
		`ALTER TABLE documents ADD COLUMN labels_source TEXT NOT NULL DEFAULT ''`,
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
		 simhash, published_at, fetched_at, created_at, labels_source FROM documents WHERE url = ?`, url).
		Scan(&d.ID, &d.URL, &d.Domain, &d.Title, &d.Description, &d.Labels, &d.LabelReasons,
			&d.WordCount, &simhash, &d.PublishedAt, &d.FetchedAt, &created, &d.LabelsSource)
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

// LabelsByURLs 批量返回页面当前的页级标签。
// 后台「用户反馈」的修改弹窗要拿它做初值：改链接得先看到这一页现在挂着什么标签，
// 否则管理员只能凭反馈里那一个标签猜全貌。
func (s *Store) LabelsByURLs(ctx context.Context, urls []string) (map[string][]string, error) {
	out := make(map[string][]string, len(urls))
	if len(urls) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(urls)), ",")
	args := make([]interface{}, len(urls))
	for i, u := range urls {
		args[i] = u
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT url, labels FROM documents WHERE url IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u, raw string
		if err := rows.Scan(&u, &raw); err != nil {
			return nil, err
		}
		var ls []string
		if raw != "" {
			_ = json.Unmarshal([]byte(raw), &ls) // 历史数据损坏时降级为「无标签」
		}
		out[u] = ls
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

// ExportDoc 结构化导出的一行：文档元数据，不含正文。
//
// 正文不进这里有两个理由：它在原文快照里已经有一份（重复一遍文件大一个数量级）；
// 下游拿元数据是为了重建索引/做统计/灾后核对，真正要读正文时按 URL 回快照取。
type ExportDoc struct {
	URL          string   `json:"url"`
	Domain       string   `json:"domain"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Labels       []string `json:"labels"`
	LabelsSource string   `json:"labels_source,omitempty"`
	WordCount    int      `json:"word_count"`
	PublishedAt  *int64   `json:"published_at,omitempty"`
	FetchedAt    int64    `json:"fetched_at"`
	Simhash      uint64   `json:"simhash"`
	ShingleHash  uint64   `json:"shingle_hash"`
	DupOf        string   `json:"dup_of,omitempty"`
	DupCount     int      `json:"dup_count,omitempty"`
}

// ExportDocuments 逐行回调全部文档元数据，返回条数。
//
// 用回调而不是返回切片：20 万篇一次性装进内存是一百多 MB，
// 而且导出本身要边读边压缩，没必要先在内存里攒一遍。
func (s *Store) ExportDocuments(ctx context.Context, fn func(ExportDoc) error) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT url, domain, title, description, labels, labels_source, word_count,
		        published_at, fetched_at, simhash, shingle_hash, dup_of, dup_count
		 FROM documents ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var d ExportDoc
		var labels string
		var simhash, shingle int64
		if err := rows.Scan(&d.URL, &d.Domain, &d.Title, &d.Description, &labels,
			&d.LabelsSource, &d.WordCount, &d.PublishedAt, &d.FetchedAt, &simhash, &shingle,
			&d.DupOf, &d.DupCount); err != nil {
			return n, err
		}
		d.Simhash = uint64(simhash)
		d.ShingleHash = uint64(shingle)
		if labels != "" {
			// 解析失败留空标签即可：导出不能因为一行脏数据整批失败。
			_ = json.Unmarshal([]byte(labels), &d.Labels)
		}
		if d.Labels == nil {
			d.Labels = []string{}
		}
		if err := fn(d); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// ---------- AI 概览配额 ----------

// OverviewQuotaTake 给「某天某个 IP」计数加一并返回加完的值。
//
// 配额本身不精确到毫秒级一致：并发的两次请求可能都读到 20 再各自加一。
// 它是成本闸门，不是安全边界——多放过一次调用的代价远小于为它加分布式锁。
func (s *Store) OverviewQuotaTake(ctx context.Context, day, ip string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO overview_quota (day, ip, n, updated_at) VALUES (?, ?, 1, ?)
		 ON CONFLICT(day, ip) DO UPDATE SET n = n + 1, updated_at = excluded.updated_at`,
		day, ip, time.Now().Unix()); err != nil {
		return 0, err
	}
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT n FROM overview_quota WHERE day = ? AND ip = ?`, day, ip).Scan(&n); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// OverviewQuotaRefund 退回一次（调用失败的那次不该算在用户头上）。
// 加过再退，所以退到 0 以下时直接不动。
func (s *Store) OverviewQuotaRefund(ctx context.Context, day, ip string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE overview_quota SET n = n - 1 WHERE day = ? AND ip = ? AND n > 0`, day, ip)
	return err
}

// OverviewQuotaPeek 只读当前计数（后台展示用，不消耗额度）。
func (s *Store) OverviewQuotaPeek(ctx context.Context, day, ip string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT n FROM overview_quota WHERE day = ? AND ip = ?`, day, ip).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// PruneOverviewQuota 删掉 before 之前（不含）的日期。传今天的日期就只留今天。
func (s *Store) PruneOverviewQuota(ctx context.Context, before string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM overview_quota WHERE day < ?`, before)
	return err
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
	return s.listWhitelist(ctx, kw, "", limit, offset)
}

// ListWhitelistIn 按分类筛选（后台「按分类筛选」用）。cat 为空表示不限。
func (s *Store) ListWhitelistIn(ctx context.Context, kw, cat string, limit, offset int) ([]WhitelistSite, int, error) {
	return s.listWhitelist(ctx, kw, cat, limit, offset)
}

func (s *Store) listWhitelist(ctx context.Context, kw, cat string, limit, offset int) ([]WhitelistSite, int, error) {
	where := `WHERE (? = '' OR domain LIKE '%' || ? || '%' OR category LIKE '%' || ? || '%')
	          AND (? = '' OR category = ?)`
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM whitelist `+where, kw, kw, kw, cat, cat).Scan(&total); err != nil {
		return nil, 0, err
	}
	// doc_count 用子查询：页面要显示「这个站有几篇链接」，而白名单最多几百行，
	// 每行一次 COUNT 走 idx_documents_domain 是毫秒级，不值得再维护一张计数表。
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, domain, seed_path, article_pattern, category, enabled, crawl_interval, last_crawled_at,
		        last_status, last_error, fail_count, created_at, labels,
		        category_source, ai_classified_at, ai_classified_docs, site_desc,
		        (SELECT COUNT(1) FROM documents d WHERE d.domain = w.domain AND d.dup_of = '')
		 FROM whitelist w `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		kw, kw, kw, cat, cat, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []WhitelistSite
	for rows.Next() {
		var w WhitelistSite
		var enabled int
		var created int64
		var rawLabels string
		if err := rows.Scan(&w.ID, &w.Domain, &w.SeedPath, &w.ArticlePattern, &w.Category, &enabled, &w.CrawlInterval,
			&w.LastCrawledAt, &w.LastStatus, &w.LastError, &w.FailCount, &created, &rawLabels,
			&w.CategorySource, &w.AIClassifiedAt, &w.AIClassifiedDocs, &w.SiteDesc, &w.DocCount); err != nil {
			return nil, 0, err
		}
		w.Enabled = enabled == 1
		w.CreatedAt = time.Unix(created, 0)
		w.Labels = splitLabels(rawLabels)
		out = append(out, w)
	}
	return out, total, rows.Err()
}

// WhitelistCategoryCounts 各分类的站点数（筛选下拉的括号数字用）。
func (s *Store) WhitelistCategoryCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT category, COUNT(1) FROM whitelist GROUP BY category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// AddWhitelist 管理员直接添加站点进白名单（抓取入口为站点首页）。
func (s *Store) AddWhitelist(ctx context.Context, domain, category string, crawlInterval int) error {
	return s.AddWhitelistWithPath(ctx, domain, category, crawlInterval, "")
}

// AddWhitelistWithPath 同上，但可指定抓取入口路径；crawlInterval 传 0 表示「按分类默认」。
//
// 同一域名重复提交走 UPSERT 而不是报唯一键冲突：用户记不住自己提过没有，
// 而重复提交的意图很明确——「按这次填的信息更新它」。所以分类与抓取间隔会被覆盖，
// 但这次没填的字段要留旧值：seed_path 空着不能把之前配好的列表页入口抹掉，
// 站点名/描述是爬虫抓出来的，更不该被一次提交清空。
// 分类来源一并回到 seed：新提交的分类是人工表态，不该继承上次 AI 的判定。
func (s *Store) AddWhitelistWithPath(ctx context.Context, domain, category string, crawlInterval int, seedPath string) error {
	return upsertWhitelist(ctx, s.db, domain, category, crawlInterval, seedPath, time.Now().Unix())
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
	var rawLabels string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, domain, seed_path, article_pattern, category, enabled, crawl_interval,
		        last_crawled_at, last_status, last_error, created_at, labels,
		        category_source, ai_classified_at, ai_classified_docs, site_desc, site_name
		   FROM whitelist WHERE id = ?`, id).
		Scan(&w.ID, &w.Domain, &w.SeedPath, &w.ArticlePattern, &w.Category, &enabled,
			&crawl, &last, &w.LastStatus, &w.LastError, &created, &rawLabels,
			&w.CategorySource, &w.AIClassifiedAt, &w.AIClassifiedDocs, &w.SiteDesc, &w.SiteName)
	if errors.Is(err, sql.ErrNoRows) {
		return WhitelistSite{}, false, nil
	}
	if err != nil {
		return WhitelistSite{}, false, err
	}
	w.Enabled = enabled == 1
	w.CrawlInterval = crawl
	w.CreatedAt = time.Unix(created, 0)
	w.Labels = splitLabels(rawLabels)
	if last.Valid {
		v := last.Int64
		w.LastCrawledAt = &v
	}
	return w, true, nil
}

// SiteLabels 取某域名对应的站点级标签（与分类同一套最长后缀匹配）。
func (s *Store) SiteLabels(ctx context.Context, domain string) ([]string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT labels FROM whitelist WHERE enabled = 1 AND (? = domain OR ? LIKE '%.' || domain)
		 ORDER BY LENGTH(domain) DESC LIMIT 1`,
		domain, domain).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return splitLabels(raw), nil
}

// AllSiteLabels 全量加载站点标签（reindex 用，避免每篇文档一次查询）。
func (s *Store) AllSiteLabels(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT domain, labels FROM whitelist WHERE enabled = 1 AND labels <> ''`)
	if err != nil {
		return nil, fmt.Errorf("site labels: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var d, raw string
		if err := rows.Scan(&d, &raw); err != nil {
			return nil, err
		}
		out[d] = splitLabels(raw)
	}
	return out, rows.Err()
}

// SetWhitelistLabels 写回站点级标签（AI 判定或人工修改）。
func (s *Store) SetWhitelistLabels(ctx context.Context, id int64, list []string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET labels = ?, updated_at = ? WHERE id = ?`,
		strings.Join(list, ","), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ApplySiteClassify 写回 AI 判定结果（分类 + 站点级标签 + 判定游标）。
//
// 一次 UPDATE 写全，避免「分类写进去了、游标没写」这种半截状态——
// 游标一旦漏写，下一轮会把同一个站反复判一遍，白烧模型调用。
func (s *Store) ApplySiteClassify(ctx context.Context, id int64, category string, lbls []string, docs int) error {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET category = ?, labels = ?, category_source = ?, ai_classified_at = ?,
		        ai_classified_docs = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(category), strings.Join(lbls, ","), CategorySourceAI, now, docs, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdateSiteDesc 记录站点首页的 meta description（仅当当前为空时写入，同 site_name）。
func (s *Store) UpdateSiteDesc(ctx context.Context, domain, desc string) error {
	if domain == "" || desc == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET site_desc = ? WHERE domain = ? AND site_desc = ''`, desc, domain)
	return err
}

// DocSample 站点语料抽样的最小单元：URL + 字数。
// AI 判定只需要「有哪几页、大概多长」，不拉正文（正文从快照里按 URL 现取）。
type DocSample struct {
	URL       string
	WordCount int
}

// DomainDocSample 返回某站入库文档的抽样（按篇幅倒序）。
//
// 按篇幅而不是按入库时间：最新入库的往往是导航页/站务页（真实数据里
// k8s 的 Community、PostgreSQL 的 Press 都排在前面），它们的标题对判断站点性质毫无用处。
// 长文更可能是该站的代表性内容。
func (s *Store) DomainDocSample(ctx context.Context, domain string, limit int) ([]DocSample, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT url, word_count FROM documents
		  WHERE domain = ? AND dup_of = '' ORDER BY word_count DESC, id DESC LIMIT ?`,
		domain, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocSample
	for rows.Next() {
		var d DocSample
		if err := rows.Scan(&d.URL, &d.WordCount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DomainDocCount 某站当前入库文档数（判定门槛用）。
func (s *Store) DomainDocCount(ctx context.Context, domain string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM documents WHERE domain = ? AND dup_of = ''`, domain).Scan(&n)
	return n, err
}

// DomainLabelStats 该站被规则打上「广告较多」「疑似 AI 生成」「追踪器较多」的页面数。
//
// 这是 AI 判定 ad_heavy / ai_suspect / tracker 的硬证据来源：规则看的是完整 HTML
// （广告位 DOM 占比、联盟链接、追踪脚本特征、AI generator meta、套话密度），
// 比模型读 3 段摘录更可靠。
func (s *Store) DomainLabelStats(ctx context.Context, domain string) (ad, ai, tracker int, err error) {
	var a, b, c sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		`SELECT SUM(CASE WHEN labels LIKE '%"ad_heavy"%' THEN 1 ELSE 0 END),
		        SUM(CASE WHEN labels LIKE '%"ai_suspect"%' THEN 1 ELSE 0 END),
		        SUM(CASE WHEN labels LIKE '%"tracker"%' THEN 1 ELSE 0 END)
		   FROM documents WHERE domain = ? AND dup_of = ''`, domain).Scan(&a, &b, &c)
	if err != nil {
		return 0, 0, 0, err
	}
	return int(a.Int64), int(b.Int64), int(c.Int64), nil
}

// DomainsNeedingWhois 返回还没查过（或超过 90 天没复查）且在启用状态的域名。
// whois 数据几乎不变，90 天复查一次只是为了捕捉「域名易主/换注册商」这种极小概率事件。
func (s *Store) DomainsNeedingWhois(ctx context.Context, staleBefore int64, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT domain FROM whitelist
		  WHERE enabled = 1 AND whois_checked_at < ?
		  ORDER BY whois_checked_at ASC, id ASC LIMIT ?`, staleBefore, limit)
	if err != nil {
		return nil, err
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

// SetWhoisCreated 写回域名注册时间。createdAt <= 0 表示「查了但没有有效结果」：
// 也推进 checked_at，否则查不到的域名每轮扫描都会被打一次上游。
func (s *Store) SetWhoisCreated(ctx context.Context, domain string, createdAt int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET whois_created_at = ?, whois_checked_at = ? WHERE domain = ?`,
		createdAt, time.Now().Unix(), domain)
	return err
}

// SetWhoisCheckedAt 只推进复查时间（不写注册时间）。失败重试用：
// 瞬时故障（限流/超时）不该被 90 天的复查间隔锁死。
func (s *Store) SetWhoisCheckedAt(ctx context.Context, domain string, checkedAt int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET whois_checked_at = ? WHERE domain = ?`, checkedAt, domain)
	return err
}

// DomainWhoisCreated 批量返回域名注册时间（unix 秒；0 = 未知，不在白名单的域名不出现）。
func (s *Store) DomainWhoisCreated(ctx context.Context, domains []string) (map[string]int64, error) {
	out := make(map[string]int64, len(domains))
	for _, d := range domains {
		out[d] = 0
	}
	if len(domains) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(domains)), ",")
	args := make([]interface{}, 0, len(domains))
	for _, d := range domains {
		args = append(args, d)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT domain, whois_created_at FROM whitelist WHERE domain IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		var ts int64
		if err := rows.Scan(&d, &ts); err != nil {
			return nil, err
		}
		out[d] = ts
	}
	return out, rows.Err()
}

// ClassifyCandidate 自动判定的候选站点（带文档数，省一次查询）。
type ClassifyCandidate struct {
	ID               int64
	Domain           string
	Category         string
	CategorySource   string
	AIClassifiedDocs int
	Docs             int
}

// ClassifyCandidates 返回「可以交给自动判定」的站点：启用、且分类不是管理员手定的。
//
// manual 跳过是硬规则——自动任务半夜把手改的分类改掉，用户会认为系统在跟他作对。
func (s *Store) ClassifyCandidates(ctx context.Context, domains []string) ([]ClassifyCandidate, error) {
	q := `SELECT w.id, w.domain, w.category, w.category_source, w.ai_classified_docs,
	             (SELECT COUNT(1) FROM documents d WHERE d.domain = w.domain AND d.dup_of = '')
	      FROM whitelist w
	      WHERE w.enabled = 1 AND w.category_source <> ?`
	args := []any{CategorySourceManual}
	if len(domains) > 0 {
		q += ` AND w.domain IN (` + placeholders(len(domains)) + `)`
		for _, d := range domains {
			args = append(args, d)
		}
	}
	q += ` ORDER BY w.id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClassifyCandidate
	for rows.Next() {
		var c ClassifyCandidate
		if err := rows.Scan(&c.ID, &c.Domain, &c.Category, &c.CategorySource, &c.AIClassifiedDocs, &c.Docs); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// placeholders 生成 "?,?,?" 形式的占位符（IN 查询用）。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// splitLabels 把逗号分隔的标签串拆干净（空串、多余逗号都不留空项）。
func splitLabels(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SetWhitelistInterval 改站点抓取间隔（秒）。后台批量动作与「按分类重算」共用。
func (s *Store) SetWhitelistInterval(ctx context.Context, id int64, sec int) error {
	if sec <= 0 {
		return errors.New("抓取间隔必须为正数")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET crawl_interval = ?, updated_at = ? WHERE id = ?`,
		sec, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ResetIntervalsByCategory 按分类重算一批站点的抓取间隔。
//
// 空 ids 表示全表——给「上线新间隔表」这种一次性回填用；
// 正常批量操作都会带 ids。返回值是「实际被改动的站点数」。
func (s *Store) ResetIntervalsByCategory(ctx context.Context, ids []int64) (int, error) {
	q := `SELECT id, category, crawl_interval FROM whitelist`
	args := []any{}
	if len(ids) > 0 {
		q += ` WHERE id IN (` + placeholders(len(ids)) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type row struct {
		id, cur int64
		cat     string
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.cat, &r.cur); err != nil {
			return 0, err
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range list {
		want := sitecat.IntervalFor(r.cat)
		// 已经等于目标值就跳过：一是少写一次库，二是「改动数」要如实反映变化——
		// 用户靠这个数字判断这次操作到底做了什么，写成 24 会让人以为全站都被改了。
		if int(r.cur) == want {
			continue
		}
		if err := s.SetWhitelistInterval(ctx, r.id, want); err != nil {
			continue
		}
		n++
	}
	return n, nil
}

// SetWhitelistCategory 写回站点分类（人工改或 AI 判定）。
func (s *Store) SetWhitelistCategory(ctx context.Context, id int64, category string) error {
	// 手改即「锁定」：自动重判看到 manual 就跳过，否则管理员刚改对的分类
	// 会被下一轮定时任务悄悄改回去（比不做自动判定更糟）。
	res, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET category = ?, category_source = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(category), CategorySourceManual, time.Now().Unix(), id)
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
	if limit <= 0 || limit > 60 {
		limit = 20
	}
	// 按发布时间倒序、缺发布时间退回抓取时间：按 id 倒序取到的是「最近入库的」，
	// 而最近入库的常是导航页/站务页，标题对判断站点性质毫无用处。
	rows, err := s.db.QueryContext(ctx,
		`SELECT title FROM documents WHERE domain = ? AND title <> '' AND dup_of = ''
		  ORDER BY COALESCE(published_at, fetched_at) DESC, id DESC LIMIT ?`,
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

// ---------- 抓取异常名单 ----------

// RecordCrawlIssue 记录（或刷新）一个站点的抓取异常。
//
// 同一域名只留一行：kind/detail 覆盖成最近一次，occurrences 累加。
// 覆盖而不是共存——「这个站现在是什么毛病」才是后台要看的，
// 至于它上个月是超时、这个月是改结构，对谁来修没有区别。
func (s *Store) RecordCrawlIssue(ctx context.Context, domain, kind, detail string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO crawl_issues (domain, kind, detail, occurrences, first_seen_at, last_seen_at)
		 VALUES (?, ?, ?, 1, ?, ?)
		 ON CONFLICT(domain) DO UPDATE SET
		   kind = excluded.kind,
		   detail = excluded.detail,
		   occurrences = crawl_issues.occurrences + 1,
		   last_seen_at = excluded.last_seen_at`,
		domain, kind, detail, now, now)
	return err
}

// ClearCrawlIssue 把站点移出异常名单（抓取恢复正常，或管理员手动忽略）。
//
// 直接删行而不是打「已恢复」标记：名单的价值在于「现在该修哪个站」，
// 一轮抓取能跑几十个站，留着历史只会让默认视图越来越长。
func (s *Store) ClearCrawlIssue(ctx context.Context, domain string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM crawl_issues WHERE domain = ?`, domain)
	return err
}

// ListCrawlIssues 分页列出异常名单（kw 模糊匹配域名与说明）。
func (s *Store) ListCrawlIssues(ctx context.Context, kind, kw string, limit, offset int) ([]CrawlIssue, int, error) {
	var conds []string
	var args []interface{}
	if kind != "" {
		conds = append(conds, `kind = ?`)
		args = append(args, kind)
	}
	if kw != "" {
		conds = append(conds, `(domain LIKE '%' || ? || '%' OR detail LIKE '%' || ? || '%')`)
		args = append(args, kw, kw)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM crawl_issues`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, domain, kind, detail, occurrences, first_seen_at, last_seen_at
		 FROM crawl_issues`+where+` ORDER BY last_seen_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []CrawlIssue
	for rows.Next() {
		var it CrawlIssue
		var first, last int64
		if err := rows.Scan(&it.ID, &it.Domain, &it.Kind, &it.Detail, &it.Occurrences, &first, &last); err != nil {
			return nil, 0, err
		}
		it.FirstSeenAt = time.Unix(first, 0)
		it.LastSeenAt = time.Unix(last, 0)
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// CrawlIssueCounts 异常名单的各类型数量（筛选下拉与侧栏徽标用）。
// 返回的 "all" 是总数，其余键是 CrawlIssue* 常量。
func (s *Store) CrawlIssueCounts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{"all": 0, CrawlIssueUnreachable: 0, CrawlIssueRobots: 0, CrawlIssueStructure: 0}
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, COUNT(1) FROM crawl_issues GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
		out["all"] += n
	}
	return out, rows.Err()
}

// BackfillCrawlIssuesOnce 建表后的一次性回填：把「上次抓取就没抓到东西」的站点直接放进名单。
//
// 为什么要回填：`crawl_issues` 是新增表，上线那一刻是空的——后台看起来像功能没生效，
// 而真实情况是上个月的失败还没被新一轮抓取覆盖。用 whitelist.last_status / last_error /
// fail_count 三个既有字段就能还原出「最后一次抓取发生了什么」。
//
// 只做一次（settings 里留标记）：否则管理员清空名单后一重启又把旧账翻出来。
func (s *Store) BackfillCrawlIssuesOnce(ctx context.Context) (int, error) {
	if v, err := s.Setting(ctx, "crawl_issues.backfilled", ""); err == nil && v == "1" {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO crawl_issues (domain, kind, detail, occurrences, first_seen_at, last_seen_at)
		 SELECT domain,
		        CASE WHEN last_status = 'no-document' THEN ? ELSE ? END,
		        CASE WHEN last_error <> '' THEN last_error ELSE '上一次抓取没有产生可索引内容' END,
		        CASE WHEN fail_count > 0 THEN fail_count ELSE 1 END,
		        COALESCE(last_crawled_at, strftime('%s','now')),
		        COALESCE(last_crawled_at, strftime('%s','now'))
		   FROM whitelist
		  WHERE enabled = 1 AND last_status IN ('error', 'no-document')
		 ON CONFLICT(domain) DO NOTHING`,
		CrawlIssueStructure, CrawlIssueUnreachable)
	if err != nil {
		return 0, err
	}
	if err := s.SetSetting(ctx, "crawl_issues.backfilled", "1"); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// SeedURLsByDomain 返回指定域名的抓取入口 URL（后台单站重抓用）。
// 全量读白名单后在内存里筛：表只有几十到几百行，比拼动态 IN (?,?,…) 简单。
func (s *Store) SeedURLsByDomain(ctx context.Context, domains []string) ([]string, error) {
	if len(domains) == 0 {
		return nil, nil
	}
	want := make(map[string]bool, len(domains))
	for _, d := range domains {
		want[strings.ToLower(strings.TrimSpace(d))] = true
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT domain, seed_path FROM whitelist WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d, p string
		if err := rows.Scan(&d, &p); err != nil {
			return nil, err
		}
		if want[strings.ToLower(d)] {
			out = append(out, "https://"+d+NormalizeSeedPath(p))
		}
	}
	return out, rows.Err()
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

// 反馈结论取值。false_positive = 标签打错了；confirmed = 打得对（用于看规则精确率）；
// false_negative = 该页面本该有这个标签却没有（规则漏报，用于看规则召回率）。
const (
	VerdictFalsePositive = "false_positive"
	VerdictConfirmed     = "confirmed"
	VerdictFalseNegative = "false_negative"
)

// 反馈的行类型。标签反馈指向某个具体标签；分类反馈指向站点分类，没有标签。
const (
	FeedbackKindLabel    = 0
	FeedbackKindCategory = 1
)

// LabelFeedback 一行反馈（标签或分类）。
// Kind=1（分类）时 Label 恒为空——分类不是标签，塞进 Label 会让「按标签统计误判率」
// 把两种东西混在一起数。
type LabelFeedback struct {
	ID      int64  `json:"id"`
	URL     string `json:"url"`
	Domain  string `json:"domain"`
	Label   string `json:"label"`
	Verdict string `json:"verdict"`
	Note    string `json:"note"`
	// Kind 行类型（见 FeedbackKind*）。
	Kind int `json:"kind"`
	// Category 反馈当时的站点分类（仅 Kind=1 有意义）。
	Category string `json:"category"`
	// Suggest 用户认为正确的分类（仅 Kind=1 有意义，可空）。
	Suggest string `json:"suggest"`
	// NoLabel 这条标签反馈成立，且该页面**不该有任何标签**（没有别的标签可换）。
	// 与 Verdict=false_positive 同时出现：不该有标签，那么用户报的那个必然是误判。
	NoLabel   bool      `json:"no_label"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UpsertLabelFeedback 记录一次标签判断。同一 client 对同一 (页面, 标签) 只有一条，
// 重复提交即改判——用户点错了不应该在后台刷出两条相反记录。
func (s *Store) UpsertLabelFeedback(ctx context.Context, fb LabelFeedback, client string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO label_feedback (url, domain, label, verdict, note, client, kind, category, suggest, no_label, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(url, label, client) DO UPDATE SET verdict=excluded.verdict,
		   note=excluded.note, domain=excluded.domain, kind=excluded.kind,
		   category=excluded.category, suggest=excluded.suggest, no_label=excluded.no_label,
		   updated_at=excluded.updated_at`,
		fb.URL, fb.Domain, fb.Label, fb.Verdict, fb.Note, client,
		fb.Kind, fb.Category, fb.Suggest, boolInt(fb.NoLabel), now, now)
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
		`SELECT id, url, domain, label, verdict, note, kind, category, suggest, no_label, created_at, updated_at
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
		var noLabel int
		if err := rows.Scan(&fb.ID, &fb.URL, &fb.Domain, &fb.Label, &fb.Verdict, &fb.Note,
			&fb.Kind, &fb.Category, &fb.Suggest, &noLabel, &created, &updated); err != nil {
			return nil, 0, err
		}
		fb.NoLabel = noLabel != 0
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
		`SELECT label, COUNT(1) FROM label_feedback WHERE verdict = ? AND kind = ? GROUP BY label`,
		VerdictFalsePositive, FeedbackKindLabel)
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
	if err := upsertWhitelist(ctx, tx, domain, category, 0, "", now); err != nil {
		return err
	}
	return tx.Commit()
}

// upsertWhitelist 写入或更新一个白名单站点（AddWhitelist 与审核通过共用同一条语义）。
//
// 为什么审核通过也要走同一条：用户提交时填的分类和「按分类定的抓取间隔」，
// 通过审核后必须落到白名单上，否则前台辛苦选的分类会被 86400 这种写死的默认值覆盖——
// 差异只会出现在「换个入口做同一件事」的地方，最难查。
func upsertWhitelist(ctx context.Context, ex execer, domain, category string, crawlInterval int, seedPath string, now int64) error {
	if crawlInterval <= 0 {
		crawlInterval = sitecat.IntervalFor(category)
	}
	_, err := ex.ExecContext(ctx,
		`INSERT INTO whitelist (domain, category, seed_path, enabled, crawl_interval, category_source, created_at, updated_at)
		 VALUES (?, ?, ?, 1, ?, ?, ?, ?)
		 ON CONFLICT(domain) DO UPDATE SET
		   category=excluded.category, crawl_interval=excluded.crawl_interval,
		   category_source=excluded.category_source, enabled=1,
		   seed_path=CASE WHEN excluded.seed_path <> '/' THEN excluded.seed_path ELSE whitelist.seed_path END,
		   updated_at=excluded.updated_at`,
		domain, category, NormalizeSeedPath(seedPath), crawlInterval, CategorySourceSeed, now, now)
	return err
}

// execer 让 *sql.DB 与 *sql.Tx 都能跑同一条写入语句。
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
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
		 title=excluded.title, description=excluded.description,
		 labels = CASE WHEN documents.labels_source = 'manual' THEN documents.labels ELSE excluded.labels END,
		 label_reasons = CASE WHEN documents.labels_source = 'manual' THEN documents.label_reasons ELSE excluded.label_reasons END,
		 word_count=excluded.word_count,
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

// SetDocLabels 人工改一页的标签，并锁定（labels_source='manual'）。
//
// 锁定是必需的：不锁的话下次抓取规则重算一遍就把管理员的修正抹掉，
// 而用户反馈的问题正是「规则判错了」——修完又被同一条规则改回去，等于没修。
func (s *Store) SetDocLabels(ctx context.Context, url string, labelsJSON, reasonsJSON string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE documents SET labels = ?, label_reasons = ?, labels_source = ?, updated_at = ?
		 WHERE url = ?`, labelsJSON, reasonsJSON, LabelSourceManual, time.Now().Unix(), url)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ManualDocLabels 读出人工锁定的标签；没锁过返回 ok=false。
// 抓取时每页查一次（唯一索引命中），拿到就顶掉规则结果。
func (s *Store) ManualDocLabels(ctx context.Context, url string) (labelsJSON, reasonsJSON string, ok bool) {
	var source string
	err := s.db.QueryRowContext(ctx,
		`SELECT labels, label_reasons, labels_source FROM documents WHERE url = ?`, url).
		Scan(&labelsJSON, &reasonsJSON, &source)
	if err != nil || source != LabelSourceManual {
		return "", "", false
	}
	return labelsJSON, reasonsJSON, true
}

// SetDomainCategory 按域名改分类并锁定（AI 自动判定不再覆盖）。
func (s *Store) SetDomainCategory(ctx context.Context, domain, category string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE whitelist SET category = ?, category_source = ?, updated_at = ? WHERE domain = ?`,
		strings.TrimSpace(category), CategorySourceManual, time.Now().Unix(), domain)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
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
	Crawls    int64 `json:"crawls"`
	Indexed   int64 `json:"indexed"`
	Summaries int64 `json:"summaries"`
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

// UsageTotals 累计口径（与按天切分的 ListUsage 互补）。
//
// 后台统计卡要「今日 + 累计」并排显示，累计值不能靠把 30 天的行加起来——
// 卡片上的「累计」是全生命周期的。这几条都是全表聚合，表在千行以内，一次查询足够。
type UsageTotals struct {
	Searches  int64 `json:"searches"`
	Crawls    int64 `json:"crawls"`
	Indexed   int64 `json:"indexed"`
	Summaries int64 `json:"summaries"`
	// Users 全部账号数（含管理员），UsersToday 今天新建的。
	Users      int64 `json:"users"`
	UsersToday int64 `json:"users_today"`
	// Documents 当前索引里的文档数——「总计入库」用这个而不是累加入库：
	// 去重折叠与下架会让累加值虚高，用户看到的是「现在收了多少篇」。
	Documents int64 `json:"documents"`
	// Since 最早的管理员账号创建时间（Unix 秒），「运行时间」的起点；0 表示还没有管理员。
	Since int64 `json:"since"`
}

// UsageTotals 返回累计用量、账号数与运行起点。
func (s *Store) UsageTotals(ctx context.Context) (UsageTotals, error) {
	var t UsageTotals
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(searches),0), COALESCE(SUM(crawls),0),
		        COALESCE(SUM(indexed),0), COALESCE(SUM(summaries),0)
		   FROM usage_daily`).
		Scan(&t.Searches, &t.Crawls, &t.Indexed, &t.Summaries); err != nil {
		return t, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM users`).Scan(&t.Users); err != nil {
		return t, err
	}
	now := time.Now()
	// 今日按本地自然日算，与 usage.today() 口径一致（运营看的是本地的「今天」）
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM users WHERE created_at >= ?`, dayStart).Scan(&t.UsersToday); err != nil {
		return t, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM documents`).Scan(&t.Documents); err != nil {
		return t, err
	}
	since, err := s.EarliestAdminCreatedAt(ctx)
	if err != nil {
		return t, err
	}
	t.Since = since
	return t, nil
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

// UpdateFeedback 后台修正一条反馈：行类型、判定对象（标签或分类）与结论一起改。
//
// 两类对象互斥，不能同时留在一条记录里：标成具体标签时分类两列清空，
// 标成分类时 label 清空——否则「某标签的误判率」会把两种东西混着数。
// 用途：用户点错了管理员直接改对，而不是让它污染统计。
func (s *Store) UpdateFeedback(ctx context.Context, id int64, kind int, label string, noLabel bool, category, suggest, verdict string) error {
	if kind != FeedbackKindCategory {
		_, err := s.db.ExecContext(ctx,
			`UPDATE label_feedback SET kind = ?, label = ?, no_label = ?, category = '', suggest = '', verdict = ?, updated_at = ? WHERE id = ?`,
			kind, label, boolInt(noLabel), verdict, time.Now().Unix(), id)
		return err
	}
	// 分类反馈不存在「无标签」这回事，标记一并清掉。
	_, err := s.db.ExecContext(ctx,
		`UPDATE label_feedback SET kind = ?, label = '', no_label = 0, category = ?, suggest = ?, verdict = ?, updated_at = ? WHERE id = ?`,
		kind, category, suggest, verdict, time.Now().Unix(), id)
	return err
}

// boolInt SQLite 没有布尔类型，统一按 0/1 存。
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
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

// ---- AI 概览持久缓存 ----

// OverviewCacheEntry 一条已生成的 AI 概览。Sources 是 overviewSource 的 JSON。
type OverviewCacheEntry struct {
	Answer    string `json:"answer"`
	Sources   string `json:"sources"`
	Provider  string `json:"provider"`
	ExpiresAt int64  `json:"expires_at"`
}

// OverviewCacheGet 按问题读缓存。过期判断留给调用方（它知道 TTL 与时钟）。
func (s *Store) OverviewCacheGet(ctx context.Context, key string) (OverviewCacheEntry, bool, error) {
	var e OverviewCacheEntry
	err := s.db.QueryRowContext(ctx,
		`SELECT answer, sources, provider, expires_at FROM overview_cache WHERE query_key = ?`, key).
		Scan(&e.Answer, &e.Sources, &e.Provider, &e.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OverviewCacheEntry{}, false, nil
	}
	if err != nil {
		return OverviewCacheEntry{}, false, err
	}
	return e, true, nil
}

// OverviewCachePut 写缓存；同一个问题覆盖旧答案。
func (s *Store) OverviewCachePut(ctx context.Context, key string, e OverviewCacheEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO overview_cache (query_key, answer, sources, provider, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(query_key) DO UPDATE SET
			answer = excluded.answer, sources = excluded.sources,
			provider = excluded.provider, created_at = excluded.created_at,
			expires_at = excluded.expires_at`,
		key, e.Answer, e.Sources, e.Provider, time.Now().Unix(), e.ExpiresAt)
	return err
}

// OverviewCacheDeleteExpired 清理过期条目（写入新条目时顺手做，避免无限增长）。
func (s *Store) OverviewCacheDeleteExpired(ctx context.Context, now int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM overview_cache WHERE expires_at <= ?`, now)
	return err
}

// RestoreDB 用一份备份文件替换数据目录里的库。
//
// 为什么不直接覆盖：正在使用的库旁边有 -wal/-shm，残留的 WAL 会被回放到新库上，
// 症状是「恢复了备份，但数据还是新的那半」。所以先停服、删旁路文件、再替换。
// 现有库一律先另存一份（.before-restore-<时间>），恢复动作本身也要可回滚。
func RestoreDB(dataDir, src string) (string, error) {
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("备份文件不存在：%w", err)
	}
	// 支持 .gz：桶里存的是压缩版，直接喂给命令即可。
	if strings.HasSuffix(src, ".gz") {
		tmp, err := os.CreateTemp("", "stdseek-restore-*.db")
		if err != nil {
			return "", err
		}
		defer os.Remove(tmp.Name())
		in, err := os.Open(src)
		if err != nil {
			tmp.Close()
			return "", err
		}
		zr, err := gzip.NewReader(in)
		if err != nil {
			in.Close()
			tmp.Close()
			return "", fmt.Errorf("不是合法的 gzip：%w", err)
		}
		if _, err := io.Copy(tmp, zr); err != nil {
			zr.Close()
			in.Close()
			tmp.Close()
			return "", err
		}
		zr.Close()
		in.Close()
		tmp.Close()
		src = tmp.Name()
		// 临时文件在函数返回时删，先复制到数据目录再说。
		defer func() { _ = src }()
	}

	dst := dbPath(dataDir)
	var saved string
	if _, err := os.Stat(dst); err == nil {
		saved = dst + ".before-restore-" + time.Now().Format("20060102T150405")
		if err := copyFile(dst, saved); err != nil {
			return "", fmt.Errorf("备份现有库失败：%w", err)
		}
	}
	for _, p := range []string{dst, dst + "-wal", dst + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return saved, err
		}
	}
	if err := copyFile(src, dst); err != nil {
		return saved, err
	}
	// 校验能打开、表结构完整：把坏文件放进去比不恢复更糟。
	db, err := sql.Open("sqlite", "file:"+dst)
	if err != nil {
		return saved, err
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='table'`).Scan(&n); err != nil {
		return saved, fmt.Errorf("备份文件不是可用的 SQLite 库：%w", err)
	}
	if n == 0 {
		return saved, errors.New("备份库里没有任何表")
	}
	return saved, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
