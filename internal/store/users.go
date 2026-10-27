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

// ListUsers 账号列表（后台用），按创建时间倒序。
func (s *Store) ListUsers(ctx context.Context, page, size int) ([]User, int, error) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 20
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userCols+` FROM users ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		size, (page-1)*size)
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
func (s *Store) CreatePasskey(ctx context.Context, uid int64, credID string, publicKey []byte, signCount uint32, aaguid, name string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO passkeys (uid, credential_id, public_key, sign_count, aaguid, name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uid, credID, publicKey, int64(signCount), aaguid, name, time.Now().Unix())
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
}

// PasskeyByCredID 按 credential id 查密钥（登录时用）。
func (s *Store) PasskeyByCredID(ctx context.Context, credID string) (*PasskeyCredential, error) {
	var p PasskeyCredential
	var count int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, uid, credential_id, public_key, sign_count FROM passkeys WHERE credential_id = ?`,
		credID).Scan(&p.ID, &p.UID, &p.CredentialID, &p.PublicKey, &count)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	p.SignCount = uint32(count)
	return &p, nil
}

// UserPasskeys 某账号的全部密钥凭据（排除列表：不能让用户拿自己的密钥签别人的名）。
func (s *Store) UserPasskeys(ctx context.Context, uid int64) ([]PasskeyCredential, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, uid, credential_id, public_key, sign_count FROM passkeys WHERE uid = ?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PasskeyCredential
	for rows.Next() {
		var p PasskeyCredential
		var count int64
		if err := rows.Scan(&p.ID, &p.UID, &p.CredentialID, &p.PublicKey, &count); err != nil {
			return nil, err
		}
		p.SignCount = uint32(count)
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
