package api

// 云冷备：把本地热层（原文快照 + 文档元数据）按包推到对象存储。
//
// 三个动作都在后台：开关（要不要往外发）、立即冷备（现在跑一轮）、
// 导出文档（元数据另存一份）。定时任务见 StartBackupLoop。
//
// 为什么默认关：上传意味着数据离开这台机器，开启必须是管理员明确的选择，
// 不能因为填了桶地址就自动开始。

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"voidxo/internal/backup"
	"voidxo/internal/objstore"
)

const (
	// backupPartMinMB / backupPartMaxMB 单包大小的可调范围。
	// 下限 16MB 是为了「别把包切成小文件」——那正是对象存储最怕的形态；
	// 上限 512MB 是单次 PUT 失败重传的代价上限。
	backupPartMinMB = 16
	backupPartMaxMB = 512
	// backupRunTimeout 单轮冷备的超时。首次全量归档可能要跑很久，
	// 但也不能无限等：包是幂等的，超时后再点一次会从断点继续。
	backupRunTimeout = 20 * time.Minute
)

// backupRunner 按当前配置构造一次运行。配置每次现读，后台改完即时生效。
func (s *Server) backupRunner(ctx context.Context) (*backup.Runner, error) {
	cfg := s.snapshotS3Config(ctx)
	if err := cfg.Validate(); err != nil {
		return nil, errors.New("云存储配置不完整：" + err.Error())
	}
	partMB := backup.ReadStatus(ctx, s.st).PartMB
	if partMB < backupPartMinMB || partMB > backupPartMaxMB {
		partMB = backup.DefaultPartMB
	}
	return &backup.Runner{
		Snaps:     s.snap,
		Client:    objstore.New(cfg, backupRunTimeout),
		Settings:  s.st,
		PartBytes: int64(partMB) << 20,
		TempDir:   s.DataDir,
	}, nil
}

