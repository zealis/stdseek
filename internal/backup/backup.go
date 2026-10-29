package backup

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"time"

	"voidxo/internal/objstore"
	"voidxo/internal/snapshot"
	"voidxo/internal/store"
)

// 云冷备：把本地热层（Pebble 里的原文快照 + SQLite 里的文档元数据）
// 按包推到对象存储。
//
// 为什么按包而不是一页一个对象：对象存储的元数据操作与 LIST 是按「对象数」
// 计费的，20 万页就是 20 万次 PUT，加上下游一次列举几万次 LIST，
// 请求费会超过存储费本身，且任何按前缀扫描的作业都会被小文件拖死。
// 这里每包 128MB（可调），一页只占包内一段连续字节。
//
// 包格式是自定义的记录流，不是 tar：tar 的头长度依赖实现（PAX/GNU 扩展会变），
// 自己做「8 字节头 + key + value」反而能保证偏移量算得准。
//
//	record := uint32be(keyLen) | uint32be(valLen) | key | val
//
// 回源读取只靠 manifest 里的 (包名, 偏移, 长度) 做一次 Range GET，
// 不需要下载整个包，也不需要在服务端维护索引。

// 状态与配置都存 settings 表：改完即时生效，换机器只要带走 data 目录。
const (
	SettingEnabled    = "storage.backup.enabled"     // "true" 才自动上传；默认关（会向外发数据）
	SettingPartMB     = "storage.backup.part_mb"     // 单包上限，默认 128
	SettingParts      = "storage.backup.parts"       // 已上传包数
	SettingKeys       = "storage.backup.keys"        // 已归档页数
	SettingLastRun    = "storage.backup.last_run"    // RFC3339
	SettingLastError  = "storage.backup.last_error"  // 最近一次失败原因，成功后清空
	SettingLastKey    = "storage.backup.last_key"    // 最近归档的 kv key，后台「校验归档」用它
	SettingExportedAt = "storage.backup.exported_at" // 文档导出时间 RFC3339
	SettingExportKey  = "storage.backup.export_key"  // 最近一次导出的对象名
	SettingExportDocs = "storage.backup.export_docs" // 最近一次导出的条数
)

// DefaultPartMB 单包默认大小。128MB 是对象存储上「不太小、也不算大」的折中：
// 一包 20 万页里约 640 页，几十个包就能装完整站；单个包上传失败的代价也可控。
const DefaultPartMB = 128

// 包内前缀（相对配置里的桶前缀）。
const (
	partsDir   = "parts"
	idxDir     = "idx"
	exportsDir = "exports"
)

