package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postMode(t *testing.T, e testEnv, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/snapshot/mode", strings.NewReader(body))
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// 没开归档就选「只留云端」= 删了本地又没上传，快照就真没了。必须挡死。
func TestSnapshotModeRejectsCloudWithoutBackup(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	rec := postMode(t, e, `{"mode":"cloud"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("应 400，got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "先开启云冷备") {
		t.Fatalf("提示应说清要先后顺序：%s", rec.Body.String())
	}
}

func TestSnapshotModeRejectsUnknown(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	if rec := postMode(t, e, `{"mode":"drop"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("未知模式应 400，got %d", rec.Code)
	}
}

func TestSnapshotModeLocalAlwaysAllowed(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	rec := postMode(t, e, `{"mode":"local"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("切回本地应允许，got %d body=%s", rec.Code, rec.Body.String())
	}
	if e.srv.isCloudMode() {
		t.Fatal("local 模式不该打开回源开关")
	}
}
