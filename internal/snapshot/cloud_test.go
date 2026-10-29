package snapshot

import (
	"context"
	"errors"
	"testing"
)

// memStore 最小的本地存储替身。
type memStore struct {
	data map[string][]byte
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := m.data[key]
	return v, ok, nil
}
func (m *memStore) Put(_ context.Context, key string, val []byte) error {
	m.data[key] = val
	return nil
}
func (m *memStore) DeleteMany(_ context.Context, keys []string) error {
	for _, k := range keys {
		delete(m.data, k)
	}
	return nil
}
func (m *memStore) Count(_ context.Context, prefix string) (int, error) { return len(m.data), nil }
func (m *memStore) Has(_ context.Context, key string) (bool, error) {
	_, ok := m.data[key]
	return ok, nil
}
func (m *memStore) Size() int64    { return 0 }
func (m *memStore) Version() int64 { return 0 }
func (m *memStore) Scan(_ context.Context, _ string, fn func(string, []byte) error) error {
	for k, v := range m.data {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}
func (m *memStore) Compact(context.Context, string) error { return nil }

// fakeRemote 云端替身。
type fakeRemote struct {
	data  map[string][]byte
	err   error
	calls int
}

func (f *fakeRemote) Get(_ context.Context, key string) ([]byte, bool, error) {
	f.calls++
	if f.err != nil {
		return nil, false, f.err
	}
	v, ok := f.data[key]
	return v, ok, nil
}
func (f *fakeRemote) Has(_ context.Context, key string) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	_, ok := f.data[key]
	return ok, nil
}

// 本地有就绝不发网络请求：检索路径上的行为不能因为配了桶而变慢。
func TestCloudPrefersLocal(t *testing.T) {
	local := &memStore{data: map[string][]byte{"page:a": []byte("本地")}}
	remote := &fakeRemote{data: map[string][]byte{"page:a": []byte("云端")}}
	s := NewCloud(local, remote, func() bool { return true })
	v, ok, err := s.Get(context.Background(), "page:a")
	if err != nil || !ok || string(v) != "本地" {
		t.Fatalf("应返回本地内容：%q %v %v", v, ok, err)
	}
	if remote.calls != 0 {
		t.Fatalf("本地命中不该访问云端，实际 %d 次", remote.calls)
	}
}

// 只留云端：本地没有就回源。
func TestCloudFallsBackToRemote(t *testing.T) {
	local := &memStore{data: map[string][]byte{}}
	remote := &fakeRemote{data: map[string][]byte{"page:a": []byte("云端")}}
	s := NewCloud(local, remote, func() bool { return true })
	v, ok, err := s.Get(context.Background(), "page:a")
	if err != nil || !ok || string(v) != "云端" {
		t.Fatalf("应从云端取回：%q %v %v", v, ok, err)
	}
	has, err := s.Has(context.Background(), "page:a")
	if err != nil || !has {
		t.Fatalf("Has 应走云端：%v %v", has, err)
	}
}

// 没开「只留云端」时，本地没有就是没有——读路径保持纯本地。
func TestCloudDoesNotTouchRemoteInLocalMode(t *testing.T) {
	local := &memStore{data: map[string][]byte{}}
	remote := &fakeRemote{data: map[string][]byte{"page:a": []byte("云端")}}
	s := NewCloud(local, remote, func() bool { return false })
	if _, ok, err := s.Get(context.Background(), "page:a"); ok || err != nil {
		t.Fatalf("本地模式不该回源：%v %v", ok, err)
	}
	if remote.calls != 0 {
		t.Fatalf("不该访问云端，实际 %d 次", remote.calls)
	}
}

// 云端读失败要和「没有这一页」分开：前者过会儿再试，后者要引导补抓。
func TestCloudMarksRemoteErrors(t *testing.T) {
	local := &memStore{data: map[string][]byte{}}
	remote := &fakeRemote{err: errors.New("connection reset")}
	s := NewCloud(local, remote, func() bool { return true })
	if _, _, err := s.Get(context.Background(), "page:a"); !errors.Is(err, ErrRemote) {
		t.Fatalf("应包成 ErrRemote，实际 %v", err)
	}
}

// 本地写入永远落本地：抓取不能被网络拖住。
func TestCloudPutStaysLocal(t *testing.T) {
	local := &memStore{data: map[string][]byte{}}
	remote := &fakeRemote{data: map[string][]byte{}}
	s := NewCloud(local, remote, func() bool { return true })
	if err := s.Put(context.Background(), "page:a", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if string(local.data["page:a"]) != "x" {
		t.Fatal("Put 应写本地")
	}
	if remote.calls != 0 {
		t.Fatalf("Put 不该访问云端，实际 %d 次", remote.calls)
	}
}
