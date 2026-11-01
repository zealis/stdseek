package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// 备份要「能恢复网站」：账号、白名单、反馈必须在，会话令牌不能进桶。
func TestBackupToKeepsDurableDataAndDropsSessions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "stdseek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.AddWhitelist(ctx, "keep.example.com", "blog", 3600); err != nil {
		t.Fatal(err)
	}
	// 造一个会话：备份里必须没有它（令牌泄露 = 别人能直接登录）。
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, uid, created_at, expires_at) VALUES ('sess-1', 1, 1, 9999999999)`); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	dst := filepath.Join(dir, "backup.db")
	size, err := st.BackupTo(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	if size == 0 {
		t.Fatal("备份文件不该是空的")
	}

	cp, err := sql.Open("sqlite", "file:"+dst)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var sessions, sites int
	if err := cp.QueryRow(`SELECT COUNT(1) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("会话不该进备份，实际 %d 条", sessions)
	}
	if err := cp.QueryRow(`SELECT COUNT(1) FROM whitelist WHERE domain='keep.example.com'`).Scan(&sites); err != nil {
		t.Fatal(err)
	}
	if sites != 1 {
		t.Fatalf("白名单应保留，实际 %d 条", sites)
	}
}

// 恢复要可回滚：先把现有库另存一份，再把备份换进去。
func TestRestoreDBReplacesAndKeepsPrevious(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "stdseek.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddWhitelist(ctx, "old.example.com", "blog", 3600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "backup.db")
	if _, err := st.BackupTo(ctx, dst); err != nil {
		t.Fatal(err)
	}
	// 备份之后又加了一个站：恢复应该把它抹掉（回到备份那一刻）。
	if err := st.AddWhitelist(ctx, "new.example.com", "blog", 3600); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	saved, err := RestoreDB(dir, dst)
	if err != nil {
		t.Fatal(err)
	}
	if saved == "" {
		t.Fatal("应把现有库另存一份")
	}

	after, err := Open(filepath.Join(dir, "stdseek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	n, _, err := after.ListWhitelist(ctx, "new.example.com", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 0 {
		t.Fatal("恢复后不该有备份之后新增的站点")
	}
	o, _, err := after.ListWhitelist(ctx, "old.example.com", 10, 0)
	if err != nil || len(o) != 1 {
		t.Fatalf("恢复后应保留备份时刻的站点：%v %v", o, err)
	}
}

func TestRestoreDBRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.db")
	if err := os.WriteFile(bad, []byte("not a sqlite file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreDB(dir, bad); err == nil {
		t.Fatal("不是 SQLite 的文件应该被拒绝")
	}
}
