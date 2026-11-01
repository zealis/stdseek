package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zealis/stdseek/internal/objstore"
	"github.com/zealis/stdseek/internal/snapshot"
)

// defaultS3Prefix 默认前缀。一个桶装四类数据，靠前缀分开：
// parts/ 原文包、idx/ 偏移量、exports/ 元数据、db/ 整库快照。
// 总前缀不叫 snapshots：数据库备份也在里面，用它会让人以为桶里只有快照。
const defaultS3Prefix = "github.com/zealis/stdseek/"

// 云存储配置：一个私有桶装四类数据（原文包 / manifest / 元数据 / 整库快照），
// 靠前缀分开。不拆两个桶：一套凭据、一次测试、一份配置，运维上少一半出错面。
const (
	settingSnapshotS3Endpoint  = "storage.snapshot.endpoint"
	settingSnapshotS3Bucket    = "storage.snapshot.bucket"
	settingSnapshotS3Region    = "storage.snapshot.region"
	settingSnapshotS3Prefix    = "storage.snapshot.prefix"
	settingSnapshotS3AccessKey = "storage.snapshot.access_key"
	settingSnapshotS3SecretKey = "storage.snapshot.secret_key"
	settingSnapshotS3PathStyle = "storage.snapshot.path_style"
	// 直连对象存储，不走 HTTPS_PROXY。大文件上传经代理会慢一个数量级，
	// 而代理通常是给浏览器/模型接口配的。
	settingSnapshotS3NoProxy = "storage.snapshot.no_proxy"
)

type snapshotStorageView struct {
	// Backend 热层恒为本地（Pebble）；云端只做归档与回源。
	Backend      string `json:"backend"`
	S3Configured bool   `json:"s3_configured"`
	Endpoint     string `json:"endpoint"`
	Bucket       string `json:"bucket"`
	Region       string `json:"region"`
	Prefix       string `json:"prefix"`
	HasAccessKey bool   `json:"has_access_key"`
	HasSecret    bool   `json:"has_secret"`
	PathStyle    bool   `json:"path_style"`
	NoProxy      bool   `json:"no_proxy"`
}

// snapshotS3Config 读出一份完整的存储配置。业务逻辑（冷备/导出/测试连接）
// 都从这里取，避免各自拼接字段时漏掉 path_style 之类。
func (s *Server) snapshotS3Config(ctx context.Context) objstore.Config {
	get := func(key, def string) string {
		v, err := s.st.Setting(ctx, key, def)
		if err != nil {
			return def
		}
		return v
	}
	// 前缀按用户填的原样保存与回显（拼对象名时 objstore 会统一去掉首尾斜杠），
	// 这里改写会让人以为「填进去的和显示出来的不是一个东西」。
	prefix := strings.TrimSpace(get(settingSnapshotS3Prefix, defaultS3Prefix))
	return objstore.Config{
		Endpoint:  get(settingSnapshotS3Endpoint, ""),
		Bucket:    get(settingSnapshotS3Bucket, ""),
		Region:    get(settingSnapshotS3Region, ""),
		AccessKey: get(settingSnapshotS3AccessKey, ""),
		SecretKey: get(settingSnapshotS3SecretKey, ""),
		Prefix:    prefix,
		PathStyle: get(settingSnapshotS3PathStyle, "true") != "false",
		NoProxy:   get(settingSnapshotS3NoProxy, "false") == "true",
	}
}

func (s *Server) snapshotStorage(ctx context.Context) snapshotStorageView {
	cfg := s.snapshotS3Config(ctx)
	return snapshotStorageView{
		Backend:      "local",
		S3Configured: cfg.Validate() == nil,
		Endpoint:     cfg.Endpoint,
		Bucket:       cfg.Bucket,
		Region:       cfg.Region,
		Prefix:       cfg.Prefix,
		HasAccessKey: cfg.AccessKey != "",
		HasSecret:    cfg.SecretKey != "",
		PathStyle:    cfg.PathStyle,
		NoProxy:      cfg.NoProxy,
	}
}

