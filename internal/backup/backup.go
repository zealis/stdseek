package backup

import (
	"bytes"
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
	"sync"
	"sync/atomic"
	"time"

	"github.com/zealis/stdseek/internal/objstore"
	"github.com/zealis/stdseek/internal/snapshot"
	"github.com/zealis/stdseek/internal/store"
)

// 云归档：把本地热层（Pebble 里的原文快照 + SQLite 里的文档元数据）
// 按包推到对象存储，并支持两种使用方式：
//
//	备份（默认）：本地留全量，云端是底稿；
//	云端为主（PruneLocal）：上传成功后删掉本地副本，读取时按 manifest 回源。
//
// 为什么按包而不是一页一个对象：对象存储的元数据操作与 LIST 是按「对象数」
// 计费的，20 万页就是 20 万次 PUT，加上下游一次列举几万次 LIST，
// 请求费会超过存储费本身，且任何按前缀扫描的作业都会被小文件拖死。
//
// 包格式是自定义记录流，不是 tar：tar 的头长度依赖实现（PAX/GNU 扩展会变），
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
	// SettingPruneLocal 云端为主：上传成功后删除本地快照，读取时回源。
	// 关掉就退回「本地全量 + 云端备份」。
	SettingPruneLocal = "storage.backup.prune_local"
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

// Settings 归档需要的配置读写能力，*store.Store 直接满足。
type Settings interface {
	Setting(ctx context.Context, key, def string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// ExportSource 文档导出数据源；api 传 s.st.ExportDocuments。
// 用函数而不是直接依赖 *store.Store：打包逻辑能脱离数据库单测。
type ExportSource func(ctx context.Context, fn func(store.ExportDoc) error) (int, error)

// Runner 一次归档运行所需的一切。配置每次运行现读，改后台即时生效。
type Runner struct {
	Snaps     snapshot.Store
	Client    *objstore.Client
	Settings  Settings
	PartBytes int64  // 单包上限（字节）
	TempDir   string // 打包临时目录（与数据同盘，避免跨盘拷贝）
	// PruneLocal 上传成功后删除本地副本（云端为主模式）。
	// 只有在 manifest 写成功之后才删——顺序反了就是丢数据。
	PruneLocal bool
	// Man 可复用的 manifest（缓存分片）；nil 时本次运行自建。
	Man *Manifest
	// Progress 进度回调（阶段, 已完成, 总数）。nil 表示不报进度。
	Progress func(phase string, done, total int64)
}

// report 安全地报一次进度。
func (r *Runner) report(phase string, done, total int64) {
	if r.Progress != nil {
		r.Progress(phase, done, total)
	}
}

// Status 后台展示的归档状态。
type Status struct {
	Enabled    bool   `json:"enabled"`
	PruneLocal bool   `json:"prune_local"`
	PartMB     int    `json:"part_mb"`
	Parts      int    `json:"parts"`
	Keys       int    `json:"keys"`
	LastRun    string `json:"last_run"`
	LastError  string `json:"last_error"`
	LastKey    string `json:"last_key"`
	ExportedAt string `json:"exported_at"`
	ExportKey  string `json:"export_key"`
	ExportDocs int    `json:"export_docs"`
	// 整库备份
	DBEvery  int    `json:"db_every_hours"`
	DBLastAt string `json:"db_last_at"`
	DBKey    string `json:"db_key"`
	DBSize   int64  `json:"db_size"`
	DBParts  int    `json:"db_parts"`
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
		PruneLocal: get(SettingPruneLocal, "false") == "true",
		PartMB:     atoi(SettingPartMB, strconv.Itoa(DefaultPartMB)),
		Parts:      atoi(SettingParts, "0"),
		Keys:       atoi(SettingKeys, "0"),
		LastRun:    get(SettingLastRun, ""),
		LastError:  get(SettingLastError, ""),
		LastKey:    get(SettingLastKey, ""),
		ExportedAt: get(SettingExportedAt, ""),
		ExportKey:  get(SettingExportKey, ""),
		ExportDocs: atoi(SettingExportDocs, "0"),
		DBEvery:    atoi(SettingDBEvery, "24"),
		DBLastAt:   get(SettingDBLastAt, ""),
		DBKey:      get(SettingDBKey, ""),
		DBSize:     int64(atoi(SettingDBSize, "0")),
		DBParts:    len(dbKeysOf(ctx, s)),
	}
}

// dbKeysOf 读整库备份保留列表（包级函数，ReadStatus 用）。
func dbKeysOf(ctx context.Context, s Settings) []string {
	if s == nil {
		return nil
	}
	raw, err := s.Setting(ctx, SettingDBKeys, "")
	if err != nil || raw == "" {
		return nil
	}
	var keys []string
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return nil
	}
	return keys
}

