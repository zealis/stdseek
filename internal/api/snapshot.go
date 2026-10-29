package api

// 原文快照的运维入口：开关、占用、清理。
//
// 为什么需要这一页：快照是索引之外的第二份数据（Pebble 里一整页原始 HTML），
// 它会随抓取页数线性增长，而用户从界面上看不到任何痕迹——
// 等到磁盘满的时候才发现，已经没法回溯是哪一步堆起来的。
// 所以三个动作都摆到后台：关掉新增、看住占用、回收孤儿。

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"voidxo/internal/backup"
	"voidxo/internal/snapshot"
	"voidxo/internal/store"
)

const (
	// snapshotPrefix 与 kv.Key 拼出的前缀一致（page:<urlkey>）。
	snapshotPrefix = "page:"
	// snapshotCapBytes 单页快照上限，与 crawler 里的截断保持一致。
	snapshotCapBytes = 2 * 1024 * 1024
	// snapshotCleanupMax 单次清理上限：清理是同步操作，别让一个请求跑几分钟。
	snapshotCleanupMax = 200000
)

// snapshotStatsView GET /admin/snapshot 的返回。
type snapshotStatsView struct {
	Enabled   bool  `json:"enabled"`   // 抓取时是否还在保存新快照
	Pages     int   `json:"pages"`     // KV 里的快照条数
	Orphans   int   `json:"orphans"`   // 索引里已经没有对应文档的快照
	Bytes     int64 `json:"bytes"`     // KV 库磁盘占用
	CapBytes  int64 `json:"cap_bytes"` // 单页上限，用于让"2MB"这个数字只有一个来源
	Documents int64 `json:"documents"` // 索引文档数，用来对照"每页平均多少"
	Available bool  `json:"available"` // KV 是否挂载（-no-snapshot 等部署下为 false）
	// Storage 云存储配置（本阶段只读展示；读写仍走本地）。
	Storage snapshotStorageView `json:"storage"`
	// Backup 云冷备状态：开关、已归档量、上次运行与最近错误。
	Backup backup.Status `json:"backup"`
}

// snapshotEnabled 读开关。缺省开启（与 crawler 的默认值保持一致）。
func (s *Server) snapshotEnabled(ctx context.Context) bool {
	v, err := s.st.Setting(ctx, store.SettingSnapshotEnabled, "true")
	if err != nil {
		return true
	}
	return v != "false"
}

// handleSnapshotStats GET /admin/snapshot：开关状态 + 占用 + 孤儿数。
func (s *Server) handleSnapshotStats(w http.ResponseWriter, r *http.Request) {
	view := snapshotStatsView{
		Enabled:   s.snapshotEnabled(r.Context()),
		CapBytes:  snapshotCapBytes,
		Available: s.snap != nil,
		Storage:   s.snapshotStorage(r.Context()),
		Backup:    backup.ReadStatus(r.Context(), s.st),
	}
	docs, err := s.st.CountDocuments(r.Context())
	if err == nil {
		view.Documents = docs
	}
	if s.snap != nil {
		view.Bytes = s.snap.Size()
		if n, cerr := s.snap.Count(r.Context(), snapshotPrefix); cerr == nil {
			view.Pages = n
		}
		if orphans, oerr := s.snapshotOrphans(r.Context()); oerr == nil {
			view.Orphans = len(orphans)
		}
	}
	writeJSON(w, http.StatusOK, view)
}

// handleSetSnapshot POST /admin/snapshot {enabled}：改开关，即时生效。
// 关掉只停止新增，已有快照保留——历史结果上的引用编号不能变成死链。
func (s *Server) handleSetSnapshot(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := s.st.SetSetting(r.Context(), store.SettingSnapshotEnabled, boolStr(req.Enabled)); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入配置失败")
		return
	}
	// 开关变动必须留痕：事后排查"为什么某天之后的页面没有快照"时，这行是唯一线索。
	slog.Info("snapshot switch changed", "enabled", req.Enabled, "at", time.Now().Format(time.RFC3339))
	writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": req.Enabled})
}

