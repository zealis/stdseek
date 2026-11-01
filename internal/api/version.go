package api

import (
	"net/http"
	"runtime"
)

// versionView 后台页脚/标题旁显示的那一行小字。
//
// 为什么从二进制里读而不是从 CHANGELOG：自托管部署最常见的问题是
// 「你说的问题在我这儿复现不了」——先确认跑的是哪个构建，比什么都省时间。
// 版本号在构建期用 -ldflags 注入（见 cmd/stdseek/main.go 顶部），
// 源码直接 go build 时回落到仓库根目录的 VERSION 文件。
type versionView struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
	Go      string `json:"go"`
}

// handleVersion GET /admin/version
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, versionView{
		Version: s.Version,
		Commit:  s.Commit,
		BuiltAt: s.BuiltAt,
		Go:      runtime.Version(),
	})
}