// PackResult 一次打包的结果。
type PackResult struct {
	Parts   int   `json:"parts"`   // 本次上传的包数
	Keys    int   `json:"keys"`    // 本次新归档（或更新）的页数
	Skipped int   `json:"skipped"` // 内容没变、已在归档里的页数
	Pruned  int   `json:"pruned"`  // 上传后删掉的本地副本
	Bytes   int64 `json:"bytes"`   // 上传字节数
}

// ExportResult 一次文档导出的结果。
type ExportResult struct {
	Key   string `json:"key"`
	Docs  int    `json:"docs"`
	Bytes int64  `json:"bytes"`
}

// Pack 把尚未归档（或内容已变）的快照打包上传。
//
// 判定「是否已归档」靠 manifest 里的内容哈希，不是靠游标也不是只看 key：
//   - 游标会漏掉插在中间的 key（key 按 URL 排序，新页面会插到前面）；
//   - 只看 key 会让重抓过的页面永远停在旧版本，云端存的是过期内容。
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
	man := r.Man
	if man == nil {
		man = NewManifest(r.Client)
	}

	stamp := time.Now().UTC().Format("20060102T150405Z")
	// 先数一下共多少页，进度才有分母；Count 只读元数据，不扫值。
	totalPages, _ := r.Snaps.Count(ctx, "page:")
	seen := 0
	var cur *part
	seq := 0
	lastKey := ""
	var packed []string
	// archivedLocally 已归档且本地还有副本的 key（内容没变的那批），
	// 云端为主模式下也要删，否则开启当天的存量页永远清不掉。
	var archivedLocally []string

	flush := func() error {
		if cur == nil || cur.n == 0 {
			return nil
		}
		if err := cur.close(); err != nil {
			return err
		}
		size, err := r.Client.PutFileProgress(ctx, cur.key, cur.path, "application/octet-stream",
			func(sent, total int64) { r.report("上传", sent, total) })
		if err != nil {
			os.Remove(cur.path)
			return err
		}
		res.Parts++
		res.Bytes += size
		_ = os.Remove(cur.path)
		cur = nil
		return nil
	}

	err := mt.Scan(ctx, "page:", func(key string, val []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen++
		if seen%10 == 0 {
			r.report("打包", int64(seen), int64(totalPages))
		}
		hash := contentHash(val)
		old, archived, lerr := man.Lookup(ctx, key)
		if lerr != nil {
			return lerr
		}
		if archived && old.H == hash {
			res.Skipped++
			// 云端为主模式：内容没变也要清本地。否则第一次开启时，
			// 已经归档过的老页面永远不会被删，本地一直是全量。
			if r.PruneLocal {
				archivedLocally = append(archivedLocally, key)
			}
			return nil
		}
		// 逐条压缩：Pebble 里存的是 snappy 压过的，取出来是原始 HTML，
		// 1145 页就要 133MB；gzip 后约 1/4，而按记录压缩仍然支持随机读——
		// 整包压缩（tar.gz）会把「只取一页」变成「下载 130MB」。
		stored, compressed := compressValue(val)
		rec := int64(8 + len(key) + len(stored))
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
		off, err := cur.write(key, stored)
		if err != nil {
			return err
		}
		man.Mark(key, entry{Part: cur.key, Off: off, KLen: len(key), VLen: len(stored), H: hash, Z: compressed})
		res.Keys++
		packed = append(packed, key)
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
	// manifest 必须先落盘再删本地：反过来的话，上传成功但 manifest 没写，
	// 本地又删了，这一页就再也找不到（只能靠全量重扫桶去猜）。
	if err := man.Save(ctx); err != nil {
		settingOrLog(ctx, r.Settings, SettingLastError, err.Error())
		return res, err
	}
	if r.PruneLocal {
		all := append(packed, archivedLocally...)
		if len(all) > 0 {
			res.Pruned = r.prune(ctx, all)
		}
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

// prune 删除已经确认在归档里的本地副本。分批删：一次几千个 key 的批量写
// 会让 Pebble 长时间持锁，抓取那边会跟着卡住。
func (r *Runner) prune(ctx context.Context, keys []string) int {
	const batch = 500
	removed := 0
	for i := 0; i < len(keys); i += batch {
		end := i + batch
		if end > len(keys) {
			end = len(keys)
		}
		if err := r.Snaps.DeleteMany(ctx, keys[i:end]); err != nil {
			// 删不掉只是本地多留几页，不能因此判定归档失败。
			break
		}
		removed = end
	}
	// 删完立刻落盘：不刷的话，进程被强杀时这些删除会丢，
	// 下次启动快照「复活」，用户以为清理失败。
	if mt, ok := r.Snaps.(snapshot.Maintenance); ok {
		_ = mt.Flush(ctx)
	}
	return removed
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
	f, err := os.CreateTemp(r.TempDir, "stdseek-docs-*.jsonl.gz")
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
	man := r.Man
	if man == nil {
		man = NewManifest(r.Client)
	}
	e, ok, err := man.Lookup(ctx, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotArchived
	}
	return fetchEntry(ctx, r.Client, key, e)
}

// fetchEntry 按 manifest 条目取回一条记录，并校验 key 没串位。
func fetchEntry(ctx context.Context, c *objstore.Client, key string, e entry) ([]byte, error) {
	total := int64(8 + e.KLen + e.VLen)
	buf, err := c.GetRange(ctx, e.Part, e.Off, total)
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
	if !e.Z {
		return val, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(val))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// compressValue 只在体积明显变小的时候返回压缩结果。
// 阈值 1KB：太小的页面压完反而更大，白付一次解压成本。
func compressValue(val []byte) ([]byte, bool) {
	if len(val) < 1024 {
		return val, false
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return val, false
	}
	if _, err := zw.Write(val); err != nil {
		return val, false
	}
	if err := zw.Close(); err != nil {
		return val, false
	}
	if buf.Len() >= len(val) {
		return val, false
	}
	return buf.Bytes(), true
}

// contentHash 内容指纹（sha256 前 8 字节，16 个十六进制字符足够区分版本）。
// 用它判断「这一页重抓过没有」：只看 key 存在与否，重抓后的新正文永远传不上去。
func contentHash(val []byte) string {
	sum := sha256.Sum256(val)
	return hex.EncodeToString(sum[:8])
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
	f, err := os.CreateTemp(dir, "stdseek-pack-*.pack")
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
	Part string `json:"p"`           // 包对象名
	Off  int64  `json:"o"`           // 记录起始偏移
	KLen int    `json:"k"`           //
	VLen int    `json:"v"`           //
	H    string `json:"h,omitempty"` // 内容指纹，判断重抓过的页面要不要重传
	Z    bool   `json:"z,omitempty"` // 内容是否 gzip 过（逐条压缩，仍可随机读）
}

// shard 一份 manifest 分片。按 key 的 sha256 首字节分成 256 份，
// 单份保持在几十 KB：回源时只读一份，不用列举或下载全量索引。
type shard struct {
	Entries map[string]entry `json:"entries"`
	dirty   bool
}

// Manifest 带内存缓存的 manifest。
//
// 为什么要缓存：搜索结果要逐条判断「这页有没有快照」（markSnapshots），
// 一次搜索十几条，每条都去桶里读分片会让首屏变成秒级。
// 分片总量很小（20 万页约 20MB），常驻内存比反复请求划算得多。
type Manifest struct {
	c      *objstore.Client
	mu     sync.Mutex
	cache  map[string]*shard
	loaded map[string]bool
	warm   sync.Once
	// Reporter 分片上传进度（阶段, 已完成, 总数）。
	Reporter func(phase string, done, total int64)
}

// NewManifest 创建空缓存。
func NewManifest(c *objstore.Client) *Manifest {
	return &Manifest{c: c, cache: map[string]*shard{}, loaded: map[string]bool{}}
}

func shardName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:1]) + ".json"
}

func (m *Manifest) path(name string) string {
	return m.c.Config().ObjectKey(path.Join(idxDir, name))
}

// getByName 取一份分片；第一次访问时从桶里拉，不存在就建空的。
// 404 是常态（首次归档），不能当成错误，否则第一轮永远跑不起来。
func (m *Manifest) getByName(ctx context.Context, name string) (*shard, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded[name] {
		return m.cache[name], nil
	}
	sh := &shard{Entries: map[string]entry{}}
	buf, err := m.c.Get(ctx, m.path(name))
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
		return nil, err
	}
	m.cache[name] = sh
	m.loaded[name] = true
	return sh, nil
}