// Settings 冷备需要的配置读写能力，*store.Store 直接满足。
type Settings interface {
	Setting(ctx context.Context, key, def string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// ExportSource 文档导出数据源；api 传 s.st.ExportDocuments。
// 用函数而不是直接依赖 *store.Store：冷备的打包逻辑能脱离数据库单测。
type ExportSource func(ctx context.Context, fn func(store.ExportDoc) error) (int, error)

// Runner 一次冷备运行所需的一切。配置每次运行现读，改后台即时生效。
type Runner struct {
	Snaps     snapshot.Store
	Client    *objstore.Client
	Settings  Settings
	PartBytes int64  // 单包上限（字节）
	TempDir   string // 打包临时目录（与数据同盘，避免跨盘拷贝）
}

// Status 后台展示的冷备状态。
type Status struct {
	Enabled    bool   `json:"enabled"`
	PartMB     int    `json:"part_mb"`
	Parts      int    `json:"parts"`
	Keys       int    `json:"keys"`
	LastRun    string `json:"last_run"`
	LastError  string `json:"last_error"`
	LastKey    string `json:"last_key"`
	ExportedAt string `json:"exported_at"`
	ExportKey  string `json:"export_key"`
	ExportDocs int    `json:"export_docs"`
}

// ReadStatus 从 settings 读出状态（缺省值集中在这里，前后端不再各猜一遍）。
func ReadStatus(ctx context.Context, s Settings) Status {
	get := func(key, def string) string {
		v, err := s.Setting(ctx, key, def)
		if err != nil || v == "" {
			return def
		}
		return v
	}
	atoi := func(key, def string) int {
		n, err := strconv.Atoi(get(key, def))
		if err != nil {
			return 0
		}
		return n
	}
	return Status{
		Enabled:    get(SettingEnabled, "false") == "true",
		PartMB:     atoi(SettingPartMB, strconv.Itoa(DefaultPartMB)),
		Parts:      atoi(SettingParts, "0"),
		Keys:       atoi(SettingKeys, "0"),
		LastRun:    get(SettingLastRun, ""),
		LastError:  get(SettingLastError, ""),
		LastKey:    get(SettingLastKey, ""),
		ExportedAt: get(SettingExportedAt, ""),
		ExportKey:  get(SettingExportKey, ""),
		ExportDocs: atoi(SettingExportDocs, "0"),
	}
}

// PackResult 一次打包的结果。
type PackResult struct {
	Parts   int   `json:"parts"`   // 本次上传的包数
	Keys    int   `json:"keys"`    // 本次新归档的页数
	Skipped int   `json:"skipped"` // 已在归档里的页数
	Bytes   int64 `json:"bytes"`   // 上传字节数
}

// ExportResult 一次文档导出的结果。
type ExportResult struct {
	Key   string `json:"key"`
	Docs  int    `json:"docs"`
	Bytes int64  `json:"bytes"`
}

// Pack 把尚未归档的快照打包上传。
//
// 判定「是否已归档」靠 manifest 里有没有这个 key，而不是靠游标：
// key 是按 URL 排序的，新页面会插到任意位置，游标会漏掉插在中间的那些。
func (r *Runner) Pack(ctx context.Context) (PackResult, error) {
	var res PackResult
	if r.Client == nil {
		return res, errors.New("未配置对象存储")
	}
	if err := r.Client.Config().Validate(); err != nil {
		return res, err
	}
	mt, ok := r.Snaps.(snapshot.Maintenance)
	if !ok || r.Snaps == nil {
		return res, snapshot.ErrMaintenanceUnsupported
	}
	limit := r.PartBytes
	if limit <= 0 {
		limit = DefaultPartMB << 20
	}

	stamp := time.Now().UTC().Format("20060102T150405Z")
	shards := newShardSet(r.Client, idxDir)
	var cur *part
	seq := 0
	lastKey := ""

	flush := func() error {
		if cur == nil || cur.n == 0 {
			return nil
		}
		if err := cur.close(); err != nil {
			return err
		}
		size, err := r.Client.PutFile(ctx, cur.key, cur.path, "application/octet-stream")
		if err != nil {
			os.Remove(cur.path)
			return err
		}
		res.Parts++
		res.Bytes += size
		if err := os.Remove(cur.path); err != nil {
			// 删不掉只是留个临时文件，不能因此判定归档失败。
			_ = err
		}
		cur = nil
		return nil
	}

	err := mt.Scan(ctx, "page:", func(key string, val []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		sh, err := shards.get(ctx, key)
		if err != nil {
			return err
		}
		if _, ok := sh.Entries[key]; ok {
			res.Skipped++
			return nil
		}
		rec := int64(8 + len(key) + len(val))
		if cur == nil || (cur.n > 0 && cur.n+rec > limit) {
			if err := flush(); err != nil {
				return err
			}
			seq++
			name := fmt.Sprintf("%s/%s-%04d.pack", partsDir, stamp, seq)
			p, err := newPart(r.TempDir, r.Client.Config().ObjectKey(name))
			if err != nil {
				return err
			}
			cur = p
		}
		off, err := cur.write(key, val)
		if err != nil {
			return err
		}
		sh.Entries[key] = entry{Part: cur.key, Off: off, KLen: len(key), VLen: len(val)}
		sh.dirty = true
		res.Keys++
		lastKey = key
		return nil
	})
	if err != nil {
		if cur != nil {
			cur.close()
			os.Remove(cur.path)
		}
		settingOrLog(ctx, r.Settings, SettingLastError, err.Error())
		return res, err
	}
	if err := flush(); err != nil {
		settingOrLog(ctx, r.Settings, SettingLastError, err.Error())
		return res, err
	}
	if err := shards.save(ctx); err != nil {
		settingOrLog(ctx, r.Settings, SettingLastError, err.Error())
		return res, err
	}

	// 状态落库：包数/页数是累计值，页面要能看出「已经存了多少」。
	prev := ReadStatus(ctx, r.Settings)
	settingOrLog(ctx, r.Settings, SettingParts, strconv.Itoa(prev.Parts+res.Parts))
	settingOrLog(ctx, r.Settings, SettingKeys, strconv.Itoa(prev.Keys+res.Keys))
	settingOrLog(ctx, r.Settings, SettingLastRun, time.Now().Format(time.RFC3339))
	if lastKey != "" {
		settingOrLog(ctx, r.Settings, SettingLastKey, lastKey)
	}
	settingOrLog(ctx, r.Settings, SettingLastError, "")
	return res, nil
}

// Export 把文档元数据导成 JSONL.gz 推到对象存储。
//
// 只导元数据（URL/标题/摘要/标签/字数/时间/指纹），不导正文：
// 正文在快照包里已经有了，重复一遍只会让文件大一个数量级。
// JSONL 而不是 Parquet：不引入新依赖，Athena/Spark 与 duckdb 都能直接读 gzip 行式 JSON。
func (r *Runner) Export(ctx context.Context, src ExportSource) (ExportResult, error) {
	var res ExportResult
	if r.Client == nil {
		return res, errors.New("未配置对象存储")
	}
	if err := r.Client.Config().Validate(); err != nil {
		return res, err
	}
	if src == nil {
		return res, errors.New("没有可导出的数据源")
	}
	f, err := os.CreateTemp(r.TempDir, "voidxo-docs-*.jsonl.gz")
	if err != nil {
		return res, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	zw := gzip.NewWriter(f)
	enc := json.NewEncoder(zw) // Encode 每条自带换行，正好是 JSONL
	n, err := src(ctx, func(d store.ExportDoc) error {
		return enc.Encode(d)
	})
	if err != nil {
		zw.Close()
		f.Close()
		return res, err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return res, err
	}
	if err := f.Close(); err != nil {
		return res, err
	}
	name := fmt.Sprintf("%s/documents-%s.jsonl.gz", exportsDir, time.Now().UTC().Format("20060102T150405Z"))
	key := r.Client.Config().ObjectKey(name)
	size, err := r.Client.PutFile(ctx, key, tmp, "application/gzip")
	if err != nil {
		settingOrLog(ctx, r.Settings, SettingLastError, err.Error())
		return res, err
	}
	res = ExportResult{Key: key, Docs: n, Bytes: size}
	settingOrLog(ctx, r.Settings, SettingExportedAt, time.Now().Format(time.RFC3339))
	settingOrLog(ctx, r.Settings, SettingExportKey, key)
	settingOrLog(ctx, r.Settings, SettingExportDocs, strconv.Itoa(n))
	return res, nil
}

// ErrNotArchived 这一页还没进归档。
var ErrNotArchived = errors.New("该页面不在云归档里")

// Fetch 按 manifest 做一次范围读，取回某页的原文。
// 这是「归档能读回来」的唯一证据：只验上传成功不验回读，
// 等真需要恢复时才发现偏移量算错，就太晚了。
func (r *Runner) Fetch(ctx context.Context, key string) ([]byte, error) {
	if r.Client == nil {
		return nil, errors.New("未配置对象存储")
	}
	shards := newShardSet(r.Client, idxDir)
	sh, err := shards.get(ctx, key)
	if err != nil {
		return nil, err
	}
	e, ok := sh.Entries[key]
	if !ok {
		return nil, ErrNotArchived
	}
	total := int64(8 + e.KLen + e.VLen)
	buf, err := r.Client.GetRange(ctx, e.Part, e.Off, total)
	if err != nil {
		return nil, err
	}
	gotKey, val, err := parseRecord(buf)
	if err != nil {
		return nil, err
	}
	if gotKey != key {
		return nil, fmt.Errorf("偏移错位：取回的是 %q", gotKey)
	}
	return val, nil
}

// ---- 包文件 ----

// part 一个正在写的包。
type part struct {
	f    *os.File
	path string
	key  string // 桶内对象名
	n    int64  // 已写字节数
}

func newPart(dir, key string) (*part, error) {
	f, err := os.CreateTemp(dir, "voidxo-pack-*.pack")
	if err != nil {
		return nil, err
	}
	return &part{f: f, path: f.Name(), key: key}, nil
}

// write 追加一条记录，返回记录起始偏移。
func (p *part) write(key string, val []byte) (int64, error) {
	off := p.n
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(len(key)))
	binary.BigEndian.PutUint32(hdr[4:8], uint32(len(val)))
	if _, err := p.f.Write(hdr[:]); err != nil {
		return 0, err
	}
	if _, err := io.WriteString(p.f, key); err != nil {
		return 0, err
	}
	if _, err := p.f.Write(val); err != nil {
		return 0, err
	}
	p.n += int64(len(hdr) + len(key) + len(val))
	return off, nil
}

func (p *part) close() error { return p.f.Close() }

// parseRecord 解析一条记录，返回 key 与 value。
func parseRecord(b []byte) (string, []byte, error) {
	if len(b) < 8 {
		return "", nil, errors.New("记录头不完整")
	}
	klen := int(binary.BigEndian.Uint32(b[0:4]))
	vlen := int(binary.BigEndian.Uint32(b[4:8]))
	if len(b) < 8+klen+vlen {
		return "", nil, errors.New("记录体不完整")
	}
	return string(b[8 : 8+klen]), b[8+klen : 8+klen+vlen], nil
}

// settingOrLog 写状态失败不该让归档整体失败：数据已经在桶里了，
// 少记一个统计值不值得重跑一遍 128MB 上传。
func settingOrLog(ctx context.Context, s Settings, key, val string) {
	if s == nil {
		return
	}
	_ = s.SetSetting(ctx, key, val)
}

// ---- manifest ----

// entry 一页在包里的位置。
type entry struct {
	Part string `json:"p"` // 包对象名
	Off  int64  `json:"o"` // 记录起始偏移
	KLen int    `json:"k"`
	VLen int    `json:"v"`
}

// shard 一份 manifest 分片。按 key 的 sha256 首字节分成 256 份，
// 单份保持在几十 KB：回源时只读一份，不用列举或下载全量索引。
type shard struct {
	Entries map[string]entry `json:"entries"`
	dirty   bool
}

type shardSet struct {
	c      *objstore.Client
	dir    string
	cache  map[string]*shard
	loaded map[string]bool
}

func newShardSet(c *objstore.Client, dir string) *shardSet {
	return &shardSet{c: c, dir: dir, cache: map[string]*shard{}, loaded: map[string]bool{}}
}

func shardName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:1]) + ".json"
}