// snapshotStorageReq 保存与测试共用。密钥留空表示不修改。
type snapshotStorageReq struct {
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	Prefix    string `json:"prefix"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	PathStyle bool   `json:"path_style"`
	NoProxy   bool   `json:"no_proxy"`
}

func (r *snapshotStorageReq) clean() {
	r.Endpoint = strings.TrimSpace(r.Endpoint)
	r.Bucket = strings.TrimSpace(r.Bucket)
	r.Region = strings.TrimSpace(r.Region)
	r.Prefix = strings.Trim(strings.TrimSpace(r.Prefix), "/")
	r.AccessKey = strings.TrimSpace(r.AccessKey)
	r.SecretKey = strings.TrimSpace(r.SecretKey)
	if r.Prefix == "" {
		r.Prefix = strings.Trim(defaultS3Prefix, "/")
	}
	r.Prefix += "/"
}

func (r snapshotStorageReq) validateEndpoint() error {
	if r.Endpoint == "" {
		return nil
	}
	u, err := url.Parse(r.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errBadStorageEndpoint
	}
	return nil
}

var errBadStorageEndpoint = errors.New("接口地址必须是 http(s) URL")

// handleSetSnapshotStorage POST /admin/snapshot/storage
func (s *Server) handleSetSnapshotStorage(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req snapshotStorageReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	req.clean()
	if err := req.validateEndpoint(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	write := func(key, val string) bool {
		if err := s.st.SetSetting(ctx, key, val); err != nil {
			writeErr(w, http.StatusInternalServerError, "写入存储配置失败")
			return false
		}
		return true
	}
	if !write(settingSnapshotS3Endpoint, req.Endpoint) ||
		!write(settingSnapshotS3Bucket, req.Bucket) ||
		!write(settingSnapshotS3Region, req.Region) ||
		!write(settingSnapshotS3Prefix, req.Prefix) ||
		!write(settingSnapshotS3PathStyle, boolStr(req.PathStyle)) ||
		!write(settingSnapshotS3NoProxy, boolStr(req.NoProxy)) {
		return
	}
	// 凭据留空 = 保留旧值；页面拿不到明文，不能要求每次重填。
	if req.AccessKey != "" && !write(settingSnapshotS3AccessKey, req.AccessKey) {
		return
	}
	if req.SecretKey != "" && !write(settingSnapshotS3SecretKey, req.SecretKey) {
		return
	}
	slog.Info("snapshot storage config saved", "backend", "local", "s3_configured",
		s.snapshotStorage(ctx).S3Configured)
	// 配置变了就丢掉旧的读取器：地址/桶换了还连旧桶，会读不到刚归档的页。
	s.readerMu.Lock()
	s.reader = nil
	s.readerMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "storage": s.snapshotStorage(ctx)})
}

// handleTestSnapshotStorage POST /admin/snapshot/storage/test
// 未保存的字段以请求为准；留空的密钥用已保存值，便于「改地址后先测再存」。
func (s *Server) handleTestSnapshotStorage(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req snapshotStorageReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	req.clean()
	if err := req.validateEndpoint(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	access := req.AccessKey
	if access == "" {
		access, _ = s.st.Setting(ctx, settingSnapshotS3AccessKey, "")
	}
	secret := req.SecretKey
	if secret == "" {
		secret, _ = s.st.Setting(ctx, settingSnapshotS3SecretKey, "")
	}
	cfg := snapshot.S3Config{
		Endpoint: req.Endpoint, Bucket: req.Bucket, Region: req.Region,
		AccessKey: access, SecretKey: secret, Prefix: req.Prefix, PathStyle: req.PathStyle,
		NoProxy: req.NoProxy,
	}
	if err := cfg.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "配置不完整："+err.Error())
		return
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := snapshot.TestS3(tctx, cfg, 15*time.Second); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "连接成功"})
}
