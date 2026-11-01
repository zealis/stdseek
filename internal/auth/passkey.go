// 通行密钥（WebAuthn）。
//
// 用的是 github.com/go-webauthn/webauthn：CBOR 解析 + COSE 公钥校验 + 签名验证
// 这套东西手写不现实（也容易写出难以发现的漏洞），这类协议实现交给成熟的库才对。
//
// 会话状态（challenge）不落库：Begin 之后把 SessionData 放进程内 map，10 分钟过期。
// 重启后未完成的注册/登录失效，用户重试一次即可——比为此建一张临时表划算。
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/zealis/stdseek/internal/store"
)

// passkeyTTL 一次 challenge 的有效期。
const passkeyTTL = 5 * time.Minute

var (
	// ErrPasskeyNeedsDomain 用 IP 访问时抛这个：WebAuthn 的 RPID 必须是域名，
	// 浏览器不会为 IP 签发凭据。
	ErrPasskeyNeedsDomain = errors.New("通行密钥需要域名或 localhost，当前是用 IP 访问的；请改用 http://localhost:端口 打开")
	ErrNoPasskey          = errors.New("这个账号还没有登记通行密钥")
	ErrPasskeyFailed      = errors.New("通行密钥验证失败，请重试")
)

// PasskeyService 通行密钥编解码 + 校验。
type PasskeyService struct {
	st          *store.Store
	displayName string

	mu   sync.Mutex
	sess map[string]passkeySession
	// 按 origin 缓存 WebAuthn 实例。RPID 必须与浏览器地址栏的域名一致，
	// 而本地开发（localhost）、局域网 IP、正式域名是三个不同的 RPID——
	// 写死一个就会在另外两个环境里被浏览器直接拒绝。
	instances map[string]*webauthn.WebAuthn
}

type passkeySession struct {
	data *webauthn.SessionData
	exp  time.Time
}

// NewPasskey 构造。WebAuthn 实例按请求来源惰性创建（见 instance），
// 这样同一份二进制在 localhost、局域网 IP、正式域名下都能工作。
func NewPasskey(st *store.Store, displayName string) *PasskeyService {
	return &PasskeyService{
		st:          st,
		displayName: displayName,
		sess:        make(map[string]passkeySession),
		instances:   make(map[string]*webauthn.WebAuthn),
	}
}

// instance 按 origin（scheme://host）取或建 WebAuthn 实例。
func (s *PasskeyService) instance(origin string) (*webauthn.WebAuthn, error) {
	s.mu.Lock()
	if wa, ok := s.instances[origin]; ok {
		s.mu.Unlock()
		return wa, nil
	}
	s.mu.Unlock()

	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return nil, ErrPasskeyFailed
	}
	// WebAuthn 规范要求 RPID 是域名，IP 会被库直接拒绝（"value must be a domain
	// and not an IP address"）。127.0.0.1 也一样——浏览器把它当安全上下文，
	// 但 RPID 仍然必须是域名，所以引导用户改用 localhost。
	if net.ParseIP(u.Hostname()) != nil {
		return nil, ErrPasskeyNeedsDomain
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: s.displayName,
		RPID:          u.Hostname(), // 不带端口
		RPOrigins:     []string{origin},
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.instances[origin] = wa
	s.mu.Unlock()
	return wa, nil
}

// waUser 把 store.User 适配成 webauthn.User。
type waUser struct {
	id    int64
	email string
	creds []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return []byte(itoa(u.id)) }
