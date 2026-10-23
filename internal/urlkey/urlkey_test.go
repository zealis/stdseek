package urlkey

import "testing"

func TestKeyNormalization(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://Go.dev/doc/?x=1", "go.dev/doc"},
		{"https://go.dev:443/doc", "go.dev/doc"},
		{"http://go.dev:80/doc/", "go.dev/doc"},
		{"http://go.dev:8080/doc", "go.dev:8080/doc"},
		{"https://go.dev/", "go.dev"},
		{"https://go.dev", "go.dev"},
		{"https://www.infoq.cn/content-center/#about", "www.infoq.cn/content-center"},
		{"https://www.infoq.cn/content-center/", "www.infoq.cn/content-center"},
		{"http://127.0.0.1:8001/", "127.0.0.1:8001"},
		{"http://127.0.0.1:8002/", "127.0.0.1:8002"},
		{"", ""},
		{"not a url", ""},
	}
	for _, c := range cases {
		if got := Key(c.in); got != c.want {
			t.Errorf("Key(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 同一主机的不同端口必须是不同 key，否则两个服务在索引里互相覆盖。
func TestKeyKeepsDistinctPorts(t *testing.T) {
	a := Key("http://127.0.0.1:8001/")
	b := Key("http://127.0.0.1:8002/")
	if a == b {
		t.Fatalf("different ports must not collide: %q == %q", a, b)
	}
}

func TestKeysPrefixes(t *testing.T) {
	got := Keys("https://go.dev/doc/x/")
	want := []string{"go.dev", "go.dev/doc", "go.dev/doc/x"}
	if len(got) != len(want) {
		t.Fatalf("Keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Keys[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if Keys("") != nil {
		t.Fatal("empty url should return nil")
	}
}
