// voidxo（维空）— Go 垂直搜索引擎单二进制入口。
//
// 骨架阶段装配：SQLite 元数据 + Bleve 索引 + chi HTTP 服务 + 白名单爬虫。
// 设计约束见 规划书/go_search_engine_plan.md：单二进制、零外部依赖、白名单策展。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"voidxo/internal/api"
	"voidxo/internal/crawler"
	"voidxo/internal/dedup"
	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/metrics"
	"voidxo/internal/processor"
	"voidxo/internal/prosperity"
	"voidxo/internal/reindex"
	"voidxo/internal/repair"
	"voidxo/internal/auth"
	"voidxo/internal/mail"
	"voidxo/internal/store"
	"voidxo/internal/suggest"
	"voidxo/internal/usage"
	"voidxo/internal/summarize"
)

// 构建期注入：go build -ldflags "-X main.version=1.2.3 -X main.commit=abc1234 -X main.buildTime=2026-10-24T23:00:00Z"
// 自托管部署要能回答「你跑的是哪个构建」，否则线上问题无法复现。
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	var (
		showVersion    = flag.Bool("version", false, "打印版本信息后退出")
		addr           = flag.String("addr", "127.0.0.1:8080", "HTTP 监听地址（默认仅本机；对外请显式改 :8080 并配置令牌）")
		dataDir        = flag.String("data", "./data", "数据目录（SQLite/Bleve）")
		submitOn       = flag.Bool("submit", true, "是否开放用户提交网站（默认开放；提交进审核队列 + 每分钟 120 次限流，关闭用 -submit=false）")
		crawlOnce      = flag.Bool("crawl-once", false, "启动前执行一次白名单种子抓取后退出")
		prosOnce       = flag.Bool("prosperity-once", false, "执行一次繁荣度统计后退出")
		prosEvery      = flag.Duration("prosperity-every", 24*time.Hour, "繁荣度统计周期，0 表示关闭定时任务")
		adminTok       = flag.String("admin-token", "", "后台管理员令牌；留空则每次启动随机生成并打印")
		reindexOn      = flag.Bool("reindex", false, "从 KV 快照重建索引后退出（需先停止服务）")
		reindexWorkers = flag.Int("reindex-workers", 0, "reindex 的 processor 并行度，0=CPU 核数（索引/SQLite 写入仍单点）")
		dedupeOnce     = flag.Bool("dedupe", false, "执行一次内容去重（折叠近重复文档）后退出")
		repairOnce     = flag.Bool("repair", false, "补齐索引：表里有、索引里缺的文档从 KV 快照补回（reindex 中断恢复用）后退出")
		crawlEvery     = flag.Duration("crawl-every", time.Hour, "定时抓取周期（按 crawl_interval 只抓到期站点；0 关闭）")
		logLevel       = flag.String("log-level", "info", "日志级别：info（默认）/ debug（逐页访问与过滤原因）")
		crawlOnly      = flag.String("crawl-only", "", "配合 -crawl-once：只抓指定域名（单站调试/补抓）")
		crawlPages     = flag.Int("crawl-pages", 25, "单站单次最多抓取页数（默认 25；扩语料时调大，如 80）")
		llmBase        = flag.String("llm-base-url", "", "摘要模型接口（OpenAI 兼容，如 https://api.deepseek.com/v1）；留空用抽取式摘要")
		llmModel       = flag.String("llm-model", "", "摘要模型名，如 deepseek-chat；与 -llm-base-url 同时配置才启用")
		llmKey         = flag.String("llm-api-key", "", "摘要模型 API Key；建议改用环境变量 VOIDXO_LLM_KEY，避免出现在进程列表里")
		llmTimeout     = flag.Duration("llm-timeout", 20*time.Second, "单次摘要模型调用超时")
		corsOrigins    = flag.String("cors", "", "允许跨域调用 /api 的来源，逗号分隔（* 为全部）；留空表示不开放")
		geoHeader      = flag.String("geo-header", "", "读取请求地区码的请求头名（如 CF-IPCountry）；留空不统计地区")
		publicURL      = flag.String("public-url", "", "站点公开地址（邮件里的链接用，如 https://voidxo.com）；留空从请求推断")
		resetPwdFor    = flag.String("reset-password", "", "重置指定邮箱的登录密码后退出（配合 -new-password；SMTP 没配时的兜底）")
		newPassword    = flag.String("new-password", "", "配合 -reset-password：新的密码")
		resetTOTPFor   = flag.String("reset-totp", "", "关闭指定邮箱的两步验证后退出（换手机/丢了验证器时用）")
		verifyEmailFor = flag.String("verify-email", "", "把指定邮箱标记为已验证后退出（收不到验证邮件时用）")
		tsSiteKey      = flag.String("turnstile-site-key", "", "Cloudflare Turnstile 的 site key（公开）；配了就用它替代内置算术题")
		tsSecret       = flag.String("turnstile-secret", "", "Turnstile 的 secret key；建议用环境变量 VOIDXO_TURNSTILE_SECRET")
		smtpHost       = flag.String("smtp-host", "", "SMTP 服务器（如 smtp.qq.com）；留空则验证/重置邮件只打印到日志")
		smtpPort       = flag.Int("smtp-port", 0, "SMTP 端口（默认 587；配 -smtp-tls 时默认 465）")
		smtpUser       = flag.String("smtp-user", "", "SMTP 用户名")
		smtpPass       = flag.String("smtp-pass", "", "SMTP 密码；建议改用环境变量 VOIDXO_SMTP_PASS")
		smtpFrom       = flag.String("smtp-from", "", "发件人地址；留空用 -smtp-user")
		smtpTLS        = flag.Bool("smtp-tls", false, "SMTP 走隐式 TLS（465 端口）")
	)
	flag.Parse()
	if *showVersion {
		fmt.Printf("voidxo %s (commit %s, built %s, %s/%s)\n",
			version, commit, buildTime, runtime.GOOS, runtime.GOARCH)
		return
	}
	if strings.EqualFold(*logLevel, "debug") {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	// Key 优先取环境变量：命令行参数会出现在 ps/tasklist 里，等于把密钥公开。
	llmAPIKey := *llmKey
	if llmAPIKey == "" {
		llmAPIKey = os.Getenv("VOIDXO_LLM_KEY")
	}
	// SMTP 密码同理：命令行参数会出现在进程列表里
	smtpPassword := *smtpPass
	if smtpPassword == "" {
		smtpPassword = os.Getenv("VOIDXO_SMTP_PASS")
	}
	// Turnstile 的 secret 同理，只从命令行读会被别人在进程列表里看见
	turnstileSecret := *tsSecret
	if turnstileSecret == "" {
		turnstileSecret = os.Getenv("VOIDXO_TURNSTILE_SECRET")
	}

	// 运维命令：改密码 / 关两步验证。走服务启动之前，不需要 HTTP 服务在跑。
	// 存在意义：SMTP 没配时「找回密码」发不出信、验证器丢了又进不了账号，
	// 这时候能登服务器的人必须有一条路。
	if *resetPwdFor != "" || *resetTOTPFor != "" || *verifyEmailFor != "" {
		if err := adminFix(*dataDir, *resetPwdFor, *newPassword, *resetTOTPFor, *verifyEmailFor); err != nil {
			slog.Error("admin fix failed", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(*addr, *dataDir, *submitOn, *crawlOnce, *prosOnce, *prosEvery, *adminTok, *reindexOn, *reindexWorkers, *dedupeOnce, *repairOnce, *crawlEvery, *crawlOnly, *crawlPages,
		summarize.Config{BaseURL: *llmBase, APIKey: llmAPIKey, Model: *llmModel, Timeout: *llmTimeout},
		apiOptions{
			CORSOrigins: splitOrigins(*corsOrigins),
			GeoHeader:   strings.TrimSpace(*geoHeader),
			PublicURL:   strings.TrimSpace(*publicURL),
			SMTP: mail.Config{
				Host: *smtpHost, Port: *smtpPort, User: *smtpUser,
				Pass: smtpPassword, From: *smtpFrom, Secure: *smtpTLS,
			},
			TurnstileSiteKey: strings.TrimSpace(*tsSiteKey),
			TurnstileSecret:  strings.TrimSpace(turnstileSecret),
		}); err != nil {
		slog.Error("voidxo exited", "err", err)
		os.Exit(1)
	}
}

// splitOrigins 解析 -cors：逗号分隔，去掉空白项。
func splitOrigins(raw string) []string {
	var out []string
	for _, o := range strings.Split(raw, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// apiOptions 启动期传给 HTTP 层的配置。
// 收成结构体是因为 run 的参数已经有十几个，再加就没人读得懂了。
type apiOptions struct {
	CORSOrigins      []string
	GeoHeader        string
	PublicURL        string
	SMTP             mail.Config
	TurnstileSiteKey string
	TurnstileSecret  string
}

func run(addr, dataDir string, submitOn, crawlOnce, prosOnce bool, prosEvery time.Duration, adminTok string, reindexOn bool, reindexWorkers int, dedupeOnce, repairOnce bool, crawlEvery time.Duration, crawlOnly string, crawlPages int, llm summarize.Config, apiOpts apiOptions) error {
	if adminTok == "" {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return err
		}
		adminTok = hex.EncodeToString(buf)
		slog.Warn("admin token generated (use -admin-token to pin it)", "token", adminTok)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dataDir, "voidxo.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	idxPath := filepath.Join(dataDir, "bleve")
	if reindexOn {
		// 重建前清空索引：旧版本文档以原始 URL 为 ID，新版以归一化 key 为 ID，
		// 不清空会让同一页面出现两条（真实语料重跑 reindex 时观察到）。
		if err := os.RemoveAll(idxPath); err != nil {
			return fmt.Errorf("clear index before reindex: %w", err)
		}
	}
	idx, err := index.Open(idxPath)
	if err != nil {
		return err
	}
	defer idx.Close()

	if n, err := st.CountDocuments(context.Background()); err == nil {
		metrics.DocumentsTotal.Set(float64(n))
	}

	snap, err := kv.Open(filepath.Join(dataDir, "kv"))
	if err != nil {
		return err
	}
	defer snap.Close()
	metrics.KVBytes.Set(float64(snap.Size()))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// 每日用量：内存累加 + 周期落盘。放这么早是因为 -crawl-once 等分支在
	// HTTP 服务起来之前就会跑完并退出。
	usageTracker := usage.New(st)
	go usageTracker.Run(ctx, 30*time.Second)
	defer stop()

	proc, err := processor.New()
	if err != nil {
		return err
	}
	if reindexOn {
		// 重建前先把元数据表的锚点变体合并，保持与索引层同一口径
		if n, err := st.DedupeDocuments(ctx); err != nil {
			slog.Warn("dedupe documents failed", "err", err)
		} else if n > 0 {
			slog.Info("deduped documents", "removed", n)
		}
		stats, err := reindex.Run(ctx, st, idx, snap, proc, reindexWorkers)
		if err != nil {
			return err
		}
		slog.Info("reindex finished", "scanned", stats.Scanned, "indexed", stats.Indexed,
			"skipped", stats.Skipped, "errors", stats.Errors, "uncovered", stats.Uncovered,
			"orphans", stats.Orphans)
		if stats.Uncovered > 0 {
			slog.Warn("some documents have no KV snapshot and were not rebuilt", "count", stats.Uncovered)
		}
		if stats.Orphans > 0 {
			slog.Info("orphan snapshots skipped (document already removed)", "count", stats.Orphans)
		}
		// 重建会把折叠过的副本重新索引回来，这里再折叠一次保持一致
		ds, err := dedup.Apply(ctx, st, idx)
		if err != nil {
			return fmt.Errorf("dedupe after reindex: %w", err)
		}
		slog.Info("dedupe finished", "clusters", ds.Clusters, "folded", ds.Folded)
		return nil
	}

	if repairOnce {
		rs, err := repair.Run(ctx, st, idx, snap)
		if err != nil {
			return err
		}
		slog.Info("repair finished", "checked", rs.Checked, "present", rs.Present,
			"repaired", rs.Repaired, "no_snapshot", rs.NoSnapshot, "thin", rs.Thin)
		return nil
	}

	if dedupeOnce {
		ds, err := dedup.Apply(ctx, st, idx)
		if err != nil {
			return err
		}
		slog.Info("dedupe finished", "clusters", ds.Clusters, "folded", ds.Folded)
		return nil
	}

	crawlCfg := crawler.DefaultConfig()
	if crawlPages > 0 {
		// 动态配额下它是「单站上限」而不是固定值：繁荣站按比例多抓，
		// 但任何站都不超过这个数。保持 -crawl-pages 的语义是「最多抓多少页」。
		crawlCfg.QuotaMax = crawlPages
	}
	cw := crawler.New(st, idx, proc, snap, crawlCfg)

	// 定时抓取：按 crawl_interval 只抓到期站点，避免无谓重复请求
	if crawlEvery > 0 {
		go func() {
			ticker := time.NewTicker(crawlEvery)
			defer ticker.Stop()
			slog.Info("crawl scheduler started", "every", crawlEvery.String())
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					stats, err := cw.RunDue(ctx)
					if err != nil {
						slog.Error("scheduled crawl failed", "err", err)
						continue
					}
					if stats.Seeds > 0 {
						slog.Info("scheduled crawl finished", "seeds", stats.Seeds, "fetched", stats.Fetched,
							"indexed", stats.Indexed, "skipped", stats.Skipped, "thin", stats.Thin, "listpage", stats.ListPage, "lang", stats.LangSkip, "boilerplate", stats.Boilerplate, "errors", stats.Errors)
						usageTracker.AddCrawl(stats.Fetched, stats.Indexed)
						// 抓完立刻折叠新出现的副本：两个站点转载同一篇时，
						// 先入库的那篇是正本，后到的必须在这里被折叠掉。
						if ds, derr := dedup.Apply(ctx, st, idx); derr != nil {
							slog.Warn("dedupe after crawl failed", "err", derr)
						} else if ds.Folded > 0 {
							slog.Info("dedupe after crawl", "clusters", ds.Clusters, "folded", ds.Folded)
						}
					}
				}
			}
		}()
	}

	if crawlOnce {
		seeds, err := cw.LoadSeeds(ctx)
		if err != nil {
			return err
		}
		if crawlOnly != "" {
			var picked []string
			prefix := "https://" + crawlOnly + "/"
			for _, s := range seeds {
				if strings.HasPrefix(s, prefix) {
					picked = append(picked, s)
				}
			}
			if len(picked) == 0 {
				slog.Warn("crawl-only matched no whitelist site", "domain", crawlOnly)
			}
			seeds = picked
		}
		stats, err := cw.CrawlSeeds(ctx, seeds)
		slog.Info("crawl finished", "seeds", stats.Seeds, "fetched", stats.Fetched,
			"indexed", stats.Indexed, "skipped", stats.Skipped, "thin", stats.Thin, "listpage", stats.ListPage, "lang", stats.LangSkip, "boilerplate", stats.Boilerplate, "errors", stats.Errors)
		usageTracker.AddCrawl(stats.Fetched, stats.Indexed)
		_ = usageTracker.Flush(ctx) // 一次性任务：进程马上退出，等不到定时落盘
		return err
	}

	if prosOnce {
		ps, err := prosperity.Refresh(ctx, st)
		if err != nil {
			return err
		}
		metrics.ProsperityPairs.Set(float64(ps.Pairs))
		metrics.ProsperityKeys.Set(float64(ps.Keys))
		slog.Info("prosperity refreshed", "pairs", ps.Pairs, "keys", ps.Keys, "max_ref", ps.MaxRef)
		return nil
	}

	// 定时繁荣度统计（默认每天一次，与 sese-engine 夜间任务对齐）
	if prosEvery > 0 {
		go func() {
			ticker := time.NewTicker(prosEvery)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					ps, err := prosperity.Refresh(ctx, st)
					if err != nil {
						slog.Error("prosperity refresh failed", "err", err)
						continue
					}
					metrics.ProsperityPairs.Set(float64(ps.Pairs))
					metrics.ProsperityKeys.Set(float64(ps.Keys))
					slog.Info("prosperity refreshed", "pairs", ps.Pairs, "keys", ps.Keys, "max_ref", ps.MaxRef)
				}
			}
		}()
	}

	apiSrv := api.NewServer(st, idx, snap, submitOn, adminTok)
	apiSrv.Usage = usageTracker
	// 账号体系：注册/登录/找回密码。SMTP 没配时邮件只打印到日志。
	apiSrv.Auth = auth.New(st)
	apiSrv.Captcha = auth.NewCaptcha()
	// 配了 Turnstile 就用它（更可靠），没配则回退内置算术题
	if apiOpts.TurnstileSecret != "" && apiOpts.TurnstileSiteKey != "" {
		apiSrv.Turnstile = auth.NewTurnstile(apiOpts.TurnstileSecret)
		apiSrv.TurnstileSiteKey = apiOpts.TurnstileSiteKey
		slog.Info("Turnstile enabled for auth")
	}
	apiSrv.Passkey = auth.NewPasskey(st, "voidxo 维空")
	// 归属地接口默认用 One IP（无需 key），后台可改成任意含 {ip} 的接口
	geoAPI, _ := st.GetSetting(ctx, "geo.api")
	if geoAPI == "" {
		geoAPI = "https://ip.huzhihui.com/api/ip/health?ip={ip}"
		_ = st.SetSetting(ctx, "geo.api", geoAPI)
	}
	apiSrv.Geo = auth.NewGeoResolver(geoAPI)
	apiSrv.Mail = mail.New(apiOpts.SMTP)
	// SMTP 以后台配置为准：DB 里有就用 DB 的，没有则把启动参数落库一次。
	// 这样「第一次部署用参数、之后在后台改」两条路都成立。
	if raw, _ := st.GetSetting(ctx, "mail.config"); raw != "" {
		var cfg mail.Config
		if json.Unmarshal([]byte(raw), &cfg) == nil && cfg.Host != "" {
			apiSrv.Mail.Reload(cfg)
		}
	} else if apiOpts.SMTP.Host != "" {
		if raw, err := json.Marshal(apiOpts.SMTP); err == nil {
			_ = st.SetSetting(ctx, "mail.config", string(raw))
		}
	}
	apiSrv.PublicURL = apiOpts.PublicURL
	// 过期会话启动时清一次，量级很小，不值得后台定时任务
	_ = st.PurgeExpiredSessions(ctx)
	if apiSrv.Mail.Enabled() {
		slog.Info("SMTP enabled", "host", apiOpts.SMTP.Host)
	} else {
		slog.Warn("SMTP 未配置：邮箱验证与重置密码的邮件只会打印到日志（-smtp-host 可开启）")
	}
	// 搜索词补全：服务端代理 Bing 的 JSONP 接口（见 internal/suggest 的说明）
	apiSrv.Suggester = suggest.NewBing()
	// 跨域：留空表示只允许同源页面调用（默认）。要嵌到别人的站点再用 -cors 显式放开。
	if len(apiOpts.CORSOrigins) > 0 {
		apiSrv.AllowedOrigins = apiOpts.CORSOrigins
		slog.Info("CORS enabled", "origins", apiOpts.CORSOrigins)
	}
	// 地区统计：只有部署在可信 CDN 后面才该配，否则谁都能伪造这个头。
	if apiOpts.GeoHeader != "" {
		apiSrv.GeoHeader = apiOpts.GeoHeader
		slog.Info("geo header enabled", "header", apiOpts.GeoHeader)
	}
	// 结果卡「这页没抓过」的按需补抓：走爬虫的单页路径（只抓这一页，不扩链）。
	// 接口层还会再做一次白名单/黑名单校验与同 URL 冷却。
	apiSrv.FetchPage = func(ctx context.Context, rawURL string) (int, error) {
		stats, err := cw.FetchPage(ctx, rawURL)
		if err != nil {
			return stats.Indexed, err
		}
		return stats.Indexed, nil
	}
	apiSrv.SetSummarizer(summarize.NewProvider(llm))
	if llm.BaseURL != "" && llm.Model != "" {
		slog.Info("summarizer configured", "provider", "openai-compat", "model", llm.Model, "base", llm.BaseURL)
	} else {
		slog.Info("summarizer configured", "provider", "extractive",
			"note", "配置 -llm-base-url 与 -llm-model 可启用模型摘要")
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewRouter(apiSrv),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		// 开关状态打进启动日志：原文摘要这类"要不要花钱调模型"的开关，
		// 排查时第一反应是看服务起来时到底是开还是关，不必先进后台。
		slog.Info("voidxo listening", "addr", addr, "submit_enabled", submitOn,
			"features", apiSrv.FeatureSnapshot(ctx))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// adminFix 直接在数据库上修账号，绕过 HTTP 与验证码。
//
// 只做两件「否则用户永远进不去」的事：重设密码、关两步验证。
// 都要人站在服务器前执行，所以不需要额外的鉴权。
func adminFix(dataDir, email, newPw, resetTOTPEmail, verifyEmail string) error {
	st, err := store.Open(filepath.Join(dataDir, "voidxo.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()

	if resetTOTPEmail != "" {
		u, _, err := st.UserByEmail(ctx, auth.NormalizeEmail(resetTOTPEmail))
		if err != nil {
			return fmt.Errorf("找不到账号 %s: %w", resetTOTPEmail, err)
		}
		if err := st.SetTOTP(ctx, u.ID, "", false); err != nil {
			return err
		}
		if err := st.DeleteUserSessions(ctx, u.ID); err != nil {
			return err
		}
		fmt.Printf("已关闭 %s 的两步验证，并清空该账号的登录会话\n", u.Email)
	}

	if verifyEmail != "" {
		u, _, err := st.UserByEmail(ctx, auth.NormalizeEmail(verifyEmail))
		if err != nil {
			return fmt.Errorf("找不到账号 %s: %w", verifyEmail, err)
		}
		if err := st.MarkEmailVerified(ctx, u.ID); err != nil {
			return err
		}
		fmt.Printf("已把 %s 标记为邮箱已验证\n", u.Email)
	}

	if email != "" {
		if newPw == "" {
			return fmt.Errorf("重置密码需要同时提供 -new-password")
		}
		hash, err := auth.HashPassword(newPw)
		if err != nil {
			return err
		}
		u, _, err := st.UserByEmail(ctx, auth.NormalizeEmail(email))
		if err != nil {
			return fmt.Errorf("找不到账号 %s: %w", email, err)
		}
		if err := st.SetPassword(ctx, u.ID, hash); err != nil {
			return err
		}
		if err := st.DeleteUserSessions(ctx, u.ID); err != nil {
			return err
		}
		fmt.Printf("已重置 %s 的密码，并清空该账号的登录会话\n", u.Email)
	}
	return nil
}
