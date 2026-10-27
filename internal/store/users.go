package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrEmailTaken 邮箱已被注册。抽成哨兵错误，让 handler 不必解析 SQLite 的错误串。
var ErrEmailTaken = errors.New("email taken")

// User 账号。密码哈希不放进结构体——它只在登录校验时按需取，避免不小心被 JSON 序列化出去。
type User struct {
	ID            int64  `json:"id"`
	Email         string `json:"email"`
	Role          string `json:"role"`
	Status        int    `json:"status"`
	EmailVerified bool   `json:"email_verified"`
	TOTPEnabled   bool   `json:"totp_enabled"`
	CreatedAt     int64  `json:"created_at"`
	LastLogin     int64  `json:"last_login"`
}

const userCols = `id, email, role, status, email_verified, totp_enabled, created_at, last_login`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var verified, totp int
	if err := row.Scan(&u.ID, &u.Email, &u.Role, &u.Status, &verified, &totp, &u.CreatedAt, &u.LastLogin); err != nil {
		return nil, err
	}
	u.EmailVerified = verified == 1
	u.TOTPEnabled = totp == 1
	return &u, nil
}

// CountUsers 账号总数，用于「第一个注册者成为管理员」的判定。
//
// 判定必须和插入在同一个事务里，否则两个并发注册会同时看到 0 都成为管理员。
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser 建账号。firstBecomesAdmin 为 true 且当前没有任何账号时，role 强制为 admin。
//
// 整个判断 + 插入在一个事务里完成（见 CountUsers 的注释）。
func (s *Store) CreateUser(ctx context.Context, email, passwordHash, role string, verified bool, ip string, firstBecomesAdmin bool) (*User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if firstBecomesAdmin {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			role = "admin"
		}
	}

	now := time.Now().Unix()
	v := 0
	if verified {
		v = 1
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO users (email, password_hash, role, status, email_verified, created_at, reg_ip)
		VALUES (?, ?, ?, 1, ?, ?, ?)`,
		email, passwordHash, role, v, now, ip)
	if err != nil {
		// modernc/sqlite 的唯一约束错误串里带 UNIQUE，按串判断比引 SQLite 错误码更省事
		if isUniqueViolation(err) {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &User{ID: id, Email: email, Role: role, Status: 1, EmailVerified: verified, CreatedAt: now}, nil
}

func isUniqueViolation(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// UserByEmail 按归一化后的邮箱查账号，同时返回密码哈希（登录校验用）。
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, string, error) {
	var hash string
	u, err := scanUserHash(s.db.QueryRowContext(ctx,
		`SELECT `+userCols+`, password_hash FROM users WHERE email = ?`, email), &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", sql.ErrNoRows
	}
	return u, hash, err
}

// UserByID 查账号。
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	return u, err
}

func scanUserHash(row interface{ Scan(...any) error }, hash *string) (*User, error) {
	var u User
	var verified, totp int
	if err := row.Scan(&u.ID, &u.Email, &u.Role, &u.Status, &verified, &totp, &u.CreatedAt, &u.LastLogin, hash); err != nil {
		return nil, err
	}
	u.EmailVerified = verified == 1
	u.TOTPEnabled = totp == 1
	return &u, nil
}

// UserByIDWithHash 按 ID 取账号与密码哈希（改密码时校验原密码用）。
func (s *Store) UserByIDWithHash(ctx context.Context, id int64) (*User, string, error) {
	var hash string
	u, err := scanUserHash(s.db.QueryRowContext(ctx,
		`SELECT `+userCols+`, password_hash FROM users WHERE id = ?`, id), &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", sql.ErrNoRows
	}
	return u, hash, err
}

// UpdateLoginStamp 登录成功后记录时间与来源 IP。
func (s *Store) UpdateLoginStamp(ctx context.Context, id int64, ip string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_login = ?, last_login_ip = ? WHERE id = ?`, time.Now().Unix(), ip, id)
	return err
}

// SetPassword 改密码。
func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

// SetEmail 改邮箱，并把验证状态重置为未验证（换了地址就得重新验证）。
func (s *Store) SetEmail(ctx context.Context, id int64, email string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET email = ?, email_verified = 0 WHERE id = ?`, email, id)
	return err
}

// MarkEmailVerified 标记邮箱已验证。
func (s *Store) MarkEmailVerified(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET email_verified = 1 WHERE id = ?`, id)
	return err
}

// SetUserRole 改角色（admin / member）。
func (s *Store) SetUserRole(ctx context.Context, id int64, role string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, id)
	return err
}

