package kv

import (
	"path/filepath"
	"testing"
)

func TestPutGetDelete(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	key := Key("go.dev/doc/error")
	if _, found, err := s.Get(key); err != nil || found {
		t.Fatalf("empty store should not find key: found=%v err=%v", found, err)
	}
	if err := s.Put(key, []byte("<html>snapshot</html>")); err != nil {
		t.Fatal(err)
	}
	v, found, err := s.Get(key)
	if err != nil || !found || string(v) != "<html>snapshot</html>" {
		t.Fatalf("get=%q found=%v err=%v", v, found, err)
	}
	if err := s.Delete(key); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.Get(key); err != nil || found {
		t.Fatalf("deleted key still present: found=%v err=%v", found, err)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kv")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := Key("example.com/page")
	if err := s.Put(key, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	v, found, err := s2.Get(key)
	if err != nil || !found || string(v) != "v1" {
		t.Fatalf("after reopen get=%q found=%v err=%v", v, found, err)
	}
}
