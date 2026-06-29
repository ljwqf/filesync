//go:build !windows

package paths

import "strings"

// sanitizeName 将 Unix 不安全字符替换为 _。
// Unix 文件名不允许 / 和 \0 字节，反斜杠 \ 与 .. 均为合法文件名，保留。
func sanitizeName(p string) string {
	r := strings.NewReplacer(
		`/`, "_",
		`\x00`, "_",
	)
	return r.Replace(p)
}
