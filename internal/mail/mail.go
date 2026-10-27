// Package mail 发信。只做一件事：把一段纯文本交给 SMTP 服务器。
//
// 没配 SMTP 时不报错，而是把正文打到日志里——本地开发要能拿到验证/重置链接，
// 否则「注册完收不到邮件」会变成死路。生产环境配了 SMTP 就不会走这条路径。
package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"log/slog"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config SMTP 配置。
type Config struct {
	Host   string
	Port   int
	User   string
	Pass   string
	From   string // 留空时用 User
	Secure bool   // true 走隐式 TLS（465），false 走 STARTTLS（587/25）
}

// Mailer 发信器。配置可在运行时替换（后台改完即时生效，不用重启）。
type Mailer struct {
	mu  sync.RWMutex
	cfg Config
}

// New 构造。Host 为空时该实例处于「未配置」状态，Send 只写日志。
func New(cfg Config) *Mailer {
	m := &Mailer{}
	m.Reload(cfg)
	return m
}

// Reload 替换配置。后台保存 SMTP 设置时调用。
func (m *Mailer) Reload(cfg Config) {
	if cfg.Port == 0 {
		if cfg.Secure {
			cfg.Port = 465
		} else {
			cfg.Port = 587
		}
	}
	if cfg.From == "" {
		cfg.From = cfg.User
	}
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
}

// Config 当前配置的副本（secret 不在这里脱敏，调用方自己注意）。
func (m *Mailer) Config() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Enabled 是否真的会发信。
func (m *Mailer) Enabled() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return strings.TrimSpace(m.cfg.Host) != ""
}

// Send 发一封纯文本邮件。body 用 \n 换行，这里统一转成 CRLF（SMTP 要求）。
func (m *Mailer) Send(ctx context.Context, to, subject, body string) error {
	if !m.Enabled() {
		// 开发模式：把正文打进日志。级别用 Info 而不是 Debug，
		// 因为默认 info 级别下要能直接看到链接。
		slog.Warn("SMTP 未配置，邮件未发送（以下内容仅打印到日志）",
			"to", to, "subject", subject)
		slog.Info("mail body", "to", to, "body", body)
		return nil
	}

	cfg := m.Config()
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	var err error
	if cfg.Secure {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()

	if !cfg.Secure {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return err
			}
		}
	}
	if cfg.User != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)); err != nil {
				return err
			}
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(buildMessage(cfg.From, to, subject, body)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// buildMessage 拼 MIME 头。Subject 用 RFC 2047 编码，否则中文主题会变乱码。
func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + encodeHeader(subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("\r\n")
	// SMTP 要求行尾 CRLF；用户给的模板都是 \n
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return []byte(b.String())
}

// encodeHeader RFC 2047 Base64 编码（纯 ASCII 时原样返回）。
func encodeHeader(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7e || s[i] < 0x20 {
			return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
		}
	}
	return s
}
