// Package hasher 计算文件内容的 xxh3 哈希作为 objectKey。
package hasher

import (
	"fmt"
	"io"
	"os"

	"github.com/zeebo/xxh3"
)

const (
	// KeyPrefix 是 objectKey 的算法前缀（xxh3）。
	KeyPrefix = "h3:"
	// EmptyObjectKey 是空文件的固定 objectKey（xxh3 对空内容的 128 位哈希）。
	EmptyObjectKey = KeyPrefix + "99aa06d3014798d86001c324468d497f"
)

// Hasher 计算内容哈希。
type Hasher interface {
	Hash(r io.Reader) (objectKey string, err error)
	HashFile(path string) (objectKey string, err error)
}

// xxh3Hasher 是默认的 xxh3 实现。
type xxh3Hasher struct{}

// New 创建默认 xxh3 Hasher。
func New() Hasher {
	return xxh3Hasher{}
}

// Hash 读取 r 全部内容计算 xxh3，返回 "h3:<hex>"。
func (xxh3Hasher) Hash(r io.Reader) (string, error) {
	h := xxh3.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("hash read: %w", err)
	}
	sum := h.Sum128()
	// xxh3 128 位 -> 32 hex 字符
	return KeyPrefix + hexEncode(sum.Hi, sum.Lo), nil
}

// HashFile 打开文件并计算哈希。
func (x xxh3Hasher) HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	return x.Hash(f)
}

// hexEncode 将高低 64 位拼为 hex（Hi 在前，标准大端顺序）。
func hexEncode(hi, lo uint64) string {
	const digits = "0123456789abcdef"
	b := make([]byte, 32)
	// 高 64 位（前 16 字节）
	for i := 15; i >= 0; i-- {
		b[i] = digits[hi&0xf]
		hi >>= 4
	}
	// 低 64 位（后 16 字节）
	for i := 31; i >= 16; i-- {
		b[i] = digits[lo&0xf]
		lo >>= 4
	}
	return string(b)
}
