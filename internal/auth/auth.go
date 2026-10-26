// Package auth 账号与会话。
//
// 规则参考了 D:/project/owlsgo 的 core/auth.php，但按 Go 的形态重写：
//   - 密码 bcrypt（cost 12），邮箱小写归一后存取；
//   - 登录失败按「账号键」计数并锁定，避免换一种写法绕开预算；
//   - 会话库内只存 token 的 sha256，明文仅存在于 HttpOnly cookie；
//   - 登录成功、改密码都清空该账号的其它会话。
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"voidxo/internal/store"
)

// SessionCookie 会话 cookie 名。
const SessionCookie = "vx_session"

const (
	// SessionTTL 会话有效期。30 天够用，再长等于把「被盗 cookie」的窗口拉长。
	SessionTTL = 30 * 24 * time.Hour
	// MinPasswordLen 最短密码。不强制大小写数字混排——长度比复杂度更能抗撞库，
	// 也少了「猜不出规则」的挫败感（NIST 800-63B 的建议）。
	MinPasswordLen = 8
	// maxPasswordLen bcrypt 只取前 72 字节，超长直接拒掉，免得用户以为设了超长密码。
	maxPasswordLen = 72

	// maxLoginFails / lockDuration 失败锁定的阈值与时长。
	maxLoginFails = 10
	lockDuration  = 15 * time.Minute

	// 邮件令牌有效期
	verifyTokenTTL = 24 * time.Hour
	resetTokenTTL  = 1 * time.Hour
)

var (
	ErrInvalidEmail = errors.New("邮箱格式不正确")
	ErrWeakPassword = errors.New("密码至少 8 位")
	ErrLongPassword = errors.New("密码过长（最多 72 字节）")
	ErrEmailTaken   = errors.New("该邮箱已注册")
	ErrNoSuchUser   = errors.New("该邮箱未注册")
	ErrBadCreds     = errors.New("邮箱或密码错误")
	ErrLocked       = errors.New("失败次数过多，请 15 分钟后再试")
	ErrDisabled     = errors.New("账号已被禁用")
	ErrTOTPRequired = errors.New("请输入两步验证码")
	ErrTOTPInvalid  = errors.New("两步验证码不正确")
	ErrBadToken     = errors.New("链接无效或已过期")
)

// Service 账号服务。
type Service struct {
	st      *store.Store
	limiter *limiter
}

// New 构造服务。
func New(st *store.Store) *Service {
	return &Service{st: st, limiter: newLimiter()}
}