// SetUserStatus 启用/禁用账号。禁用时同时清掉它的会话（否则已登录的还能继续用）。
func (s *Store) SetUserStatus(ctx context.Context, id int64, status int) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET status = ? WHERE id = ?`, status, id); err != nil {
		return err
	}
	if status != 1 {
		return s.DeleteUserSessions(ctx, id)
	}
	return nil
}

// SetTOTP 写入两步验证密钥与开关状态。
func (s *Store) SetTOTP(ctx context.Context, id int64, secret string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE users SET totp_secret = ?, totp_enabled = ? WHERE id = ?`, secret, v, id)
	return err
}

// TOTPSecret 取两步验证密钥（只有本人开启时才非空）。
func (s *Store) TOTPSecret(ctx context.Context, id int64) (string, bool, error) {
	var secret string
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT totp_secret, totp_enabled FROM users WHERE id = ?`, id).Scan(&secret, &enabled)
	return secret, enabled == 1, err
}

// ListUsers 账号列表（后台用），按创建时间倒序。kw 非空时按邮箱模糊匹配。
func (s *Store) ListUsers(ctx context.Context, page, size int, kw string) ([]User, int, error) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 20
	}
	kw = strings.TrimSpace(strings.ToLower(kw))
	where := ""
	args := []any{}
	if kw != "" {
		// 邮箱统一小写存，直接 LIKE 即可（无需 COLLATE）
		where = ` WHERE email LIKE ?`
		args = append(args, "%"+kw+"%")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userCols+` FROM users`+where+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, size, (page-1)*size)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]User, 0, size)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *u)
	}
	return out, total, rows.Err()
}

// ---- 会话 ----

// CreateSession 落一条会话。tokenHash 是 cookie 明文 token 的 sha256。
func (s *Store) CreateSession(ctx context.Context, tokenHash string, uid int64, ip, ua string, expiresAt int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, uid, created_at, expires_at, ip, ua)
		VALUES (?, ?, ?, ?, ?, ?)`,
		tokenHash, uid, time.Now().Unix(), expiresAt, ip, ua)
	return err
}

// SessionUser 用 tokenHash 换账号：过期的会话顺手删掉，返回 sql.ErrNoRows 表示无效。
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (*User, error) {
	var uid int64
	var expires int64
	err := s.db.QueryRowContext(ctx,
		`SELECT uid, expires_at FROM sessions WHERE token_hash = ?`, tokenHash).Scan(&uid, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	if expires < time.Now().Unix() {
		_ = s.DeleteSession(ctx, tokenHash)
		return nil, sql.ErrNoRows
	}
	return s.UserByID(ctx, uid)
}

// DeleteSession 登出。
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteUserSessions 清掉某个账号的全部会话。改密码、禁用账号时要调。
func (s *Store) DeleteUserSessions(ctx context.Context, uid int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE uid = ?`, uid)
	return err
}

// PurgeExpiredSessions 清理过期会话，启动时调一次即可（量级很小）。
func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	return err
}

// ---- 邮件一次性令牌 ----

// CreateEmailToken 发一条验证/重置令牌。同 uid + kind 的旧令牌会被作废：
// 用户重复点「重新发送」时，只有最新那封邮件里的链接有效。
func (s *Store) CreateEmailToken(ctx context.Context, tokenHash string, uid int64, kind string, expiresAt int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE email_tokens SET used = 1 WHERE uid = ? AND kind = ? AND used = 0`, uid, kind); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO email_tokens (token_hash, uid, kind, expires_at, used, created_at)
		VALUES (?, ?, ?, ?, 0, ?)`,
		tokenHash, uid, kind, expiresAt, time.Now().Unix())
	return err
}

// ConsumeEmailToken 校验并消费令牌，返回 uid。已用过、过期、kind 不符都返回 sql.ErrNoRows。
func (s *Store) ConsumeEmailToken(ctx context.Context, tokenHash, kind string) (int64, error) {
	var uid, expires int64
	var used int
	err := s.db.QueryRowContext(ctx,
		`SELECT uid, expires_at, used FROM email_tokens WHERE token_hash = ? AND kind = ?`,
		tokenHash, kind).Scan(&uid, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, sql.ErrNoRows
	}
	if err != nil {
		return 0, err
	}
	if used == 1 || expires < time.Now().Unix() {
		return 0, sql.ErrNoRows
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE email_tokens SET used = 1 WHERE token_hash = ?`, tokenHash); err != nil {
		return 0, err
	}
	return uid, nil
}

// ---- 通行密钥（WebAuthn） ----

