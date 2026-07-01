// Package verify 校验目标盘所有镜像文件哈希与索引一致。
package verify

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ljwqf/filesync/internal/cas"
	"github.com/ljwqf/filesync/internal/hasher"
	"github.com/ljwqf/filesync/internal/index"
	"github.com/ljwqf/filesync/internal/paths"
)

// Stats 是 verify 统计。
type Stats struct {
	Checked int64
	Failed  int64
	Missing int64
	Errors  []string
}

// Verifier 执行全盘校验。
type Verifier struct {
	cas        cas.CAS
	index      index.Index
	hasher     hasher.Hasher
	targetRoot string
}

// New 创建 Verifier。
func New(c cas.CAS, idx index.Index, h hasher.Hasher, targetRoot string) *Verifier {
	return &Verifier{cas: c, index: idx, hasher: h, targetRoot: targetRoot}
}

// Run 遍历索引中所有文件记录，重算目标镜像哈希比对。
func (v *Verifier) Run() (Stats, error) {
	var stats Stats
	// 预计算 targetRoot 的绝对路径用于 containment 校验
	absRoot, err := filepath.Abs(v.targetRoot)
	if err != nil {
		return stats, fmt.Errorf("resolve target root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)
	err = v.index.IterateFiles(func(relPath string, r index.FileRecord) bool {
		stats.Checked++
		// 安全校验：确保最终路径仍在 targetRoot 内，防止篡改索引中的 relPath
		// （如 ../../etc/passwd）导致读取 targetRoot 外的文件。
		// 在 paths.Long 之前校验，因为 Long 会加 \\?\ 前缀影响比较。
		joined := filepath.Join(v.targetRoot, relPath)
		if !isWithinRoot(joined, absRoot) {
			stats.Failed++
			stats.Errors = append(stats.Errors, fmt.Sprintf("%s: path escapes target root", relPath))
			return true
		}
		dest := paths.Long(joined)
		key, err := v.hasher.HashFile(dest)
		if err != nil {
			// 区分文件缺失与读取错误（权限不足等），给出准确的诊断信息
			if os.IsNotExist(err) {
				stats.Missing++
				stats.Errors = append(stats.Errors, fmt.Sprintf("%s: missing", relPath))
			} else {
				stats.Failed++
				stats.Errors = append(stats.Errors, fmt.Sprintf("%s: read error: %v", relPath, err))
			}
			return true
		}
		if key != r.ObjectKey {
			stats.Failed++
			stats.Errors = append(stats.Errors, fmt.Sprintf("%s: hash mismatch (got %s, index %s)", relPath, key, r.ObjectKey))
		}
		return true
	})
	return stats, err
}

// isWithinRoot 校验 path（经 paths.Long 处理后）是否仍在 root 目录内。
// 防止 relPath 含 ../ 导致路径逃逸出 targetRoot。
func isWithinRoot(path, root string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absPath = filepath.Clean(absPath)
	root = filepath.Clean(root)
	// 精确匹配 root 本身，或以 root + 分隔符 开头
	if absPath == root {
		return true
	}
	return strings.HasPrefix(absPath, root+string(filepath.Separator))
}
