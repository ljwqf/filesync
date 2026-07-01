// Package cas 实现内容寻址对象存储与文件系统自适应放置。
// NTFS: object 永久保留只读，目标文件硬链接到 object（零额外空间）。
// exFAT: object 作临时中转，复制到目标后删除（空间与 1:1 持平）。
package cas

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ljwqf/filesync/internal/hasher"
	"github.com/ljwqf/filesync/internal/paths"
)

// StorageMode 表示目标文件系统支持的去重方式。
type StorageMode int

const (
	ModeHardlink StorageMode = iota // NTFS
	ModeCopy                        // exFAT/FAT32
)

// CAS 管理对象存储与目标文件放置。
type CAS interface {
	// EnsureObject 确保 objectKey 对应的 object 物理就绪。
	// NTFS: 不存在则从 srcAbsPath 拷入并设只读 0444；存在则复用。
	// exFAT: 不存在则从 srcAbsPath 拷入（临时）；存在则复用（本轮已拷过）。
	// 返回 exists=true 表示 object 已存在（复用），false 表示新拷入。
	EnsureObject(srcAbsPath, objectKey string) (exists bool, err error)
	// PlaceFileHardlink 创建从 object 到 destAbsPath 的硬链接（NTFS）。
	PlaceFileHardlink(objectKey, destAbsPath string) error
	// PlaceFileCopy 从 object 复制到 destAbsPath（exFAT 或强制）。
	PlaceFileCopy(objectKey, destAbsPath string) error
	// RemoveTempObject 删除临时 object（exFAT 拷后清理；NTFS no-op）。
	RemoveTempObject(objectKey string) error
	// DeleteObject 物理删除 object 文件（prune 与异步清理用）。
	DeleteObject(objectKey string) error
	// ListObjects 列出所有 object 的 objectKey（物理存在）。
	ListObjects() ([]string, error)
	// ObjectPath 返回 objectKey 对应的物理绝对路径。
	ObjectPath(objectKey string) string
	// Mode 返回当前去重模式。
	Mode() StorageMode
}

type fileCAS struct {
	targetRoot  string
	objectsRoot string
	mode        StorageMode
}

// New 创建 CAS。targetRoot 是目标盘根，objectsRoot 是 objects 目录绝对路径。
func New(targetRoot, objectsRoot string) (CAS, error) {
	mode := detectMode(objectsRoot)
	return &fileCAS{
		targetRoot:  targetRoot,
		objectsRoot: objectsRoot,
		mode:        mode,
	}, nil
}

func (c *fileCAS) Mode() StorageMode { return c.mode }

// validateObjectKey 校验 objectKey 格式合法，防止路径穿越攻击。
// 合法 objectKey 格式为 "h3:" + 纯十六进制字符串（xxh3 128位哈希 = 32 hex 字符）。
// 拒绝非十六进制字符（如 ../）可防止 filepath.Join 解析 .. 导致路径逃逸出 objectsRoot。
func validateObjectKey(objectKey string) error {
	hexPart := strings.TrimPrefix(objectKey, hasher.KeyPrefix)
	if hexPart == objectKey {
		return fmt.Errorf("objectKey missing %q prefix: %q", hasher.KeyPrefix, objectKey)
	}
	if len(hexPart) < 4 {
		return fmt.Errorf("objectKey hex too short (need >=4): %q", objectKey)
	}
	if !isHex(hexPart) {
		return fmt.Errorf("objectKey contains non-hex characters: %q", objectKey)
	}
	return nil
}

func (c *fileCAS) ObjectPath(objectKey string) string {
	if err := validateObjectKey(objectKey); err != nil {
		// 合法 objectKey 总是纯 hex（由 hasher.HashFile 生成）。
		// 到达此处说明索引被篡改或存在 bug，返回空路径使调用方报错而非穿越。
		return ""
	}
	hex := strings.TrimPrefix(objectKey, "h3:")
	if len(hex) < 4 {
		for len(hex) < 4 {
			hex += "0"
		}
	}
	// 物理文件名用纯 hex（Windows 文件名不允许冒号）；
	// objectKey 含 "h3:" 前缀仅用于索引 key 区分哈希算法。
	return filepath.Join(c.objectsRoot, hex[:2], hex[:4], hex)
}

