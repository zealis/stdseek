// Package objstore 提供 S3 兼容对象存储的最小客户端：Put / Get / Range / Head / Delete。
//
// 为什么不用 AWS SDK：这里只需要四个 HTTP 动词，SDK 会带进十几 MB 依赖与
// 一整套凭据链（环境变量、profile、IMDS、SSO…），对一个单二进制项目是负担。
// SigV4 只有几十行，自己实现反而能把「只签名实际发出的头」这件事钉死。
//
// 刻意不做的事：
//   - 不做 List：按前缀列举是「小文件问题」的放大器，归档一律靠 manifest；
//   - 不跟随重定向：301/307 通常表示端点或区域填错，跟过去会丢掉签名，
//     最后只看到一句 SignatureDoesNotMatch；
//   - 不重试：上层（冷备）按整包重跑，逐请求重试只会把一次失败拖成几分钟。
package objstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ErrNotFound 对象不存在（HTTP 404）。调用方用它区分「还没归档」与「真的出错」。
var ErrNotFound = errors.New("对象不存在")

// Config S3 兼容存储（Backblaze B2 / Cloudflare R2 / MinIO）的连接配置。
type Config struct {
	Endpoint  string // 例：https://s3.us-west-004.backblazeb2.com
	Region    string // B2：us-west-004；R2：auto
	Bucket    string
	AccessKey string
	SecretKey string
	Prefix    string // 桶内前缀，空表示桶根
	PathStyle bool   // true：endpoint/bucket；false：bucket.endpoint
	// NoProxy 直连，不走系统代理（HTTPS_PROXY）。
	// 为什么需要：大文件上传走代理会慢一个数量级，而代理是给浏览器/模型接口配的，
	// 对象存储往往该直连；把它做成开关而不是写死，是因为有的网络只有代理能出去。
	NoProxy bool
}

// Validate 校验发起请求所必需的字段。
func (c Config) Validate() error {
	if c.Endpoint == "" {
		return errors.New("缺少接口地址")
	}
	u, err := url.Parse(strings.TrimRight(c.Endpoint, "/"))
	if err != nil || u.Host == "" {
		return errors.New("接口地址不是合法 URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("接口地址必须是 http(s)")
	}
	if c.Bucket == "" {
		return errors.New("缺少桶名")
	}
	if c.Region == "" {
		return errors.New("缺少区域")
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return errors.New("缺少 Access Key 或 Secret")
	}
	return nil
}

// ObjectKey 把相对路径拼到配置前缀下。rel 用 / 分隔，不要以 / 开头。
func (c Config) ObjectKey(rel string) string {
	p := strings.Trim(c.Prefix, "/")
	rel = strings.TrimLeft(rel, "/")
	switch {
	case p == "":
		return rel
	case rel == "":
		return p
	}
	return p + "/" + rel
}

// Client 绑定一组配置的客户端。并发安全（http.Client 本身安全，配置只读）。
type Client struct {
	cfg Config
	hc  *http.Client
}

// New 创建客户端；timeout<=0 时用 60 秒（上传 128MB 包也够）。
func New(cfg Config, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.NoProxy {
		tr.Proxy = nil
	}
	return &Client{cfg: cfg, hc: &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Config 返回绑定时的配置（只读用途）。
func (c *Client) Config() Config { return c.cfg }

// urlFor 拼出对象的最终 URL 与 Host（Host 要参与签名）。
func (c *Client) urlFor(key string) (string, string, error) {
	u, err := url.Parse(strings.TrimRight(c.cfg.Endpoint, "/"))
	if err != nil || u.Host == "" {
		return "", "", errors.New("接口地址不是合法 URL")
	}
	key = strings.TrimLeft(key, "/")
	if c.cfg.PathStyle {
		u.Path = "/" + c.cfg.Bucket
		if key != "" {
			u.Path += "/" + key
		}
	} else {
		u.Host = c.cfg.Bucket + "." + u.Host
		u.Path = "/" + key
	}
	return u.String(), u.Host, nil
}

// do 发一个已签名的请求。body 为 nil 时不带负载。
func (c *Client) do(ctx context.Context, method, key string, body io.Reader, size int64, payloadHash string, hdr map[string]string) (*http.Response, error) {
	raw, host, err := c.urlFor(key)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, raw, body)
	if err != nil {
		return nil, err
	}
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if size >= 0 && body != nil {
		req.ContentLength = size
	}
	c.sign(req, payloadHash)
	return c.hc.Do(req)
}

// check 把非 2xx 响应变成可读错误；404 统一映射为 ErrNotFound。
func check(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusForbidden:
		return errors.New("认证失败或没有桶权限（HTTP 403）")
	case http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return fmt.Errorf("端点或区域不匹配（HTTP %d，Location: %s）", resp.StatusCode, resp.Header.Get("Location"))
	}
	msg := strings.TrimSpace(string(body))
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("HTTP %d：%s", resp.StatusCode, msg)
}

// HeadBucket 探测桶是否存在、凭据是否有权限（后台「测试连接」用）。
func (c *Client) HeadBucket(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodHead, "", nil, -1, sha256Hex(nil), nil)
	if err != nil {
		return fmt.Errorf("连接失败：%w", err)
	}
	defer resp.Body.Close()
	if err := check(resp); err != nil {
		if errors.Is(err, ErrNotFound) {
			return errors.New("桶不存在（HTTP 404）")
		}
		return err
	}
	return nil
}

