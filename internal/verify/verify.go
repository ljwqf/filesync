// Package verify 校验目标盘所有镜像文件哈希与索引一致。
package verify

import (
	"fmt"
	"os"
	"path/filepath"

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
	err := v.index.IterateFiles(func(relPath string, r index.FileRecord) bool {
		stats.Checked++
		dest := paths.Long(filepath.Join(v.targetRoot, relPath))
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
