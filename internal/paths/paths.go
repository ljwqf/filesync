// Package paths 提供 Windows 长路径（\\?\ 前缀）与路径归一化工具。
package paths

import (
	"crypto/sha1"
	"encoding/hex"
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

// Sanitized 将可能含非法字符/超长的路径转为安全的单段文件名。
// 用于冲突文件的存放路径，避免目录穿越与非法字符。
// 非法字符替换为 _，超长路径截断并追加短哈希后缀。
func Sanitized(p string) string {
	// 去掉卷标冒号、路径分隔符、.. 等
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
	s := r.Replace(p)
	// 控制长度（留余地给后续路径拼接）
	const maxLen = 150
	if len(s) > maxLen {
		h := sha1.Sum([]byte(p))
		hexStr := hex.EncodeToString(h[:])[:8]
		s = s[:maxLen-9] + "_" + hexStr
	}
	return s
}

// ObjectPath 根据 objectKey 计算两层分桶的 object 存储路径。
// objectKey 格式 "h3:<hex>"；bucket1 = hex 前2字符，bucket2 = hex 前4字符。
// 物理文件名用纯 hex（Windows 文件名不允许冒号），objectKey 的 "h3:" 前缀仅用于索引 key。
// objectsRoot 是 objects 目录的绝对路径。
func ObjectPath(objectsRoot, objectKey string) string {
	hex := strings.TrimPrefix(objectKey, "h3:")
	if len(hex) < 4 {
		// 不足4字符时用完整 hex 补齐
		for len(hex) < 4 {
			hex += "0"
		}
	}
	bucket1 := hex[:2]
	bucket2 := hex[:4]
	return filepath.Join(objectsRoot, bucket1, bucket2, hex)
}

// ObjectBuckets 从 objectKey 提取两层分桶目录名。
func ObjectBuckets(objectKey string) (bucket1, bucket2 string) {
	hex := strings.TrimPrefix(objectKey, "h3:")
	if len(hex) < 4 {
		for len(hex) < 4 {
			hex += "0"
		}
	}
	return hex[:2], hex[:4]
}
