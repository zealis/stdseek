package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 导出节奏：从没导过要导，24 小时内不重导，超过就再导。
// 这条判断决定「对象存储上每天多一份 JSONL」还是「一天几十份」。
func TestDueExport(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		last string
		want bool
	}{
		{"", true},
		{now.Add(-23 * time.Hour).Format(time.RFC3339), false},
		{now.Add(-24 * time.Hour).Format(time.RFC3339), true},
		{now.Add(-48 * time.Hour).Format(time.RFC3339), true},
		{"不是时间", true}, // 解析不了就重新导一次，不能因为一条脏值永远不导出
	}
	for _, c := range cases {
		if got := dueExport(c.last, now); got != c.want {
			t.Fatalf("dueExport(%q)=%v want %v", c.last, got, c.want)
		}
	}
}

// 整库备份的频率判断：0 关闭、到点才跑、没跑过就跑。
func TestDueEvery(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		last  string
		every int
		want  bool
	}{
		{"", 0, false}, // 关闭
		{"", 24, true}, // 从没备过
		{now.Add(-23 * time.Hour).Format(time.RFC3339), 24, false}, // 还没到点
		{now.Add(-25 * time.Hour).Format(time.RFC3339), 24, true},  // 到点
		{now.Add(-100 * time.Hour).Format(time.RFC3339), 168, false},
		{"坏数据", 24, true}, // 解析不了就重跑一次
	}
	for _, c := range cases {
		if got := dueEvery(c.last, c.every, now); got != c.want {
			t.Fatalf("dueEvery(%q,%d)=%v want %v", c.last, c.every, got, c.want)
		}
	}
}

func TestDBBackupEveryValidation(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/snapshot/db", strings.NewReader(body))
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(`{"every_hours":3}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("3 小时太密应被拒，got %d", rec.Code)
	}
	if rec := post(`{"every_hours":200}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("200 小时超出范围应被拒，got %d", rec.Code)
	}
	rec := post(`{"every_hours":72}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("72 小时应接受，got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"db_every_hours":72`) {
		t.Fatalf("响应应回显新频率：%s", rec.Body.String())
	}
}
