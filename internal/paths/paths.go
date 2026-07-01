// Package paths 提供路径归一化与 object 存储路径计算工具。
package paths

import (
	"crypto/sha1"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// Sanitized 将可能含非法字符/超长的路径转为安全的单段文件名。
// 用于冲突文件的存放路径，避免目录穿越与非法字符。
// 非法字符替换为 _，超长路径截断并追加短哈希后缀。
func Sanitized(p string) string {
	s := sanitizeName(p)
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
// 非法 objectKey（含非 hex 字符如 ../）返回空字符串，调用方应检查。
func ObjectPath(objectsRoot, objectKey string) string {
	hex := strings.TrimPrefix(objectKey, "h3:")
	if !isValidHex(hex) || len(hex) < 4 {
		return ""
	}
	bucket1 := hex[:2]
	bucket2 := hex[:4]
	return filepath.Join(objectsRoot, bucket1, bucket2, hex)
}

// ObjectBuckets 从 objectKey 提取两层分桶目录名。
// 非法 objectKey 返回空字符串。
func ObjectBuckets(objectKey string) (bucket1, bucket2 string) {
	hex := strings.TrimPrefix(objectKey, "h3:")
	if !isValidHex(hex) || len(hex) < 4 {
		return "", ""
	}
	return hex[:2], hex[:4]
}

// isValidHex 判断字符串是否为纯十六进制（防路径穿越：../ 等非 hex 字符被拒绝）。
func isValidHex(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
