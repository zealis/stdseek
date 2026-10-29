package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"voidxo/internal/kv"
	"voidxo/internal/objstore"
	"voidxo/internal/snapshot"
	"voidxo/internal/store"
)

type memSettings struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemSettings() *memSettings { return &memSettings{m: map[string]string{}} }

func (s *memSettings) Setting(_ context.Context, key, def string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[key]; ok {
		return v, nil
	}
	return def, nil
}

func (s *memSettings) SetSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

// fakeS3 存对象的最小 S3：PUT 存、GET 支持 Range、HEAD 判存在。
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
}

func newFakeS3() (*fakeS3, *httptest.Server) {
	f := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			f.objects[key] = body
			f.puts++
			w.WriteHeader(http.StatusOK)
		case http.MethodHead:
			if _, ok := f.objects[key]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			body, ok := f.objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if rng := r.Header.Get("Range"); rng != "" {
				parts := strings.SplitN(strings.TrimPrefix(rng, "bytes="), "-", 2)
				start, _ := strconv.Atoi(parts[0])
				end, _ := strconv.Atoi(parts[1])
				if start < 0 || end >= len(body) || start > end {
					w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return
				}
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(body[start : end+1])
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		case http.MethodDelete:
			delete(f.objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	return f, srv
}

func setupWithStore(t *testing.T, partBytes int64) (*Runner, *fakeS3, []string, map[string][]byte, *kv.Store) {
	t.Helper()
	dir := t.TempDir()
	k, err := kv.Open(filepath.Join(dir, "kv"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	pages := map[string][]byte{
		"page:aaa": bytes.Repeat([]byte("甲"), 200),
		"page:bbb": bytes.Repeat([]byte("乙"), 200),
		"page:ccc": bytes.Repeat([]byte("丙"), 200),
	}
	keys := make([]string, 0, len(pages))
	for key, val := range pages {
		if err := k.Put(key, val); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	f, srv := newFakeS3()
	t.Cleanup(srv.Close)
	cfg := objstore.Config{
		Endpoint: srv.URL, Bucket: "bucket", Region: "us-test-1",
		AccessKey: "AK", SecretKey: "SK", Prefix: "snapshots", PathStyle: true,
	}
	r := &Runner{
		Snaps: snapshot.NewLocal(k), Client: objstore.New(cfg, 5*time.Second),
		Settings: newMemSettings(), PartBytes: partBytes, TempDir: dir,
	}
	return r, f, keys, pages, k
}

func setup(t *testing.T, partBytes int64) (*Runner, *fakeS3, []string, map[string][]byte) {
	t.Helper()
	r, f, keys, pages, _ := setupWithStore(t, partBytes)
	return r, f, keys, pages
}

func TestPackUploadsRangesAndIsIdempotent(t *testing.T) {
	// 上限 300 字节，三页必然被切到多个包里——顺便验证跨包读取。
	r, f, keys, pages := setup(t, 300)
	ctx := context.Background()

	res, err := r.Pack(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Keys != 3 {
		t.Fatalf("归档页数=%d want 3", res.Keys)
	}
	if res.Parts < 2 {
		t.Fatalf("包数=%d，上限 300 字节应该切包", res.Parts)
	}

	// 每一页都要能按 manifest 的范围读回来。
	for _, key := range keys {
		got, err := r.Fetch(ctx, key)
		if err != nil {
			t.Fatalf("取回 %s 失败：%v", key, err)
		}
		if !bytes.Equal(got, pages[key]) {
			t.Fatalf("取回内容与原文不一致：%s", key)
		}
	}

	// 再跑一轮：全部跳过，不产生新包。
	before := f.puts
	res2, err := r.Pack(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Keys != 0 || res2.Skipped != 3 {
		t.Fatalf("第二轮应全部跳过：%+v", res2)
	}
	if f.puts != before {
		t.Fatalf("第二轮不该上传任何东西，PUT 次数 %d → %d", before, f.puts)
	}

	st := ReadStatus(ctx, r.Settings)
	if st.Keys != 3 || st.Parts < 2 || st.LastRun == "" || st.LastError != "" {
		t.Fatalf("状态没落库：%+v", st)
	}
}

func TestFetchMissingReportsNotArchived(t *testing.T) {
	r, _, _, _ := setup(t, 1<<20)
	if _, err := r.Fetch(context.Background(), "page:nope"); err != ErrNotArchived {
		t.Fatalf("未归档应返回 ErrNotArchived，实际 %v", err)
	}
}

func TestExportWritesJSONL(t *testing.T) {
	r, f, _, _ := setup(t, 1<<20)
	docs := []store.ExportDoc{
		{URL: "https://a.example/1", Domain: "a.example", Title: "标题一", Labels: []string{"ugc_platform"}},
		{URL: "https://b.example/2", Domain: "b.example", Title: "标题二", Labels: []string{}},
	}
	src := func(_ context.Context, fn func(store.ExportDoc) error) (int, error) {
		for _, d := range docs {
			if err := fn(d); err != nil {
				return 0, err
			}
		}
		return len(docs), nil
	}
	res, err := r.Export(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Docs != 2 || res.Bytes == 0 {
		t.Fatalf("导出结果不对：%+v", res)
	}
	if !strings.HasPrefix(res.Key, "snapshots/exports/") {
		t.Fatalf("导出对象名=%q", res.Key)
	}
	f.mu.Lock()
	raw := f.objects[res.Key]
	f.mu.Unlock()
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("JSONL 行数=%d want 2：%s", len(lines), body)
	}
	var got store.ExportDoc
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got.URL != docs[0].URL || got.Title != "标题一" || len(got.Labels) != 1 {
		t.Fatalf("导出行内容不对：%+v", got)
	}
	if st := ReadStatus(context.Background(), r.Settings); st.ExportDocs != 2 || st.ExportedAt == "" {
		t.Fatalf("导出状态没落库：%+v", st)
	}
}

func TestPackRequiresConfig(t *testing.T) {
	r, _, _, _ := setup(t, 1<<20)
	r.Client = objstore.New(objstore.Config{}, time.Second)
	if _, err := r.Pack(context.Background()); err == nil {
		t.Fatal("没有配置时应该报错")
	}
}

// 重抓过的页面必须重传：只看 key 存在与否会把新正文永远留在本地、
// 云端一直是旧版本，恢复时才发现丢了一天。
func TestPackReuploadsChangedContent(t *testing.T) {
	r, _, keys, _ := setup(t, 1<<20)
	ctx := context.Background()
	if _, err := r.Pack(ctx); err != nil {
		t.Fatal(err)
	}
	newer := bytes.Repeat([]byte("新"), 150)
	if err := r.Snaps.Put(ctx, "page:aaa", newer); err != nil {
		t.Fatal(err)
	}
	res, err := r.Pack(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Keys != 1 || res.Skipped != 2 {
		t.Fatalf("应只重传改动的那一页：%+v", res)
	}
	got, err := r.Fetch(ctx, "page:aaa")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newer) {
		t.Fatalf("云端应是最新内容，取回 %d 字节", len(got))
	}
	_ = keys
}

// 云端为主：上传成功后删掉本地副本，且删完仍然读得回来。
func TestPackPrunesLocalButKeepsCloudReadable(t *testing.T) {
	r, _, keys, pages, k := setupWithStore(t, 1<<20)
	r.PruneLocal = true
	ctx := context.Background()
	res, err := r.Pack(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pruned != 3 {
		t.Fatalf("应删掉 3 个本地副本，实际 %+v", res)
	}
	if n, _ := k.Count("page:"); n != 0 {
		t.Fatalf("本地应已清空，剩 %d 条", n)
	}
	for _, key := range keys {
		got, err := r.Fetch(ctx, key)
		if err != nil {
			t.Fatalf("本地删了之后云端仍要能读回 %s：%v", key, err)
		}
		if !bytes.Equal(got, pages[key]) {
			t.Fatalf("取回内容不一致：%s", key)
		}
	}

	// 再跑一轮：本地已经空了，不该产生新包或误删云端数据。
	before := res.Parts
	res2, err := r.Pack(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Parts != 0 || res2.Keys != 0 || res2.Pruned != 0 {
		t.Fatalf("空本地不该有动作：%+v（上一轮 %d 包）", res2, before)
	}
}

// 云端为主模式开启的第一次：已经归档过的老页面（内容没变）也要清掉，
// 否则存量永远留在本地，用户以为开了却没省下磁盘。
func TestPruneRemovesAlreadyArchivedPages(t *testing.T) {
	r, _, _, _, k := setupWithStore(t, 1<<20)
	ctx := context.Background()
	if _, err := r.Pack(ctx); err != nil {
		t.Fatal(err)
	}
	// 同一份 manifest（缓存命中），只打开 prune，模拟后台刚切到只留云端。
	r.PruneLocal = true
	res, err := r.Pack(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Keys != 0 || res.Pruned != 3 {
		t.Fatalf("应只清理不重传：%+v", res)
	}
	if n, _ := k.Count("page:"); n != 0 {
		t.Fatalf("本地应已清空，剩 %d 条", n)
	}
}

// 整库备份：上传成功、按时间保留最近 DBKeep 份（更旧的从桶里删掉）。
func TestDBBackupKeepsOnlyRecentCopies(t *testing.T) {
	r, f, _, _ := setup(t, 1<<20)
	ctx := context.Background()
	dump := func(_ context.Context, path string) (int64, error) {
		data := bytes.Repeat([]byte("db"), 100)
		return int64(len(data)), os.WriteFile(path, data, 0o644)
	}
	for i := 0; i < DBKeep+2; i++ {
		if _, err := r.DB(ctx, dump); err != nil {
			t.Fatalf("第 %d 次备份失败：%v", i+1, err)
		}
	}
	f.mu.Lock()
	n := 0
	for k := range f.objects {
		if strings.HasPrefix(k, "snapshots/db/") {
			n++
		}
	}
	f.mu.Unlock()
	if n != DBKeep {
		t.Fatalf("桶里应只剩 %d 份，实际 %d", DBKeep, n)
	}
	if st := ReadStatus(ctx, r.Settings); st.DBParts != DBKeep || st.DBKey == "" || st.DBLastAt == "" {
		t.Fatalf("状态没落库：%+v", st)
	}
}

func TestDBBackupRequiresConfig(t *testing.T) {
	r, _, _, _ := setup(t, 1<<20)
	r.Client = objstore.New(objstore.Config{}, time.Second)
	if _, err := r.DB(context.Background(), func(context.Context, string) (int64, error) { return 0, nil }); err == nil {
		t.Fatal("没有配置时应该报错")
	}
}
