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
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"voidxo/internal/api"
	"voidxo/internal/crawler"
	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/metrics"
	"voidxo/internal/processor"
	"voidxo/internal/prosperity"
	"voidxo/internal/store"
)

func main() {
	var (
		addr      = flag.String("addr", "127.0.0.1:8080", "HTTP 监听地址（默认仅本机；对外请显式改 :8080 并配置令牌）")
		dataDir   = flag.String("data", "./data", "数据目录（SQLite/Bleve）")
		submitOn  = flag.Bool("submit", false, "是否开放用户提交网站（红线：首批白名单验证前保持关闭）")
		crawlOnce = flag.Bool("crawl-once", false, "启动前执行一次白名单种子抓取后退出")
		prosOnce  = flag.Bool("prosperity-once", false, "执行一次繁荣度统计后退出")
		prosEvery = flag.Duration("prosperity-every", 24*time.Hour, "繁荣度统计周期，0 表示关闭定时任务")
		adminTok  = flag.String("admin-token", "", "后台管理员令牌；留空则每次启动随机生成并打印")
	)
	flag.Parse()

	if err := run(*addr, *dataDir, *submitOn, *crawlOnce, *prosOnce, *prosEvery, *adminTok); err != nil {
		slog.Error("voidxo exited", "err", err)
		os.Exit(1)
	}
}

func run(addr, dataDir string, submitOn, crawlOnce, prosOnce bool, prosEvery time.Duration, adminTok string) error {
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

	idx, err := index.Open(filepath.Join(dataDir, "bleve"))
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
	defer stop()

	proc, err := processor.New()
	if err != nil {
		return err
	}
	cw := crawler.New(st, idx, proc, snap, crawler.DefaultConfig())

	if crawlOnce {
		stats, err := cw.Run(ctx)
		slog.Info("crawl finished", "seeds", stats.Seeds, "fetched", stats.Fetched,
			"indexed", stats.Indexed, "skipped", stats.Skipped, "errors", stats.Errors)
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

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewRouter(api.NewServer(st, idx, snap, submitOn, adminTok)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("voidxo listening", "addr", addr, "submit_enabled", submitOn)
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