// Lookup 查一页在归档里的位置。
func (m *Manifest) Lookup(ctx context.Context, key string) (entry, bool, error) {
	sh, err := m.getByName(ctx, shardName(key))
	if err != nil {
		return entry{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := sh.Entries[key]
	return e, ok, nil
}

// Mark 在内存里登记一页（打包时用），Save 时统一上传。
func (m *Manifest) Mark(key string, e entry) {
	name := shardName(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	sh, ok := m.cache[name]
	if !ok {
		sh = &shard{Entries: map[string]entry{}}
		m.cache[name] = sh
		m.loaded[name] = true
	}
	sh.Entries[key] = e
	sh.dirty = true
}

// Save 只上传变化过的分片。
//
// 并发上传：分片最多 256 个，串行的话每个都要一次 TLS 握手 + 一个来回，
// 经代理时这一串能占掉几分钟——第一次归档「看起来卡住」多半卡在这里。
// 6 路并发足够，再高只是把上行挤成一条更细的管道。
func (m *Manifest) Save(ctx context.Context) error {
	type job struct {
		name string
		buf  []byte
	}
	m.mu.Lock()
	var jobs []job
	for name, sh := range m.cache {
		if sh == nil || !sh.dirty {
			continue
		}
		buf, err := json.Marshal(sh)
		if err != nil {
			m.mu.Unlock()
			return err
		}
		jobs = append(jobs, job{name: name, buf: buf})
	}
	m.mu.Unlock()
	if len(jobs) == 0 {
		return nil
	}

	const workers = 6
	var (
		next     atomic.Int64
		done     atomic.Int64
		firstErr atomic.Value
		wg       sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(jobs) || ctx.Err() != nil {
					return
				}
				if err := m.c.PutBytes(ctx, m.path(jobs[i].name), jobs[i].buf, "application/json"); err != nil {
					firstErr.CompareAndSwap(nil, err)
					return
				}
				n := done.Add(1)
				if m.Reporter != nil {
					m.Reporter("写索引", n, int64(len(jobs)))
				}
			}
		}()
	}
	wg.Wait()
	if e := firstErr.Load(); e != nil {
		return e.(error)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	for _, sh := range m.cache {
		if sh != nil {
			sh.dirty = false
		}
	}
	m.mu.Unlock()
	return nil
}

// Warm 后台把 256 份分片全部读进来，让第一次搜索不用等 20 次网络往返。
// 只做一次；失败静默（下一次 Lookup 还会再试）。
func (m *Manifest) Warm(ctx context.Context) {
	m.warm.Do(func() {
		go func() {
			for i := 0; i < 256; i++ {
				if ctx.Err() != nil {
					return
				}
				name := fmt.Sprintf("%02x.json", i)
				_, _ = m.getByName(ctx, name)
			}
		}()
	})
}

// Reader 云端只读视图：给「本地不保存」模式做回源。
type Reader struct {
	c   *objstore.Client
	man *Manifest
}

// NewReader 按配置创建读取器；cfg 必须已通过 Validate。
func NewReader(cfg objstore.Config, timeout time.Duration) *Reader {
	c := objstore.New(cfg, timeout)
	return &Reader{c: c, man: NewManifest(c)}
}

// Get 取回一页原文；not archived 返回 (nil,false,nil)。
// 网络/权限问题返回 error，调用方要能把「没有」和「读不到」分开告诉用户。
func (r *Reader) Get(ctx context.Context, key string) ([]byte, bool, error) {
	e, ok, err := r.man.Lookup(ctx, key)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	val, err := fetchEntry(ctx, r.c, key, e)
	if err != nil {
		return nil, false, err
	}
	return val, true, nil
}

// Has 判断一页是否在归档里（走内存缓存，不产生网络请求）。
func (r *Reader) Has(ctx context.Context, key string) (bool, error) {
	_, ok, err := r.man.Lookup(ctx, key)
	return ok, err
}

// Warm 后台预热 manifest。
func (r *Reader) Warm(ctx context.Context) { r.man.Warm(ctx) }

// Config 返回当前配置（用于判断配置有没有变）。
func (r *Reader) Config() objstore.Config { return r.c.Config() }

// Manifest 暴露 manifest，供打包复用同一份缓存。
// 打包与读取各带一份缓存的话，刚归档的页在读取侧仍然显示「没有」，直到缓存过期。
func (r *Reader) Manifest() *Manifest { return r.man }

// ---- 整库备份 ----

// 整库备份的配置与状态。频率单位是小时：24=每天，0=关闭。
const (
	SettingDBEvery  = "storage.backup.db_every_hours"
	SettingDBLastAt = "storage.backup.db_last_at"
	SettingDBKey    = "storage.backup.db_key"
	SettingDBKeys   = "storage.backup.db_keys" // JSON 数组，用于保留最近 N 份
	SettingDBSize   = "storage.backup.db_size"
)

// DBKeep 保留最近几份整库备份。再多也没用：真正需要回滚时只会挑最近的一两份，
// 而每份都是几十 MB 的静态费用。
const DBKeep = 7

// DumpFunc 生成一份 SQLite 快照到指定路径，返回字节数。
// 由 store.BackupTo 提供；用函数注入是为了让 backup 包不依赖数据库实现。
type DumpFunc func(ctx context.Context, path string) (int64, error)

// DBResult 一次整库备份的结果。
type DBResult struct {
	Key   string `json:"key"`
	Bytes int64  `json:"bytes"`
}

// DB 备份整库：导出 → gzip → 上传 → 只保留最近 DBKeep 份。
//
// 这是「恢复网站」需要的另一半：快照包只有页面原文，账号、白名单、反馈、
// 站点配置全在 SQLite 里，不备这一份，灾难恢复后站点是一个空壳。
func (r *Runner) DB(ctx context.Context, dump DumpFunc) (DBResult, error) {
	var res DBResult
	if r.Client == nil {
		return res, errors.New("未配置对象存储")
	}
	if err := r.Client.Config().Validate(); err != nil {
		return res, err
	}
	if dump == nil {
		return res, errors.New("没有可导出的数据库")
	}
	dir := r.TempDir
	raw, err := os.CreateTemp(dir, "stdseek-db-*.db")
	if err != nil {
		return res, err
	}
	rawPath := raw.Name()
	_ = raw.Close()
	defer os.Remove(rawPath)
	if _, err := dump(ctx, rawPath); err != nil {
		settingOrLog(ctx, r.Settings, SettingLastError, err.Error())
		return res, err
	}

	gzPath := rawPath + ".gz"
	gzf, err := os.Create(gzPath)
	if err != nil {
		return res, err
	}
	defer os.Remove(gzPath)
	zw, _ := gzip.NewWriterLevel(gzf, gzip.BestSpeed)
	src, err := os.Open(rawPath)
	if err != nil {
		gzf.Close()
		return res, err
	}
	if _, err := io.Copy(zw, src); err != nil {
		src.Close()
		zw.Close()
		gzf.Close()
		return res, err
	}
	src.Close()
	if err := zw.Close(); err != nil {
		gzf.Close()
		return res, err
	}
	if err := gzf.Close(); err != nil {
		return res, err
	}

	// 后缀用完整纳秒：以前是 UnixNano()%100000，而 1 毫秒 = 1e6 纳秒，
	// 恰好是 1e5 的整数倍——两次相隔整毫秒的备份会算出同一个后缀，
	// 后一次直接覆盖前一次（测试里连着备 8 次只剩 6 份就是这么来的）。
	now := time.Now().UTC()
	name := fmt.Sprintf("db/stdseek-%s-%s.db.gz",
		now.Format("20060102T150405Z"),
		strconv.FormatInt(now.UnixNano(), 36))
	key := r.Client.Config().ObjectKey(name)
	size, err := r.Client.PutFile(ctx, key, gzPath, "application/gzip")
	if err != nil {
		settingOrLog(ctx, r.Settings, SettingLastError, err.Error())
		return res, err
	}
	res = DBResult{Key: key, Bytes: size}

	keys := r.dbKeys(ctx)
	keys = append(keys, key)
	for len(keys) > DBKeep {
		old := keys[0]
		keys = keys[1:]
		// 删旧失败不影响本次备份成功：多留一份只是多花钱。
		_ = r.Client.Delete(ctx, old)
	}
	buf, _ := json.Marshal(keys)
	settingOrLog(ctx, r.Settings, SettingDBKeys, string(buf))
	settingOrLog(ctx, r.Settings, SettingDBKey, key)
	settingOrLog(ctx, r.Settings, SettingDBSize, strconv.FormatInt(size, 10))
	settingOrLog(ctx, r.Settings, SettingDBLastAt, time.Now().Format(time.RFC3339))
	settingOrLog(ctx, r.Settings, SettingLastError, "")
	return res, nil
}

// dbKeys 读出已备份的对象名列表（保序，最旧在前）。
func (r *Runner) dbKeys(ctx context.Context) []string {
	if r.Settings == nil {
		return nil
	}
	raw, err := r.Settings.Setting(ctx, SettingDBKeys, "")
	if err != nil || raw == "" {
		return nil
	}
	var keys []string
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return nil
	}
	return keys
}

// All 遍历所有已归档条目（逐分片，256 次以内）。
// 恢复时用它把整站页面从桶里捞回来——不需要先知道有哪些 URL。
func (m *Manifest) All(ctx context.Context, fn func(key string, e entry) error) error {
	for i := 0; i < 256; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		sh, err := m.getByName(ctx, fmt.Sprintf("%02x.json", i))
		if err != nil {
			return err
		}
		m.mu.Lock()
		batch := make(map[string]entry, len(sh.Entries))
		for k, v := range sh.Entries {
			batch[k] = v
		}
		m.mu.Unlock()
		for k, v := range batch {
			if err := fn(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// RestoreAll 把归档里的页面写回本地。
//
// skip 返回 true 表示本地已有这一页：**不覆盖**。新抓的内容可能比归档新，
// 恢复动作把旧版本盖上去，等于把刚修的页面改回上个月的快照。
func (r *Reader) RestoreAll(ctx context.Context, skip func(key string) (bool, error), put func(key string, val []byte) error) (int, int, error) {
	restored, skipped := 0, 0
	err := r.man.All(ctx, func(key string, e entry) error {
		ok, err := skip(key)
		if err != nil {
			return err
		}
		if ok {
			skipped++
			return nil
		}
		val, err := fetchEntry(ctx, r.c, key, e)
		if err != nil {
			return err
		}
		if err := put(key, val); err != nil {
			return err
		}
		restored++
		return nil
	})
	return restored, skipped, err
}