// Passkey 一把通行密钥。public_key 与 sign_count 不出现在 JSON 里（前者是二进制，
// 后者只对校验有意义）。
type Passkey struct {
	ID           int64  `json:"id"`
	CredentialID string `json:"credential_id"`
	Name         string `json:"name"`
	CreatedAt    int64  `json:"created_at"`
	LastUsed     int64  `json:"last_used"`
}

// CreatePasskey 落一把新密钥。
func (s *Store) CreatePasskey(ctx context.Context, uid int64, credID string, publicKey []byte, signCount uint32, flags uint8, aaguid, name string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO passkeys (uid, credential_id, public_key, sign_count, flags, aaguid, name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		uid, credID, publicKey, int64(signCount), int(flags), aaguid, name, time.Now().Unix())
	return err
}

// ListPasskeys 某账号的全部密钥（后台/设置页展示用）。
func (s *Store) ListPasskeys(ctx context.Context, uid int64) ([]Passkey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, credential_id, name, created_at, last_used FROM passkeys
		 WHERE uid = ? ORDER BY created_at DESC`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		var p Passkey
		if err := rows.Scan(&p.ID, &p.CredentialID, &p.Name, &p.CreatedAt, &p.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PasskeyCredential 校验签名需要的完整数据。
type PasskeyCredential struct {
	ID           int64
	UID          int64
	CredentialID string
	PublicKey    []byte
	SignCount    uint32
	// Flags 认证器标志位（UP/UV/BE/BS 的位掩码），登录校验时按原值还原
	Flags uint8
}

// PasskeyByCredID 按 credential id 查密钥（登录时用）。
func (s *Store) PasskeyByCredID(ctx context.Context, credID string) (*PasskeyCredential, error) {
	var p PasskeyCredential
	var count, flags int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, uid, credential_id, public_key, sign_count, flags FROM passkeys WHERE credential_id = ?`,
		credID).Scan(&p.ID, &p.UID, &p.CredentialID, &p.PublicKey, &count, &flags)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	p.SignCount = uint32(count)
	p.Flags = uint8(flags)
	return &p, nil
}

// UserPasskeys 某账号的全部密钥凭据（排除列表：不能让用户拿自己的密钥签别人的名）。
func (s *Store) UserPasskeys(ctx context.Context, uid int64) ([]PasskeyCredential, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, uid, credential_id, public_key, sign_count, flags FROM passkeys WHERE uid = ?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PasskeyCredential
	for rows.Next() {
		var p PasskeyCredential
		var count, flags int64
		if err := rows.Scan(&p.ID, &p.UID, &p.CredentialID, &p.PublicKey, &count, &flags); err != nil {
			return nil, err
		}
		p.SignCount = uint32(count)
		p.Flags = uint8(flags)
		out = append(out, p)
	}
	return out, rows.Err()
}

// TouchPasskey 校验成功后更新计数器与最后使用时间。
func (s *Store) TouchPasskey(ctx context.Context, id int64, signCount uint32) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE passkeys SET sign_count = ?, last_used = ? WHERE id = ?`,
		int64(signCount), time.Now().Unix(), id)
	return err
}

// DeletePasskey 删除一把密钥。必须带 uid：不能让别人删你的。
func (s *Store) DeletePasskey(ctx context.Context, id, uid int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM passkeys WHERE id = ? AND uid = ?`, id, uid)
	return err
}

// CountPasskeys 某账号的密钥数量。
func (s *Store) CountPasskeys(ctx context.Context, uid int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkeys WHERE uid = ?`, uid).Scan(&n)
	return n, err
}

// ---- 登录事件（最近登录的设备） ----

// LoginEvent 一次登录/注册成功的记录。
type LoginEvent struct {
	ID        int64  `json:"id"`
	IP        string `json:"ip"`
	UA        string `json:"ua"`
	Kind      string `json:"kind"`
	CreatedAt int64  `json:"created_at"`
	// Current 表示这条记录对应「当前仍在有效的会话」
	Current bool `json:"current"`
	// Geo 已缓存的归属地画像（没解析过则为 nil）
	Geo *GeoInfo `json:"geo,omitempty"`
}

// RecordLoginEvent 记一条登录轨迹。
//
// 同一 uid + ip + ua 在 1 小时内重复登录只留一条：
// 反复登录同一个浏览器不该把「最近设备」刷满整页。
func (s *Store) RecordLoginEvent(ctx context.Context, uid int64, ip, ua, kind string) error {
	now := time.Now().Unix()
	var last int64
	err := s.db.QueryRowContext(ctx,
		`SELECT created_at FROM login_events WHERE uid = ? AND ip = ? AND ua = ? ORDER BY created_at DESC LIMIT 1`,
		uid, ip, ua).Scan(&last)
	if err == nil && now-last < 3600 {
		// 只更新时间，不新增：这仍然保证「当前在线」能对上最新会话
		_, err = s.db.ExecContext(ctx,
			`UPDATE login_events SET created_at = ? WHERE uid = ? AND ip = ? AND ua = ? AND created_at = ?`,
			now, uid, ip, ua, last)
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO login_events (uid, ip, ua, kind, created_at) VALUES (?, ?, ?, ?, ?)`,
		uid, ip, ua, kind, now)
	return err
}

