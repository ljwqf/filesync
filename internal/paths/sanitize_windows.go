//go:build windows

package paths

import "strings"

// sanitizeName 将 Windows 非法文件名字符替换为 _。
func sanitizeName(p string) string {
	r := strings.NewReplacer(
		`\`, "_",
		`/`, "_",
		`:`, "_",
		`..`, "_",
		`"`, "_",
		`<`, "_",
		`>`, "_",
		`|`, "_",
		`*`, "_",
		`?`, "_",
	)
	return r.Replace(p)
}
