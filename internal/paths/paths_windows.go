//go:build windows

package paths

import (
	"path/filepath"
	"strings"
)

// longPrefix 是 Win32 长路径前缀。
const longPrefix = `\\?\`

// uncLongPrefix 是 UNC 路径的长路径前缀。
const uncLongPrefix = `\\?\UNC\`

// Long 将路径转换为 \\?\ 长路径形式，绕过 Windows 260 字符限制。
// 已带前缀的路径原样返回。
func Long(p string) string {
	if p == "" {
		return p
	}
	if strings.HasPrefix(p, longPrefix) {
		return p
	}
	if strings.HasPrefix(p, `\\`) {
		// UNC 路径 \\server\share -> \\?\UNC\server\share
		return uncLongPrefix + strings.TrimPrefix(p, `\\`)
	}
	// 普通绝对路径直接加前缀
	if filepath.IsAbs(p) {
		return longPrefix + p
	}
	// 相对路径不处理（调用方应保证绝对路径）
	return p
}

// IsLong 判断路径是否已带长路径前缀。
func IsLong(p string) bool {
	return strings.HasPrefix(p, longPrefix)
}
