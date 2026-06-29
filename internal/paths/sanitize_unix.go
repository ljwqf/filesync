//go:build !windows

package paths

import "strings"

// sanitizeName 将 Unix 不安全字符替换为 _。
// Unix 文件名不允许 / 和 \0，不允许路径穿越 ..，但反斜杠 \ 是合法字符，保留。
func sanitizeName(p string) string {
	r := strings.NewReplacer(
		`/`, "_",
		`\x00`, "_",
		`..`, "_",
	)
	return r.Replace(p)
}