func (u *waUser) WebAuthnName() string                       { return u.email }
func (u *waUser) WebAuthnDisplayName() string                { return u.email }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// loadUser 按 uid 取账号与它的全部密钥。
func (s *PasskeyService) loadUser(ctx context.Context, uid int64) (*waUser, *store.User, error) {
	u, err := s.st.UserByID(ctx, uid)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.st.UserPasskeys(ctx, uid)
	if err != nil {
		return nil, nil, err
	}
	creds := make([]webauthn.Credential, 0, len(rows))
	for _, r := range rows {
		creds = append(creds, webauthn.Credential{
			ID:        decodeB64(r.CredentialID),
			PublicKey: r.PublicKey,
			// Flags 必须按原值还原：BE/BS 与认证器报的不一致会被判为
			// "Backup Eligible flag inconsistency" 而登录失败
			Flags: decodeFlags(r.Flags),
			// Authenticator.SignCount 参与「计数器必须递增」的校验
			Authenticator: webauthn.Authenticator{SignCount: r.SignCount},
		})
	}
	return &waUser{id: u.ID, email: u.Email, creds: creds}, u, nil
}

// BeginRegistration 生成注册选项，返回给浏览器的 JSON 与 session id。
func (s *PasskeyService) BeginRegistration(ctx context.Context, uid int64, origin string) (any, string, error) {
	wa, err := s.instance(origin)
	if err != nil {
		return nil, "", err
	}
	user, _, err := s.loadUser(ctx, uid)
	if err != nil {
		return nil, "", err
	}
	// 排除已有密钥：同一把 authenticator 重复注册会被浏览器拒绝，提前告知更友好
	exclusions := make([]protocol.CredentialDescriptor, 0, len(user.creds))
	for _, c := range user.creds {
		exclusions = append(exclusions, protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: c.ID,
		})
	}
	creation, session, err := wa.BeginRegistration(user,
		webauthn.WithExclusions(exclusions),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
	)
	if err != nil {
		return nil, "", err
	}
	sid := s.putSession(session)
	// 只返回 .Response：CredentialCreation 外层还包了一层 {publicKey: ...}，
	// 前端要的正是里面那层，多包一层会让 navigator.credentials 拿到 undefined 的 challenge
	return creation.Response, sid, nil
}

// FinishRegistration 校验注册响应并落库。
func (s *PasskeyService) FinishRegistration(ctx context.Context, uid int64, sid, origin string, body io.Reader) (*store.Passkey, error) {
	wa, err := s.instance(origin)
	if err != nil {
		return nil, err
	}
	session, ok := s.takeSession(sid)
	if !ok {
		// 最常见的原因：challenge 过期（5 分钟）或服务重启导致内存里的会话没了
		slog.Warn("passkey register: session missing or expired", "uid", uid)
		return nil, ErrPasskeyFailed
	}
	user, u, err := s.loadUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(body)
	if err != nil {
		slog.Warn("passkey register: parse response failed", "uid", uid, "err", err)
		return nil, ErrPasskeyFailed
	}
	cred, err := wa.CreateCredential(user, *session, parsed)
	if err != nil {
		// 这里是真正的原因所在：RPID 不匹配、origin 不一致、challenge 对不上、
		// attestation 校验失败都在这一句里
		slog.Warn("passkey register: create credential failed", "uid", uid, "err", err)
		return nil, ErrPasskeyFailed
	}
	name := "通行密钥"
	if len(u.Email) > 0 {
		name = u.Email
	}
	credID := encodeB64(cred.ID)
	if err := s.st.CreatePasskey(ctx, uid, credID, cred.PublicKey, cred.Authenticator.SignCount, encodeFlags(cred.Flags), "", name); err != nil {
		return nil, err
	}
	rows, err := s.st.ListPasskeys(ctx, uid)
	if err != nil {
		return nil, err
	}
	for _, p := range rows {
		if p.CredentialID == credID {
			return &p, nil
		}
	}
	return nil, nil
}

// BeginLogin 生成登录选项。必须带邮箱：无用户名（discoverable）登录要求所有密钥
// 都带 resident key，而这只在注册时明确要求过才成立，不能假设。
func (s *PasskeyService) BeginLogin(ctx context.Context, email, origin string) (any, string, error) {
	wa, err := s.instance(origin)
	if err != nil {
		return nil, "", err
	}
	u, _, err := s.st.UserByEmail(ctx, NormalizeEmail(email))
	if err != nil {
		return nil, "", ErrNoPasskey
	}
	user, _, err := s.loadUser(ctx, u.ID)
	if err != nil {
		return nil, "", err
	}
	if len(user.creds) == 0 {
		return nil, "", ErrNoPasskey
	}
	assertion, session, err := wa.BeginLogin(user)
	if err != nil {
		return nil, "", err
	}
	sid := s.putSession(session)
	return assertion.Response, sid, nil
}