// maxLoginEventAge 登录设备列表的时间窗口。
//
// 只显示一个月内的记录：更旧的记录对用户判断「现在谁在用我的账号」没有意义，
// 反而会把「最近登录的设备」这个位置挤成一长串历史。数据本身不删，只是不展示。
const maxLoginEventAge = 30 * 24 * time.Hour

// ListLoginEvents 取某账号最近的登录设备（只含一个月内）。limit 默认 10、上限 50。
//
// 每条会标记 Current：库里存在一条未过期的会话，且 ip/ua 与这条记录一致。
func (s *Store) ListLoginEvents(ctx context.Context, uid int64, limit int) ([]LoginEvent, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.ip, e.ua, e.kind, e.created_at,
		       EXISTS(
		         SELECT 1 FROM sessions s
		         WHERE s.uid = e.uid AND s.ip = e.ip AND s.ua = e.ua AND s.expires_at > ?
		       ) AS current
		FROM login_events e
		WHERE e.uid = ? AND e.created_at >= ?
		ORDER BY e.created_at DESC
		LIMIT ?`, time.Now().Unix(), uid, time.Now().Add(-maxLoginEventAge).Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]LoginEvent, 0, limit)
	for rows.Next() {
		var e LoginEvent
		var cur int
		if err := rows.Scan(&e.ID, &e.IP, &e.UA, &e.Kind, &e.CreatedAt, &cur); err != nil {
			return nil, err
		}
		e.Current = cur == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- IP 封禁 ----

// IPBan 一条封禁规则。
type IPBan struct {
	ID        int64  `json:"id"`
	Pattern   string `json:"pattern"`
	Reason    string `json:"reason"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
}

// ActiveIPBans 当前生效的规则（过期的忽略）。
func (s *Store) ActiveIPBans(ctx context.Context) ([]IPBan, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, pattern, reason, created_at, expires_at FROM ip_bans
		WHERE expires_at = 0 OR expires_at > ?
		ORDER BY created_at DESC`, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IPBan
	for rows.Next() {
		var b IPBan
		if err := rows.Scan(&b.ID, &b.Pattern, &b.Reason, &b.CreatedAt, &b.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// AddIPBan 新增封禁。
func (s *Store) AddIPBan(ctx context.Context, pattern, reason string, expiresAt int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ip_bans (pattern, reason, created_at, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(pattern) DO UPDATE SET reason = excluded.reason, expires_at = excluded.expires_at`,
		pattern, reason, time.Now().Unix(), expiresAt)
	return err
}

// RemoveIPBan 删除规则。
func (s *Store) RemoveIPBan(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM ip_bans WHERE id = ?`, id)
	return err
}

// ---- 归属地缓存 ----

// GeoLabelOf 读某个 IP 的归属地，无缓存返回空串。
func (s *Store) GeoLabelOf(ctx context.Context, ip string) (string, error) {
	var label string
	err := s.db.QueryRowContext(ctx,
		`SELECT label FROM geo_cache WHERE ip = ?`, ip).Scan(&label)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	return label, nil
}

// GeoInfo 一个 IP 的完整画像（归属地 + ASN + 信誉）。
type GeoInfo struct {
	Label  string `json:"label"` // 地区：国家 省 市
	ISP    string `json:"isp"`   // 网络运营商（中国移动 / Cloudflare 等）
	ASN    int    `json:"asn"`
	Score  int    `json:"score"`
	Status string `json:"status"`
	Risk   string `json:"risk"` // JSON 数组
}

// SaveGeoInfo 写完整画像缓存。
func (s *Store) SaveGeoInfo(ctx context.Context, ip string, g GeoInfo) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO geo_cache (ip, label, resolved_at, asn, score, status, risk, isp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ip) DO UPDATE SET
			label = excluded.label, resolved_at = excluded.resolved_at,
			asn = excluded.asn, score = excluded.score,
			status = excluded.status, risk = excluded.risk, isp = excluded.isp`,
		ip, g.Label, time.Now().Unix(), g.ASN, g.Score, g.Status, g.Risk, g.ISP)
	return err
}