// snapshotCleanupReq POST /admin/snapshot/cleanup 的入参。
// mode=orphan 只删索引里已不存在的页；mode=all 全清。
type snapshotCleanupReq struct {
	Mode string `json:"mode"`
}

// handleCleanupSnapshot POST /admin/snapshot/cleanup：回收快照占用的空间。
//
// 删除后必须 Compact：只写墓碑的话 DiskSpaceUsage 不会降，
// 后台显示"已清理 4000 条"但磁盘一点没变，比不做还让人困惑。
func (s *Server) handleCleanupSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.snap == nil {
		writeErr(w, http.StatusBadRequest, "原文快照未启用，没有可清理的数据")
		return
	}
	maxBody(w, r)
	var req snapshotCleanupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	var keys []string
	var err error
	switch req.Mode {
	case "orphan":
		keys, err = s.snapshotOrphans(r.Context())
	case "all":
		keys, err = s.snapshotAllKeys(r.Context())
	default:
		writeErr(w, http.StatusBadRequest, "未知的清理模式："+req.Mode)
		return
	}
	if err != nil {
		slog.Warn("snapshot cleanup scan failed", "mode", req.Mode, "err", err)
		writeErr(w, http.StatusInternalServerError, "扫描快照失败")
		return
	}

	before := s.snap.Size()
	removed := 0
	for i := 0; i < len(keys); i += 500 {
		end := i + 500
		if end > len(keys) {
			end = len(keys)
		}
		if derr := s.snap.DeleteMany(r.Context(), keys[i:end]); derr != nil {
			slog.Warn("snapshot cleanup delete failed", "mode", req.Mode, "removed", removed, "err", derr)
			writeErr(w, http.StatusInternalServerError, "删除失败，已删除 "+strconv.Itoa(removed)+" 条")
			return
		}
		removed = end
		if removed >= snapshotCleanupMax {
			break
		}
	}
	// 没有删掉东西就别 Compact：整段合并会把现存数据重写一遍，
	// 期间磁盘占用反而上涨（实测 48MB → 54MB），用户会以为清理把东西弄多了。
	if removed > 0 {
		if mt, ok := s.snap.(snapshot.Maintenance); ok {
			if err := mt.Compact(r.Context(), snapshotPrefix); err != nil {
				slog.Warn("snapshot compact failed", "err", err)
			}
		}
	}
	after := s.snap.Size()
	freed := before - after
	if freed < 0 {
		// 合并期间可能先把现存数据重写一遍，占用短时上涨；对外只报非负的回收量。
		freed = 0
	}
	slog.Info("snapshot cleanup", "mode", req.Mode, "removed", removed,
		"bytes_before", before, "bytes_after", after, "at", time.Now().Format(time.RFC3339))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mode":         req.Mode,
		"removed":      removed,
		"truncated":    len(keys) > removed,
		"bytes_before": before,
		"bytes_after":  after,
		"freed":        freed,
	})
}

// snapshotOrphans 找出索引里已没有对应文档的快照键。
// DocumentURLs 的 key 就是 urlkey，与 KV 键去掉前缀后完全同构，不需要二次归一化。
func (s *Server) snapshotOrphans(ctx context.Context) ([]string, error) {
	docs, err := s.st.DocumentURLs(ctx)
	if err != nil {
		return nil, err
	}
	mt, ok := s.snap.(snapshot.Maintenance)
	if !ok {
		return nil, snapshot.ErrMaintenanceUnsupported
	}
	var out []string
	err = mt.Scan(ctx, snapshotPrefix, func(key string, _ []byte) error {
		if _, ok := docs[strings.TrimPrefix(key, snapshotPrefix)]; !ok {
			out = append(out, key)
		}
		return nil
	})
	return out, err
}

// snapshotAllKeys 列出全部快照键。
func (s *Server) snapshotAllKeys(ctx context.Context) ([]string, error) {
	mt, ok := s.snap.(snapshot.Maintenance)
	if !ok {
		return nil, snapshot.ErrMaintenanceUnsupported
	}
	var out []string
	err := mt.Scan(ctx, snapshotPrefix, func(key string, _ []byte) error {
		out = append(out, key)
		return nil
	})
	return out, err
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