func (s *shardSet) path(name string) string {
	return s.c.Config().ObjectKey(path.Join(s.dir, name))
}

// get 取某 key 所属分片；第一次访问时从桶里拉，不存在就建空的。
// 404 是常态（首次归档），不能当成错误，否则第一轮永远跑不起来。
func (s *shardSet) get(ctx context.Context, key string) (*shard, error) {
	name := shardName(key)
	if s.loaded[name] {
		return s.cache[name], nil
	}
	s.loaded[name] = true
	sh := &shard{Entries: map[string]entry{}}
	buf, err := s.c.Get(ctx, s.path(name))
	switch {
	case err == nil:
		if jerr := json.Unmarshal(buf, sh); jerr != nil {
			return nil, fmt.Errorf("manifest %s 解析失败：%w", name, jerr)
		}
		if sh.Entries == nil {
			sh.Entries = map[string]entry{}
		}
	case errors.Is(err, objstore.ErrNotFound):
		// 首次归档
	default:
		s.loaded[name] = false
		return nil, err
	}
	s.cache[name] = sh
	return sh, nil
}

// save 只上传变化过的分片。
func (s *shardSet) save(ctx context.Context) error {
	for name, sh := range s.cache {
		if sh == nil || !sh.dirty {
			continue
		}
		buf, err := json.Marshal(sh)
		if err != nil {
			return err
		}
		if err := s.c.PutBytes(ctx, s.path(name), buf, "application/json"); err != nil {
			return err
		}
		sh.dirty = false
	}
	return nil
}