// handleSetBackup POST /admin/snapshot/backup {enabled, part_mb}
// 开启前要求配置完整：否则定时任务每小时失败一次，日志里全是同一个错。
func (s *Server) handleSetBackup(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		Enabled bool `json:"enabled"`
		PartMB  int  `json:"part_mb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.PartMB != 0 && (req.PartMB < backupPartMinMB || req.PartMB > backupPartMaxMB) {
		writeErr(w, http.StatusBadRequest,
			"单包大小需在 "+strconv.Itoa(backupPartMinMB)+"–"+strconv.Itoa(backupPartMaxMB)+" MB 之间")
		return
	}
	ctx := r.Context()
	if req.Enabled {
		if err := s.snapshotS3Config(ctx).Validate(); err != nil {
			writeErr(w, http.StatusBadRequest, "先填完云存储配置（并测试连接）再开启："+err.Error())
			return
		}
	}
	if req.PartMB != 0 {
		if err := s.st.SetSetting(ctx, backup.SettingPartMB, strconv.Itoa(req.PartMB)); err != nil {
			writeErr(w, http.StatusInternalServerError, "写入配置失败")
			return
		}
	}
	if err := s.st.SetSetting(ctx, backup.SettingEnabled, boolStr(req.Enabled)); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入配置失败")
		return
	}
	slog.Info("cloud backup switch changed", "enabled", req.Enabled, "part_mb", req.PartMB)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": req.Enabled, "backup": backup.ReadStatus(ctx, s.st),
	})
}

// tryBackupLock 抢到一轮运行权；抢不到说明已有任务在跑。
// 用 TryLock 而不是排队：用户连点两次「立即冷备」，第二次等十几分钟再跑一遍
// 同一批数据毫无意义，直接告诉他正在跑更清楚。
func (s *Server) tryBackupLock(w http.ResponseWriter) bool {
	if !s.bkMu.TryLock() {
		writeErr(w, http.StatusConflict, "正在执行上一条云端任务，请稍后刷新状态")
		return false
	}
	return true
}

// handleRunBackup POST /admin/snapshot/backup/run：立刻打包上传一轮。
func (s *Server) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	if !s.tryBackupLock(w) {
		return
	}
	defer s.bkMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), backupRunTimeout)
	defer cancel()
	runner, err := s.backupRunner(ctx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := runner.Pack(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "冷备失败："+err.Error())
		return
	}
	slog.Info("cloud backup finished", "parts", res.Parts, "keys", res.Keys,
		"skipped", res.Skipped, "bytes", res.Bytes)
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "backup": backup.ReadStatus(ctx, s.st)})
}

// handleExportDocuments POST /admin/snapshot/export：文档元数据导出为 JSONL.gz。
func (s *Server) handleExportDocuments(w http.ResponseWriter, r *http.Request) {
	if !s.tryBackupLock(w) {
		return
	}
	defer s.bkMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), backupRunTimeout)
	defer cancel()
	runner, err := s.backupRunner(ctx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := runner.Export(ctx, s.st.ExportDocuments)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "导出失败："+err.Error())
		return
	}
	slog.Info("documents export finished", "key", res.Key, "docs", res.Docs, "bytes", res.Bytes)
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "backup": backup.ReadStatus(ctx, s.st)})
}

// handleRestoreSnapshot POST /admin/snapshot/restore {key}
// 从云归档按 manifest 范围读回一页，并与本地快照比对长度。
// 只验「上传成功」不验「读得回来」，等真要恢复时才发现偏移算错就晚了。
func (s *Server) handleRestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.Key == "" {
		writeErr(w, http.StatusBadRequest, "缺少要校验的页面")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	runner, err := s.backupRunner(ctx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	remote, err := runner.Fetch(ctx, req.Key)
	if err != nil {
		if errors.Is(err, backup.ErrNotArchived) {
			writeErr(w, http.StatusNotFound, "该页面还没进云归档")
			return
		}
		writeErr(w, http.StatusBadGateway, "取回失败："+err.Error())
		return
	}
	out := map[string]any{"key": req.Key, "bytes": len(remote)}
	if s.snap != nil {
		if local, ok, gerr := s.snap.Get(ctx, req.Key); gerr == nil && ok {
			out["local_bytes"] = len(local)
			out["match"] = len(local) == len(remote)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// StartBackupLoop 定时冷备：每小时看一次，有新页面就打包，导出每天一次。
//
// 为什么是「每小时看一次」而不是「每天定时跑」：抓取本身是分散的，
// 固定凌晨跑会让一天的快照攒在同一个时间窗，失败了也要等第二天。
// 没有新页面时这一轮只做一次 manifest 读取，几乎不花钱。
func (s *Server) StartBackupLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Hour
	}
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		slog.Info("cloud backup scheduler started", "every", every.String())
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.backupTick(ctx)
			}
		}
	}()
}

func (s *Server) backupTick(ctx context.Context) {
	st := backup.ReadStatus(ctx, s.st)
	if !st.Enabled {
		return
	}
	if !s.bkMu.TryLock() {
		return // 手动任务在跑，下一轮再说
	}
	defer s.bkMu.Unlock()
	// 不继承调度 ctx 的取消：它随进程退出才取消，但这里仍要一个独立超时，
	// 免得一次卡死的上传把后续所有轮次都堵住。
	runCtx, cancel := context.WithTimeout(context.Background(), backupRunTimeout)
	defer cancel()
	runner, err := s.backupRunner(runCtx)
	if err != nil {
		slog.Warn("cloud backup skipped", "err", err)
		return
	}
	res, err := runner.Pack(runCtx)
	if err != nil {
		slog.Warn("cloud backup failed", "err", err)
		_ = s.st.SetSetting(runCtx, backup.SettingLastError, err.Error())
		return
	}
	if res.Keys > 0 {
		slog.Info("cloud backup finished", "parts", res.Parts, "keys", res.Keys, "bytes", res.Bytes)
	}
	if dueExport(st.ExportedAt, time.Now()) {
		ex, err := runner.Export(runCtx, s.st.ExportDocuments)
		if err != nil {
			slog.Warn("documents export failed", "err", err)
			return
		}
		slog.Info("documents exported", "key", ex.Key, "docs", ex.Docs, "bytes", ex.Bytes)
	}
}

// dueExport 距离上次导出超过 24 小时就该再导一次；从没导过也算到期。
func dueExport(last string, now time.Time) bool {
	if last == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	return now.Sub(t) >= 24*time.Hour
}
