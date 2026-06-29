//go:build !windows

package paths

// Long 在 Unix 上为 no-op，直接返回原路径。
// Unix 文件系统无 260 字符限制，不需要长路径前缀。
func Long(p string) string {
	return p
}

// IsLong 在 Unix 上始终返回 false。
func IsLong(p string) bool {
	return false
}