func (c *fileCAS) EnsureObject(srcAbsPath, objectKey string) (bool, error) {
	if err := validateObjectKey(objectKey); err != nil {
		return false, fmt.Errorf("invalid objectKey: %w", err)
	}
	objPath := paths.Long(c.ObjectPath(objectKey))
	if _, err := os.Stat(objPath); err == nil {
		return true, nil // 已存在，复用
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat object %s: %w", objPath, err)
	}
	// 不存在，从源拷入
	if err := os.MkdirAll(filepath.Dir(objPath), 0755); err != nil {
		return false, fmt.Errorf("mkdir object dir: %w", err)
	}
	if err := copyFile(paths.Long(srcAbsPath), objPath, 0); err != nil {
		return false, fmt.Errorf("copy to object: %w", err)
	}
	// NTFS: object 设只读，防止误编辑污染所有硬链接
	if c.mode == ModeHardlink {
		if err := os.Chmod(objPath, 0444); err != nil {
			return false, fmt.Errorf("chmod object readonly: %w", err)
		}
	}
	return false, nil
}

func (c *fileCAS) PlaceFileHardlink(objectKey, destAbsPath string) error {
	if err := validateObjectKey(objectKey); err != nil {
		return fmt.Errorf("invalid objectKey: %w", err)
	}
	objPath := paths.Long(c.ObjectPath(objectKey))
	destLong := paths.Long(destAbsPath)
	// 覆盖预处理：dest 只读则先 chmod 可写
	prepareOverwrite(destLong)
	// 删除已存在的 dest
	if _, err := os.Stat(destLong); err == nil {
		if err := os.Remove(destLong); err != nil {
			return fmt.Errorf("remove existing dest: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(destLong), 0755); err != nil {
		return fmt.Errorf("mkdir dest dir: %w", err)
	}
	// object 只读，硬链接本身不受只读限制（链接创建不写 object 内容）
	if err := os.Link(objPath, destLong); err != nil {
		return fmt.Errorf("hardlink %s -> %s: %w", objPath, destLong, err)
	}
	return nil
}

func (c *fileCAS) PlaceFileCopy(objectKey, destAbsPath string) error {
	if err := validateObjectKey(objectKey); err != nil {
		return fmt.Errorf("invalid objectKey: %w", err)
	}
	objPath := paths.Long(c.ObjectPath(objectKey))
	destLong := paths.Long(destAbsPath)
	prepareOverwrite(destLong)
	if err := os.MkdirAll(filepath.Dir(destLong), 0755); err != nil {
		return fmt.Errorf("mkdir dest dir: %w", err)
	}
	if err := copyFile(objPath, destLong, 0644); err != nil {
		return fmt.Errorf("copy object to dest: %w", err)
	}
	return nil
}

func (c *fileCAS) RemoveTempObject(objectKey string) error {
	if c.mode == ModeHardlink {
		return nil // NTFS no-op
	}
	if err := validateObjectKey(objectKey); err != nil {
		return fmt.Errorf("invalid objectKey: %w", err)
	}
	objPath := paths.Long(c.ObjectPath(objectKey))
	// 文件已不存在视为成功：exFAT 崩溃恢复重试时前次可能已清理临时 object，
	// 此时 Chmod 会因 ENOENT 失败，必须在 Chmod 前短路，保证幂等。
	if _, err := os.Stat(objPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat temp object: %w", err)
	}
	// exFAT 临时 object，需先解除只读再删
	if err := os.Chmod(objPath, 0644); err != nil {
		return fmt.Errorf("chmod temp object: %w", err)
	}
	if err := os.Remove(objPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove temp object: %w", err)
	}
	return nil
}

func (c *fileCAS) DeleteObject(objectKey string) error {
	if err := validateObjectKey(objectKey); err != nil {
		return fmt.Errorf("invalid objectKey: %w", err)
	}
	objPath := paths.Long(c.ObjectPath(objectKey))
	// 文件已不存在视为成功：prune 重跑/崩溃恢复会重复删除同一 object，
	// 此时 Chmod 会因 ENOENT 失败，必须在 Chmod 前短路，保证容错（与 prune.go 注释契约一致）。
	if _, err := os.Stat(objPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat object: %w", err)
	}
	if err := os.Chmod(objPath, 0644); err != nil {
		return fmt.Errorf("chmod object: %w", err)
	}
	if err := os.Remove(objPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete object %s: %w", objPath, err)
	}
	return nil
}

func (c *fileCAS) ListObjects() ([]string, error) {
	var keys []string
	err := filepath.Walk(c.objectsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		// 物理文件名是纯 hex，还原为 objectKey（"h3:<hex>"）
		hex := filepath.Base(path)
		if isHex(hex) {
			keys = append(keys, "h3:"+hex)
		}
		return nil
	})
	return keys, err
}

// isHex 判断字符串是否为纯十六进制（object 物理文件名）。
func isHex(s string) bool {
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

// detectMode 检测目标文件系统是否支持硬链接。
func detectMode(path string) StorageMode {
	return DetectMode(path)
}

// DetectMode 检测 path 所在文件系统是否支持硬链接（导出版，供 dedup 等外部包使用）。
// 使用带随机后缀的临时目录避免并发调用时互相干扰。
func DetectMode(path string) StorageMode {
	// 创建带随机后缀的临时目录，避免并发调用间的竞态
	testDir, cleanup, ok := makeUniqueTestDir()
	if !ok {
		return ModeCopy // 无法创建，保守用 copy
	}
	defer cleanup()
	src := filepath.Join(testDir, "src")
	dst := filepath.Join(testDir, "dst")
	if err := os.WriteFile(src, []byte("x"), 0644); err != nil {
		return ModeCopy
	}
	if err := os.Link(src, dst); err != nil {
		return ModeCopy // 不支持硬链接
	}
	return ModeHardlink
}

// makeUniqueTestDir 在系统临时目录下创建带随机后缀的测试目录，返回目录路径、清理函数与成功标志。
// 使用系统临时目录避免在目标路径（可能是只读介质）创建文件，同时避免并发调用间的竞态。
func makeUniqueTestDir() (dir string, cleanup func(), ok bool) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// rand 失败时退回使用固定目录（仍可用，只是并发不安全）
		d := filepath.Join(os.TempDir(), ".filesync-modetest")
		if err := os.MkdirAll(d, 0755); err != nil {
			return "", func() {}, false
		}
		return d, func() { os.RemoveAll(d) }, true
	}
	dir = filepath.Join(os.TempDir(), ".filesync-modetest-"+hex.EncodeToString(suffix[:]))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", func() {}, false
	}
	return dir, func() { os.RemoveAll(dir) }, true
}

// copyFile 用 1MB buffer 复制文件，toPerm 为目标权限。
func copyFile(src, dst string, toPerm os.FileMode) error {
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sf.Close()
	perm := toPerm
	if toPerm == 0 {
		perm = 0644
	}
	df, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer df.Close()
	buf := make([]byte, 1024*1024) // 1MB
	if _, err := io.CopyBuffer(df, sf, buf); err != nil {
		return err
	}
	return nil
}

// prepareOverwrite 若 dest 已存在且只读，先 chmod 可写，便于覆盖。
func prepareOverwrite(dest string) {
	fi, err := os.Stat(dest)
	if err != nil {
		return
	}
	if fi.Mode()&0200 == 0 { // 只读
		os.Chmod(dest, fi.Mode()|0200) // 错误不处理：后续 PlaceFile 会报明确错误
	}
}
