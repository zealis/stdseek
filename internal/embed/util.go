package embed

import (
	"encoding/json"
	"os"
)

// readJSON 读取并解析 JSON 文件
func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// fileExists 判断路径存在且不是目录
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
