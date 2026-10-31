package whois

import (
	"testing"
	"time"
)

// xxapi v2 的实际返回形态：data.registration_time（RFC3339）。
func TestParseXXAPI(t *testing.T) {
	body := []byte(`{"code":200,"msg":"数据请求成功","data":{"Domain Name":"github.com",
		"Registration Time":"2007-10-09T18:20:50Z",
		"data":{"registration_time":"2007-10-09T18:20:50Z"}}}`)
	got, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Year() != 2007 || got.Month() != time.October {
		t.Fatalf("unexpected registration time: %v", got)
	}
}

// 自定义接口的约定：JSON 任意字段带 ISO 日期即可，字段名不必对齐 xxapi。
func TestParseCustomShape(t *testing.T) {
	body := []byte(`{"ok":true,"result":{"regDate":"2015-03-01","other":"x"}}`)
	got, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Year() != 2015 {
		t.Fatalf("unexpected year: %v", got)
	}
}

// 脏数据防护：未来日期、1990 之前的值不采；没有日期就报错而不是给零值。
func TestParseRejectsJunk(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"registration_time":"2099-01-01"}`),
		[]byte(`{"registration_time":"1970-01-01"}`),
		[]byte(`{"foo":"bar"}`),
		[]byte(`not json`),
	}
	for i, body := range cases {
		if got, err := Parse(body); err == nil || !got.IsZero() {
			t.Errorf("case %d: expected error+zero, got %v err=%v", i, got, err)
		}
	}
}
