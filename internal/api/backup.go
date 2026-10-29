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
	"fmt"
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
	st := backup.ReadStatus(ctx, s.st)
	partMB := st.PartMB
	if partMB < backupPartMinMB || partMB > backupPartMaxMB {
		partMB = backup.DefaultPartMB
	}
	// 打包与读取共用同一份 manifest 缓存：各带一份的话，
	// 刚归档的页在读取侧还会显示「没有快照」，直到缓存过期。
	rd, err := s.snapshotReader(ctx)
	if err != nil {
		return nil, errors.New("云存储配置不完整：" + err.Error())
	}
	return &backup.Runner{
		Snaps:      s.snap,
		Client:     objstore.New(cfg, backupRunTimeout),
		Settings:   s.st,
		PartBytes:  int64(partMB) << 20,
		TempDir:    s.DataDir,
		PruneLocal: st.PruneLocal,
		Man:        rd.Manifest(),
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

// handleRunBackup POST /admin/snapshot/backup/run：立刻返回，后台打包上传。
//
// 为什么必须异步：1145 页约 130MB，同步上传要几十秒到几分钟，
// 而路由上挂着 30 秒的请求超时（防止任何 handler 挂死），
// 同步做会被一刀切断，前端只看到「失败」，实际包已经传了一半。
func (s *Server) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	if !s.tryBackupLock(w) {
		return
	}
	s.setJob("pack")
	go func() {
		defer s.bkMu.Unlock()
		defer s.setJob("")
		ctx, cancel := context.WithTimeout(context.Background(), backupRunTimeout)
		defer cancel()
		runner, err := s.backupRunner(ctx)
		if err != nil {
			s.setJobMsg("冷备失败：" + err.Error())
			return
		}
		res, err := runner.Pack(ctx)
		if err != nil {
			s.setJobMsg("冷备失败：" + err.Error())
			return
		}
		msg := fmt.Sprintf("已上传 %d 个包，归档 %d 页", res.Parts, res.Keys)
		if res.Keys == 0 {
			msg = fmt.Sprintf("没有新页面（已在归档 %d 页）", res.Skipped)
		}
		if res.Pruned > 0 {
			msg += fmt.Sprintf("，清理本地 %d 条", res.Pruned)
		}
		s.setJobMsg(msg)
		slog.Info("cloud backup finished", "parts", res.Parts, "keys", res.Keys,
			"skipped", res.Skipped, "pruned", res.Pruned, "bytes", res.Bytes)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "running", "backup": backup.ReadStatus(r.Context(), s.st),
	})
}

// handleExportDocuments POST /admin/snapshot/export：同样异步，理由同上。
func (s *Server) handleExportDocuments(w http.ResponseWriter, r *http.Request) {
	if !s.tryBackupLock(w) {
		return
	}
	s.setJob("export")
	go func() {
		defer s.bkMu.Unlock()
		defer s.setJob("")
		ctx, cancel := context.WithTimeout(context.Background(), backupRunTimeout)
		defer cancel()
		runner, err := s.backupRunner(ctx)
		if err != nil {
			s.setJobMsg("导出失败：" + err.Error())
			return
		}
		res, err := runner.Export(ctx, s.st.ExportDocuments)
		if err != nil {
			s.setJobMsg("导出失败：" + err.Error())
			return
		}
		s.setJobMsg(fmt.Sprintf("已导出 %d 条，%s", res.Docs, humanBytes(res.Bytes)))
		slog.Info("documents export finished", "key", res.Key, "docs", res.Docs, "bytes", res.Bytes)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "running", "backup": backup.ReadStatus(r.Context(), s.st),
	})
}

// setJob 记录当前任务类型（"" 表示空闲）。
func (s *Server) setJob(kind string) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	s.job = kind
	if kind != "" {
		s.jobAt = time.Now()
	}
}

func (s *Server) setJobMsg(msg string) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	s.jobMsg = msg
}

// jobView 任务状态，给前台轮询。
type jobView struct {
	Job       string `json:"job"`
	StartedAt string `json:"started_at"`
	Message   string `json:"message"`
}

