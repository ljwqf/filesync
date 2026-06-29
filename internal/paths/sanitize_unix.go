//go:build !windows

package paths

import "strings"

// sanitizeName 将 Unix 不安全字符替换为 _。
// Unix 文件名仅不允许 / 和 \0，但为安全起见也剥离 \ 和 .. 防止路径穿越。
func sanitizeName(p string) string {
	r := strings.NewReplacer(
		`\`, "_",
		`/`, "_",
		`\x00`, "_",
		`..`, "_",
	)
	return r.Replace(p)
}
