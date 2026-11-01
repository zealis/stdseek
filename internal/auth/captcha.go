package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// 人机验证：一道算术题，答案用 HMAC 签名随题下发。
//
// 为什么选这个而不是图形验证码：自托管场景没有第三方服务可用，图形验证码要自己
// 画扭曲文字（实现重、无障碍差、还得防 OCR）。算术题挡不住定向攻击，但结合
// 「同 IP 每分钟 120 次」的限流，足够挡住批量注册脚本——这是性价比最高的档位。
// 真要被盯上，正确做法是接 Turnstile 之类的托管方案，而不是把算术题做得更花。
//
// 无状态设计：题目参数与签名一起发给前端，提交时带回来验签。
// 服务端不留 session，重启后旧题目失效（用户刷新一下即可），代价可以接受。

// captchaTTL 题目有效期。5 分钟够填，也不至于让签名长期可重放。
const captchaTTL = 5 * time.Minute

// Captcha 生成与验证。
type Captcha struct {
	secret []byte
}

// NewCaptcha 构造。secret 为进程内随机值：重启后未提交的旧题目失效，
// 换来的是不用把密钥落盘（少一个需要保护的秘密）。
func NewCaptcha() *Captcha {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// rand 失败意味着系统熵源有问题，退化成一个固定串也比 panic 好
		copy(buf, []byte("stdseek-captcha-fallback-secret"))
	}
	return &Captcha{secret: buf}
}

// Challenge 一道题：两个 1~9 的数相加，答案是 2~18。
//
// 范围刻意压小：区号 + 这类两位数题目会让心算成本上升，而防机器人的收益为零
// （脚本不管答案多长都只是多一次加法）。
type Challenge struct {
	A   int    `json:"a"`
	B   int    `json:"b"`
	Exp int64  `json:"exp"`
	Sig string `json:"sig"`
}

// New 生成一道新题。
func (c *Captcha) New() Challenge {
	a := randInt(1, 9)
	b := randInt(1, 9)
	exp := time.Now().Add(captchaTTL).Unix()
	return Challenge{A: a, B: b, Exp: exp, Sig: c.sign(a, b, exp)}
}

// Verify 校验答案。任何一项对不上都返回 false——调用方只需区分「过」与「不过」，
// 不必知道是过期了还是答案错了（那属于给攻击者的额外信息）。
func (c *Captcha) Verify(ch Challenge, answer string) bool {
	if ch.Exp < time.Now().Unix() {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(ch.Sig), []byte(c.sign(ch.A, ch.B, ch.Exp))) != 1 {
		return false
	}
	got, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil {
		return false
	}
	return got == ch.A+ch.B
}

func (c *Captcha) sign(a, b int, exp int64) string {
	mac := hmac.New(sha256.New, c.secret)
	fmt.Fprintf(mac, "%d|%d|%d", a, b, exp)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// randInt 返回 [min, max] 内的随机整数。
func randInt(min, max int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	if err != nil {
		return min
	}
	return min + int(n.Int64())
}