func (s *Server) jobStatus() jobView {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	v := jobView{Job: s.job, Message: s.jobMsg}
	if !s.jobAt.IsZero() {
		v.StartedAt = s.jobAt.Format(time.RFC3339)
	}
	return v
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
	// 和本地那一层比，不能用 s.snap：它已经带云端回源，
	// 拿它比等于「云端和自己比」，永远是 true（真删了也看不出来）。
	if s.snapLocal != nil {
		if local, ok, gerr := s.snapLocal.Get(ctx, req.Key); gerr == nil && ok {
			out["local_bytes"] = len(local)
			out["match"] = len(local) == len(remote)
		} else {
			out["local_absent"] = true
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetDBEvery POST /admin/snapshot/db {every_hours}
// 整库备份的频率。0 表示关闭；最小 6 小时——再密就是拿 IO 换心理安慰。
func (s *Server) handleSetDBEvery(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		EveryHours int `json:"every_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.EveryHours != 0 && (req.EveryHours < 6 || req.EveryHours > 168) {
		writeErr(w, http.StatusBadRequest, "备份频率需在 6–168 小时之间，或填 0 关闭")
		return
	}
	if err := s.st.SetSetting(r.Context(), backup.SettingDBEvery, strconv.Itoa(req.EveryHours)); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入配置失败")
		return
	}
	slog.Info("db backup interval changed", "hours", req.EveryHours)
	writeJSON(w, http.StatusOK, map[string]any{"backup": backup.ReadStatus(r.Context(), s.st)})
}

// handleRunDBBackup POST /admin/snapshot/db/run：立刻备一份整库（异步）。
func (s *Server) handleRunDBBackup(w http.ResponseWriter, r *http.Request) {
	if !s.tryBackupLock(w) {
		return
	}
	s.setJob("db")
	go func() {
		defer s.bkMu.Unlock()
		defer s.setJob("")
		ctx, cancel := context.WithTimeout(context.Background(), backupRunTimeout)
		defer cancel()
		runner, err := s.backupRunner(ctx)
		if err != nil {
			s.setJobMsg("备份失败：" + err.Error())
			return
		}
		res, err := runner.DB(ctx, s.st.BackupTo)
		if err != nil {
			s.setJobMsg("备份失败：" + err.Error())
			return
		}
		s.setJobMsg(fmt.Sprintf("数据库已备份，%s", humanBytes(res.Bytes)))
		slog.Info("db backup finished", "key", res.Key, "bytes", res.Bytes)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "running", "backup": backup.ReadStatus(r.Context(), s.st),
	})
}

// handleRestoreAllSnapshots POST /admin/snapshot/restore-all
// 把云端归档里的页面全部写回本地（本地已有的跳过）。
// 恢复网站时用：DB 还原完，先把页面取回来，再跑 -reindex 重建索引。
func (s *Server) handleRestoreAllSnapshots(w http.ResponseWriter, r *http.Request) {
	if s.snapLocal == nil {
		writeErr(w, http.StatusBadRequest, "原文快照未启用")
		return
	}
	if !s.tryBackupLock(w) {
		return
	}
	s.setJob("restore")
	go func() {
		defer s.bkMu.Unlock()
		defer s.setJob("")
		// 取回整站是逐页 Range GET，20 分钟不够几万页，这里放宽到 2 小时。
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		rd, err := s.snapshotReader(ctx)
		if err != nil {
			s.setJobMsg("取回失败：" + err.Error())
			return
		}
		restored, skipped, err := rd.RestoreAll(ctx,
			func(key string) (bool, error) { return s.snapLocal.Has(ctx, key) },
			func(key string, val []byte) error { return s.snapLocal.Put(ctx, key, val) },
		)
		if err != nil {
			s.setJobMsg(fmt.Sprintf("取回中断：已恢复 %d 页（%v）", restored, err))
			return
		}
		s.setJobMsg(fmt.Sprintf("已取回 %d 页，跳过本地已有的 %d 页", restored, skipped))
		slog.Info("snapshot restore finished", "restored", restored, "skipped", skipped)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "running"})
}

// handleSnapshotMode POST /admin/snapshot/mode {mode: local|cloud}
//
// local：本地留全量，云端是备份；
// cloud：上传成功后删掉本地副本，读取时从归档回源（本地只是暂存区）。
//
// 选 cloud 必须同时满足「归档开着 + 桶配置完整」：否则删了本地又没传上去，
// 快照就真的没了——这个组合必须在校验里挡死，不能指望用户先点对顺序。
func (s *Server) handleSnapshotMode(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	ctx := r.Context()
	switch req.Mode {
	case "local":
	case "cloud":
		if !backup.ReadStatus(ctx, s.st).Enabled {
			writeErr(w, http.StatusBadRequest, "先开启云冷备（并确认能上传）再改成只留云端")
			return
		}
		if err := s.snapshotS3Config(ctx).Validate(); err != nil {
			writeErr(w, http.StatusBadRequest, "云存储配置不完整："+err.Error())
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "未知的存储方式："+req.Mode)
		return
	}
	prune := req.Mode == "cloud"
	if err := s.st.SetSetting(ctx, backup.SettingPruneLocal, boolStr(prune)); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入配置失败")
		return
	}
	s.cloudMode.Store(prune)
	slog.Info("snapshot storage mode changed", "mode", req.Mode)
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": req.Mode, "backup": backup.ReadStatus(ctx, s.st),
	})
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
	if dueEvery(st.DBLastAt, st.DBEvery, time.Now()) {
		dbr, err := runner.DB(runCtx, s.st.BackupTo)
		if err != nil {
			slog.Warn("db backup failed", "err", err)
			return
		}
		slog.Info("db backup finished", "key", dbr.Key, "bytes", dbr.Bytes)
	}
}

// dueEvery 按「上次时间 + 间隔小时」判断该不该再跑一次；0 表示关闭。
func dueEvery(last string, everyHours int, now time.Time) bool {
	if everyHours <= 0 {
		return false
	}
	if last == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	return now.Sub(t) >= time.Duration(everyHours)*time.Hour
}

// humanBytes 给任务结果用的一句话体积（KB/MB/GB），避免前端再换算一次。
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
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
