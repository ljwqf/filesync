// Package reindex 重建索引。
//   - NTFS: 用 os.SameFile 关联镜像文件与 objects/ 下 object（无需重算哈希）
//   - exFAT: objects/ 应为空，重算每个镜像文件哈希重建索引
package reindex

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ljw/filesync/internal/cas"
	"github.com/ljw/filesync/internal/hasher"
	"github.com/ljw/filesync/internal/index"
	"github.com/ljw/filesync/internal/paths"
)

// Stats 是 reindex 统计。
type Stats struct {
	Files   int64
	Objects int64
	Failed  int64
}

// Reindexer 重建索引。
type Reindexer struct {
	cas        cas.CAS
	hasher     hasher.Hasher
	targetRoot string
	indexPath  string
}

// New 创建 Reindexer。
func New(c cas.CAS, h hasher.Hasher, targetRoot, indexPath string) *Reindexer {
	return &Reindexer{cas: c, hasher: h, targetRoot: targetRoot, indexPath: indexPath}
}

// Run 重建索引（覆盖现有 index.db）。
// 所有 file 与 object 记录在内存中收集，最后通过单次原子事务批量写入，
// 确保崩溃后不会出现 file 已写但 object RefCount 未更新的不一致状态。
func (r *Reindexer) Run() (Stats, error) {
	var stats Stats

	// 打开（或创建）索引
	idx, err := index.Open(r.indexPath)
	if err != nil {
		return stats, fmt.Errorf("open index: %w", err)
	}
	defer idx.Close()

	// 构建 object 物理文件 map（NTFS 用 SameFile 关联）
	type objInfo struct {
		path string
		fi   os.FileInfo
	}
	objMap := map[string]objInfo{}
	if r.cas.Mode() == cas.ModeHardlink {
		keys, _ := r.cas.ListObjects()
		for _, k := range keys {
			p := r.cas.ObjectPath(k)
			fi, err := os.Stat(p)
			if err == nil {
				objMap[k] = objInfo{p, fi}
			}
		}
	}

	// 在内存中收集所有 file 与 object 记录，最后原子批量写入
	fileRecs := map[string]index.FileRecord{}
	objectRecs := map[string]index.ObjectRecord{} // key -> 累计 RefCount 的 object 记录

	// 遍历目标盘镜像文件（排除 .filesync）；用长路径前缀绕过 Windows 260 限制
	walkRoot := paths.Long(r.targetRoot)
	err = filepath.Walk(walkRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if path == walkRoot {
				return nil
			}
			// 跳过 .filesync 目录
			rel, _ := filepath.Rel(walkRoot, path)
			if rel == ".filesync" {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(walkRoot, path)
		relPath := filepath.ToSlash(rel)

		var key string
		if r.cas.Mode() == cas.ModeHardlink {
			// 用 SameFile 关联到 object
			for k, oi := range objMap {
				if os.SameFile(info, oi.fi) {
					key = k
					break
				}
			}
			if key == "" {
				// 未关联到 object，重算
				key, err = r.hasher.HashFile(paths.Long(path))
				if err != nil {
					stats.Failed++
					return nil
				}
			}
		} else {
			// exFAT: 重算
			key, err = r.hasher.HashFile(paths.Long(path))
			if err != nil {
				stats.Failed++
				return nil
			}
		}

		fileRecs[relPath] = index.FileRecord{
			Size:      info.Size(),
			Mtime:     info.ModTime(),
			ObjectKey: key,
			SyncedAt:  info.ModTime(),
		}
		// 累计 object RefCount
		rec := objectRecs[key]
		rec.Size = info.Size()
		rec.RefCount++
		rec.Orphaned = false
		objectRecs[key] = rec
		stats.Files++
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("walk target: %w", err)
	}

	// 原子批量写入：单事务内写入所有 file 与 object 记录
	if err := idx.ApplyReindexBatch(fileRecs, objectRecs); err != nil {
		return stats, fmt.Errorf("atomic reindex write: %w", err)
	}
	stats.Objects = int64(len(objectRecs))

	return stats, nil
}