// GeoInfoOf 读某个 IP 的完整画像，无缓存返回零值。
func (s *Store) GeoInfoOf(ctx context.Context, ip string) (GeoInfo, error) {
	var g GeoInfo
	err := s.db.QueryRowContext(ctx,
		`SELECT label, asn, score, status, risk, isp FROM geo_cache WHERE ip = ?`, ip).
		Scan(&g.Label, &g.ASN, &g.Score, &g.Status, &g.Risk, &g.ISP)
	if err != nil && err != sql.ErrNoRows {
		return GeoInfo{}, err
	}
	return g, nil
}

// GeoRiskRow 一个 IP 的风险画像：归属地 + ASN + 信誉分 + 关联账号数。
type GeoRiskRow struct {
	IP       string `json:"ip"`
	Label    string `json:"label"`
	ISP      string `json:"isp"`
	ASN      int    `json:"asn"`
	Score    int    `json:"score"`
	Status   string `json:"status"`
	Risk     string `json:"risk"` // 风险标记 JSON 数组（datacenter / vpn / proxy / tor / abuser…）
	Accounts int    `json:"accounts"`
	Logins   int    `json:"logins"`
	LastSeen int64  `json:"last_seen"`
}

// ListIPRisk 分页返回「同一 IP 上活动过的账号数 ≥2」的风险列表。
//
// 只统计登录事件里的 IP；单账号 IP 是用户自己的设备，不算风险。
// kw 按 IP 前缀模糊匹配（后台搜索框用）。
func (s *Store) ListIPRisk(ctx context.Context, kw string, page, size int) ([]GeoRiskRow, int, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 20
	}
	kw = strings.TrimSpace(kw)
	like := kw + "%"

	// 总数（分页用）
	var total int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT e.ip FROM login_events e
			WHERE (? = '' OR e.ip LIKE ?)
			GROUP BY e.ip
			HAVING COUNT(DISTINCT e.uid) >= 2
		) t`, kw, like).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT e.ip,
		       COUNT(DISTINCT e.uid) AS accounts,
		       COUNT(*) AS logins,
		       MAX(e.created_at) AS last_seen,
		       COALESCE((SELECT label FROM geo_cache g WHERE g.ip = e.ip), '') AS label,
		       COALESCE((SELECT isp FROM geo_cache g WHERE g.ip = e.ip), '') AS isp,
		       COALESCE((SELECT asn FROM geo_cache g WHERE g.ip = e.ip), 0) AS asn,
		       COALESCE((SELECT score FROM geo_cache g WHERE g.ip = e.ip), 0) AS score,
		       COALESCE((SELECT status FROM geo_cache g WHERE g.ip = e.ip), '') AS status,
		       COALESCE((SELECT risk FROM geo_cache g WHERE g.ip = e.ip), '[]') AS risk
		FROM login_events e
		WHERE (? = '' OR e.ip LIKE ?)
		GROUP BY e.ip
		HAVING COUNT(DISTINCT e.uid) >= 2
		ORDER BY accounts DESC, logins DESC, last_seen DESC
		LIMIT ? OFFSET ?`, kw, like, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []GeoRiskRow
	for rows.Next() {
		var r GeoRiskRow
		if err := rows.Scan(&r.IP, &r.Accounts, &r.Logins, &r.LastSeen, &r.Label, &r.ISP, &r.ASN, &r.Score, &r.Status, &r.Risk); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// AccountsByIP 反查：给定 IP，列出用过它的账号（「同 IP 的其他账号」）。
func (s *Store) AccountsByIP(ctx context.Context, ip string) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT u.id, u.email, u.role, u.status, u.email_verified, u.totp_enabled,
		       u.created_at, u.last_login
		FROM users u JOIN login_events e ON e.uid = u.id
		WHERE e.ip = ? ORDER BY u.id`, ip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// EarliestAdminCreatedAt 最早的管理员账号创建时间。
//
// 隐私政策用它当「生效日」——服务从那天开始收集数据，比写死一个日期可靠：
// 换部署环境、重建库都能自动对上。
func (s *Store) EarliestAdminCreatedAt(ctx context.Context) (int64, error) {
	var ts sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(created_at) FROM users WHERE role = 'admin'`).Scan(&ts); err != nil {
		return 0, err
	}
	if !ts.Valid {
		return 0, nil
	}
	return ts.Int64, nil
}