// FinishLogin 校验签名，返回账号 id。
func (s *PasskeyService) FinishLogin(ctx context.Context, sid, origin string, body io.Reader) (int64, error) {
	wa, err := s.instance(origin)
	if err != nil {
		return 0, err
	}
	session, ok := s.takeSession(sid)
	if !ok {
		slog.Warn("passkey login: session missing or expired")
		return 0, ErrPasskeyFailed
	}
	// session.UserID 是 BeginLogin 时写进去的 WebAuthnID（十进制 uid）
	uid, err := parseID(session.UserID)
	if err != nil {
		slog.Warn("passkey login: bad user id in session", "err", err)
		return 0, ErrPasskeyFailed
	}
	user, _, err := s.loadUser(ctx, uid)
	if err != nil {
		return 0, ErrPasskeyFailed
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(body)
	if err != nil {
		slog.Warn("passkey login: parse response failed", "uid", uid, "err", err)
		return 0, ErrPasskeyFailed
	}
	cred, err := wa.ValidateLogin(user, *session, parsed)
	if err != nil {
		slog.Warn("passkey login: validate failed", "uid", uid, "err", err)
		return 0, ErrPasskeyFailed
	}
	// 计数器回写：防止同一份签名被重放（认证器每次递增，服务端必须记住上一次的值）
	if pk, err := s.st.PasskeyByCredID(ctx, encodeB64(cred.ID)); err == nil {
		_ = s.st.TouchPasskey(ctx, pk.ID, cred.Authenticator.SignCount)
	}
	return uid, nil
}

// HasPasskeys 账号是否登记过密钥。
func (s *PasskeyService) HasPasskeys(ctx context.Context, uid int64) bool {
	n, err := s.st.CountPasskeys(ctx, uid)
	return err == nil && n > 0
}

// ---- 会话表 ----

func (s *PasskeyService) putSession(d *webauthn.SessionData) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	sid := hex.EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	// 顺手清过期项：量级很小（几分钟内几十条），不必单独起清理协程
	now := time.Now()
	for k, v := range s.sess {
		if v.exp.Before(now) {
			delete(s.sess, k)
		}
	}
	s.sess[sid] = passkeySession{data: d, exp: now.Add(passkeyTTL)}
	return sid
}

func (s *PasskeyService) takeSession(sid string) (*webauthn.SessionData, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sess[sid]
	if !ok || e.exp.Before(time.Now()) {
		delete(s.sess, sid)
		return nil, false
	}
	delete(s.sess, sid) // 一次性：同一 challenge 不能用于第二次校验
	return e.data, true
}

func parseID(b []byte) (int64, error) {
	var n int64
	if len(b) == 0 || len(b) > 18 {
		return 0, ErrPasskeyFailed
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, ErrPasskeyFailed
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// encodeFlags 把认证器标志位压成一个字节。位定义与 decodeFlags 一一对应。
func encodeFlags(f webauthn.CredentialFlags) uint8 {
	var v uint8
	if f.UserPresent {
		v |= 1
	}
	if f.UserVerified {
		v |= 2
	}
	if f.BackupEligible {
		v |= 4
	}
	if f.BackupState {
		v |= 8
	}
	return v
}

func decodeFlags(v uint8) webauthn.CredentialFlags {
	return webauthn.CredentialFlags{
		UserPresent:    v&1 != 0,
		UserVerified:   v&2 != 0,
		BackupEligible: v&4 != 0,
		BackupState:    v&8 != 0,
	}
}

func encodeB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func decodeB64(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}