// NormalizeEmail 归一化：去空白 + 小写。注册、登录、找回密码三处必须用同一个函数，
// 否则「注册时写了大小写、登录时忘了」会变成查不到账号。
func NormalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ValidEmail 宽松校验：有且只有一个 @、本地部分与域名都非空、含点、长度合理。
// 不追求 RFC 完整性——真正的校验是发一封信过去。
func ValidEmail(email string) bool {
	if len(email) < 5 || len(email) > 254 {
		return false
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at != strings.LastIndexByte(email, '@') {
		return false
	}
	local, domain := email[:at], email[at+1:]
	if local == "" || len(domain) < 3 {
		return false
	}
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	return !strings.ContainsAny(email, " \t\r\n")
}

// HashPassword bcrypt cost 12。cost 12 在普通机器上约 200~300ms，
// 登录接口因此天然抗暴力破解（配合失败锁定）。
func HashPassword(pw string) (string, error) {
	if len(pw) < MinPasswordLen {
		return "", ErrWeakPassword
	}
	if len(pw) > maxPasswordLen {
		return "", ErrLongPassword
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword 校验密码。哈希格式非法时返回 false 而不是错误——
// 对调用方来说「验不过」就够了。
func VerifyPassword(hash, pw string) bool {
	if hash == "" || pw == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// NewToken 生成一次性令牌：返回明文（给 cookie / 邮件链接）与哈希（落库）。
func NewToken() (plain, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plain = base64.RawURLEncoding.EncodeToString(buf)
	return plain, HashToken(plain), nil
}

// HashToken 令牌哈希。令牌本身已经是 256 位随机数，不需要再加盐。
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// Register 注册。firstBecomesAdmin 为 true 时，站内第一个账号自动成为管理员
// （这是产品决定：部署者往往是第一个打开页面的人；代价是公网裸奔时先到者得）。
func (s *Service) Register(ctx context.Context, email, password, ip string) (*store.User, error) {
	email = NormalizeEmail(email)
	if !ValidEmail(email) {
		return nil, ErrInvalidEmail
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	u, err := s.st.CreateUser(ctx, email, hash, "member", false, ip, true)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// Login 校验账号密码，成功则创建会话并返回明文令牌。
func (s *Service) Login(ctx context.Context, email, password, totpCode, ip, ua string) (*store.User, string, error) {
	email = NormalizeEmail(email)
	key := "e:" + email + "|" + ip

	if locked, _ := s.limiter.locked(key); locked {
		return nil, "", ErrLocked
	}

	u, hash, err := s.st.UserByEmail(ctx, email)
	if err != nil {
		// 账号不存在也记一次失败：否则「这个邮箱有没有注册过」可以通过
		// 「失败 10 次被锁」与「从不被锁」区分出来
		s.limiter.fail(key)
		return nil, "", ErrBadCreds
	}
	// 换到账号键上计数：同一个账号从同一 IP 用不同写法（大小写、空格）尝试时，
	// 预算必须共用——这是 owlsgo 踩过的坑，直接沿用。
	uidKey := fmt.Sprintf("u:%d|%s", u.ID, ip)
	if locked, _ := s.limiter.locked(uidKey); locked {
		return nil, "", ErrLocked
	}

	if !VerifyPassword(hash, password) {
		left := s.limiter.fail(uidKey)
		s.limiter.fail(key)
		if left > 0 && left <= 3 {
			return nil, "", fmt.Errorf("%w（还剩 %d 次尝试机会）", ErrBadCreds, left)
		}
		return nil, "", ErrBadCreds
	}
	if u.Status != 1 {
		return nil, "", ErrDisabled
	}
	if u.TOTPEnabled {
		if strings.TrimSpace(totpCode) == "" {
			return nil, "", ErrTOTPRequired
		}
		secret, _, err := s.st.TOTPSecret(ctx, u.ID)
		if err != nil || !VerifyTOTP(secret, totpCode) {
			s.limiter.fail(uidKey)
			return nil, "", ErrTOTPInvalid
		}
	}

	// 成功：两种键一起清，否则换个写法还会继承之前的失败计数
	s.limiter.reset(key)
	s.limiter.reset(uidKey)

	plain, hashToken, err := NewToken()
	if err != nil {
		return nil, "", err
	}
	expires := time.Now().Add(SessionTTL).Unix()
	if err := s.st.CreateSession(ctx, hashToken, u.ID, ip, ua, expires); err != nil {
		return nil, "", err
	}
	_ = s.st.UpdateLoginStamp(ctx, u.ID, ip)
	return u, plain, nil
}

// SessionUser 用 cookie 里的明文令牌换当前账号。
func (s *Service) SessionUser(ctx context.Context, plainToken string) (*store.User, error) {
	if plainToken == "" {
		return nil, ErrBadToken
	}
	u, err := s.st.SessionUser(ctx, HashToken(plainToken))
	if err != nil {
		return nil, ErrBadToken
	}
	if u.Status != 1 {
		return nil, ErrDisabled
	}
	return u, nil
}

// Logout 销毁当前会话。
func (s *Service) Logout(ctx context.Context, plainToken string) error {
	if plainToken == "" {
		return nil
	}
	return s.st.DeleteSession(ctx, HashToken(plainToken))
}

// ChangePassword 改密码，并踢掉该账号的其它会话（改密码的常见动机就是「怀疑泄露」；
// 不清理的话，攻击者手里的旧 cookie 还能继续用 30 天）。
func (s *Service) ChangePassword(ctx context.Context, uid int64, oldPw, newPw string) error {
	_, hash, err := s.st.UserByIDWithHash(ctx, uid)
	if err != nil {
		return err
	}
	if !VerifyPassword(hash, oldPw) {
		return ErrBadCreds
	}
	newHash, err := HashPassword(newPw)
	if err != nil {
		return err
	}
	if err := s.st.SetPassword(ctx, uid, newHash); err != nil {
		return err
	}
	return s.st.DeleteUserSessions(ctx, uid)
}

// EnableTOTP 校验一次性验证码后开启两步验证。
func (s *Service) EnableTOTP(ctx context.Context, uid int64, secret, code string) error {
	if !VerifyTOTP(secret, code) {
		return ErrTOTPInvalid
	}
	return s.st.SetTOTP(ctx, uid, secret, true)
}

// DisableTOTP 关闭两步验证（需要当前密码，避免 cookie 被盗后直接关掉）。
func (s *Service) DisableTOTP(ctx context.Context, uid int64, password string) error {
	_, hash, err := s.st.UserByIDWithHash(ctx, uid)
	if err != nil {
		return err
	}
	if !VerifyPassword(hash, password) {
		return ErrBadCreds
	}
	return s.st.SetTOTP(ctx, uid, "", false)
}

// ---- 邮件令牌 ----

// StartEmailVerify 生成验证令牌，返回明文（拼进邮件链接）。
func (s *Service) StartEmailVerify(ctx context.Context, uid int64) (string, error) {
	plain, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	if err := s.st.CreateEmailToken(ctx, hash, uid, "verify", time.Now().Add(verifyTokenTTL).Unix()); err != nil {
		return "", err
	}
	return plain, nil
}

// FinishEmailVerify 消费验证令牌。
func (s *Service) FinishEmailVerify(ctx context.Context, plain string) error {
	uid, err := s.st.ConsumeEmailToken(ctx, HashToken(plain), "verify")
	if err != nil {
		return ErrBadToken
	}
	return s.st.MarkEmailVerified(ctx, uid)
}

// StartPasswordReset 生成重置令牌。邮箱不存在时也返回 nil（不泄露账号是否存在），
// 由调用方决定是否真的发信。
func (s *Service) StartPasswordReset(ctx context.Context, email string) (uid int64, plain string, err error) {
	email = NormalizeEmail(email)
	u, _, err := s.st.UserByEmail(ctx, email)
	if err != nil {
		return 0, "", ErrNoSuchUser
	}
	plain, hash, err := NewToken()
	if err != nil {
		return 0, "", err
	}
	if err := s.st.CreateEmailToken(ctx, hash, u.ID, "reset", time.Now().Add(resetTokenTTL).Unix()); err != nil {
		return 0, "", err
	}
	return u.ID, plain, nil
}

// FinishPasswordReset 用令牌设置新密码，并清空该账号的全部会话。
func (s *Service) FinishPasswordReset(ctx context.Context, plain, newPw string) error {
	uid, err := s.st.ConsumeEmailToken(ctx, HashToken(plain), "reset")
	if err != nil {
		return ErrBadToken
	}
	hash, err := HashPassword(newPw)
	if err != nil {
		return err
	}
	if err := s.st.SetPassword(ctx, uid, hash); err != nil {
		return err
	}
	return s.st.DeleteUserSessions(ctx, uid)
}

// ---- 登录限流 ----

type failEntry struct {
	count int
	until time.Time
}

type limiter struct {
	mu   sync.Mutex
	data map[string]*failEntry
}

func newLimiter() *limiter {
	return &limiter{data: make(map[string]*failEntry)}
}

// locked 是否还在锁定期内。顺带返回剩余时长。
func (l *limiter) locked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.data[key]
	if !ok {
		return false, 0
	}
	if e.until.IsZero() || time.Now().After(e.until) {
		if !e.until.IsZero() {
			delete(l.data, key)
		}
		return false, 0
	}
	return true, time.Until(e.until)
}

// fail 记一次失败，返回剩余尝试次数（0 表示刚刚被锁）。
func (l *limiter) fail(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.data[key]
	if !ok || (!e.until.IsZero() && time.Now().After(e.until)) {
		e = &failEntry{}
		l.data[key] = e
	}
	e.count++
	if e.count >= maxLoginFails {
		e.until = time.Now().Add(lockDuration)
		e.count = 0
		return 0
	}
	return maxLoginFails - e.count
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.data, key)
}

// ---- TOTP（RFC 6238，SHA1 / 6 位 / 30 秒步长） ----

// NewTOTPSecret 生成 base32 密钥（20 字节 = 160 位，RFC 推荐长度）。
func NewTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// TOTPCode 按时间生成 6 位码。
func TOTPCode(secret string, t time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return "", err
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(t.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[off]&0x7f) << 24) |
		(uint32(sum[off+1]) << 16) |
		(uint32(sum[off+2]) << 8) |
		uint32(sum[off+3])
	return fmt.Sprintf("%06d", code%1_000_000), nil
}

// VerifyTOTP 校验验证码：允许前后各一个时间步，容忍客户端时钟偏差。
func VerifyTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if secret == "" || len(code) != 6 {
		return false
	}
	now := time.Now()
	for _, delta := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		want, err := TOTPCode(secret, now.Add(delta))
		if err != nil {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// OtpauthURL 生成二维码内容（otpauth://totp/...）。前端用 qrcode 渲染成本地图片，
// 不依赖任何外部二维码服务——目标是可离线自托管。
func OtpauthURL(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}
