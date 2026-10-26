package querysyntax

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Query
	}{
		{
			name: "包含与排除",
			in:   "+北京 -大学",
			want: Query{Text: "北京", Required: []string{"北京"}, Excluded: []string{"大学"}},
		},
		{
			name: "两个都包含",
			in:   "+北京 +大学",
			want: Query{Text: "北京 大学", Required: []string{"北京", "大学"}},
		},
		{
			name: "短语与普通词",
			in:   `"分布式系统" 原理`,
			want: Query{Text: "分布式系统 原理", Terms: []string{"原理"}, Phrases: []string{"分布式系统"}},
		},
		{
			name: "中文引号",
			in:   "“北京大学” 招生",
			want: Query{Text: "北京大学 招生", Terms: []string{"招生"}, Phrases: []string{"北京大学"}},
		},
		{
			name: "site 语法",
			in:   "site:php.net 手册",
			want: Query{Text: "手册", Terms: []string{"手册"}, Site: "php.net"},
		},
		{
			name: "site 粘贴整条 URL",
			in:   "site:https://www.php.net/manual/en/",
			want: Query{Site: "www.php.net"},
		},
		{
			name: "site 大小写归一",
			in:   "site:PHP.NET",
			want: Query{Site: "php.net"},
		},
		{
			name: "没有语法时原样进正文",
			in:   "docker 构建镜像",
			want: Query{Text: "docker 构建镜像", Terms: []string{"docker", "构建镜像"}},
		},
		{
			name: "孤立的加减号被忽略",
			in:   "+ - 北京",
			want: Query{Text: "北京", Terms: []string{"北京"}},
		},
		{
			name: "site 不像域名时当普通词",
			in:   "site:abc 说明",
			want: Query{Text: "site:abc 说明", Terms: []string{"site:abc", "说明"}},
		},
		{
			name: "带引号的排除短语",
			in:   `-"广告 推广" 教程`,
			want: Query{Text: "教程", Terms: []string{"教程"}, Excluded: []string{"广告 推广"}},
		},
	}

	for _, c := range cases {
		got := Parse(c.in)
		want := c.want
		want.Raw = c.in
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Parse(%q)\n got %+v\nwant %+v", c.name, c.in, got, want)
		}
	}
}

func TestHasSyntax(t *testing.T) {
	if Parse("docker 镜像").HasSyntax() {
		t.Error("普通查询不该被判定为含语法")
	}
	for _, in := range []string{"+北京", "-大学", `"短语"`, "site:php.net"} {
		if !Parse(in).HasSyntax() {
			t.Errorf("%q 应判定为含语法", in)
		}
	}
}