// Probe 用一次对象 HEAD 判断「凭据能不能用」。
//
// 为什么需要它：Backblaze B2 的桶级应用密钥没有列桶权限，
// 对 HeadBucket 可能直接回 403，但那不代表凭据坏了——它只是不被允许
// 看「桶存在吗」。HEAD 一个对象时：200=有这对象、404=没有但凭据有效，
// 两种都说明签名与密钥没问题。
func (c *Client) Probe(ctx context.Context, key string) error {
	resp, err := c.do(ctx, http.MethodHead, key, nil, -1, sha256Hex(nil), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return check(resp)
}

// PutBytes 上传一段内存数据。
func (c *Client) PutBytes(ctx context.Context, key string, data []byte, contentType string) error {
	hdr := map[string]string{}
	if contentType != "" {
		hdr["Content-Type"] = contentType
	}
	resp, err := c.do(ctx, http.MethodPut, key, bytes.NewReader(data), int64(len(data)), sha256Hex(data), hdr)
	if err != nil {
		return fmt.Errorf("上传 %s 失败：%w", key, err)
	}
	defer resp.Body.Close()
	return check(resp)
}

// PutFile 上传本地文件。
//
// 先读一遍算 sha256 再发：SigV4 要求签名覆盖负载哈希，
// 而 128MB 的包不值得为了省一遍读就把文件整个读进内存。
func (c *Client) PutFile(ctx context.Context, key, path, contentType string) (int64, error) {
	return c.PutFileProgress(ctx, key, path, contentType, nil)
}

// PutFileProgress 同上，但每传完约 1MB 回调一次进度。
// 34MB 从国内传到美西要好几分钟，界面上没有数字就一定会被当成卡死。
func (c *Client) PutFileProgress(ctx context.Context, key, path, contentType string, onProgress func(sent, total int64)) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return 0, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	hdr := map[string]string{}
	if contentType != "" {
		hdr["Content-Type"] = contentType
	}
	var body io.Reader = f
	if onProgress != nil {
		body = &progressReader{r: f, total: size, fn: onProgress, step: 1 << 20}
	}
	resp, err := c.do(ctx, http.MethodPut, key, body, size, hex.EncodeToString(h.Sum(nil)), hdr)
	if err != nil {
		return 0, fmt.Errorf("上传 %s 失败：%w", key, err)
	}
	defer resp.Body.Close()
	if err := check(resp); err != nil {
		return 0, fmt.Errorf("上传 %s 失败：%w", key, err)
	}
	return size, nil
}

// Get 读取整个对象。
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, -1, sha256Hex(nil), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := check(resp); err != nil {
		return nil, err
	}
	return io.ReadAll(resp.Body)
}

// GetRange 读取对象的 [off, off+length) 区间。
// 冷备的回源读取只走这条路径：拿到 manifest 里的偏移就能只取一页，
// 不必把 128MB 的包整个拉下来。
func (c *Client) GetRange(ctx context.Context, key string, off, length int64) ([]byte, error) {
	if off < 0 || length <= 0 {
		return nil, errors.New("范围不合法")
	}
	hdr := map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", off, off+length-1)}
	resp, err := c.do(ctx, http.MethodGet, key, nil, -1, sha256Hex(nil), hdr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := check(resp); err != nil {
		return nil, err
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if int64(len(out)) < length {
		return nil, fmt.Errorf("范围读返回 %d 字节，期望 %d", len(out), length)
	}
	return out, nil
}

// Head 返回对象字节数（不存在时 ErrNotFound）。
func (c *Client) Head(ctx context.Context, key string) (int64, error) {
	resp, err := c.do(ctx, http.MethodHead, key, nil, -1, sha256Hex(nil), nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := check(resp); err != nil {
		return 0, err
	}
	return resp.ContentLength, nil
}

// Delete 删除对象；不存在也算成功（冷备重跑是幂等的）。
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, http.MethodDelete, key, nil, -1, sha256Hex(nil), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := check(resp); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// sign 计算 AWS Signature Version 4。
// 只签 host、x-amz-content-sha256、x-amz-date：S3 允许未签名的其他头，
// 少签一个头就少一类「本地能跑、线上 403」的原因。
func (c *Client) sign(req *http.Request, payloadHash string) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := req.URL.Query().Encode()
	canonicalHeaders := "host:" + req.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		req.Method, canonicalURI, canonicalQuery, canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")
	scope := date + "/" + c.cfg.Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonicalRequest))

	kDate := hmacSHA256([]byte("AWS4"+c.cfg.SecretKey), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(c.cfg.Region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	sig := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))
	req.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential="+c.cfg.AccessKey+"/"+scope+
			", SignedHeaders="+signedHeaders+", Signature="+sig)
}

// progressReader 统计已交给连接的字节数。注意这是「写进 socket」的量，
// 不是对端已确认的量；用来判断「还在动」足够了。
type progressReader struct {
	r     io.Reader
	sent  int64
	total int64
	last  int64
	step  int64
	fn    func(sent, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.sent += int64(n)
	if p.fn != nil && p.step > 0 && p.sent-p.last >= p.step {
		p.last = p.sent
		p.fn(p.sent, p.total)
	}
	return n, err
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
