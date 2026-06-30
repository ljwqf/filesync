// Package syncer 编排完整同步流程：扫描 → 哈希 → 查索引生成任务 → copier → 报告。
package syncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ljwqf/filesync/internal/cas"
	"github.com/ljwqf/filesync/internal/config"
	"github.com/ljwqf/filesync/internal/copier"
	"github.com/ljwqf/filesync/internal/disk"
	"github.com/ljwqf/filesync/internal/hasher"
	"github.com/ljwqf/filesync/internal/index"
	"github.com/ljwqf/filesync/internal/paths"
	"github.com/ljwqf/filesync/internal/scanner"
)

// nowFn 可被测试覆盖的时间函数。
var nowFn = func() time.Time { return time.Now() }

// ProgressEvent 描述单个文件处理完成的事件（透传 copier.ProgressEvent）。
type ProgressEvent = copier.ProgressEvent

// Syncer 是主编排器。
type Syncer struct {
	cfg      *config.Config
	progress copier.ProgressFunc
}

// New 创建 Syncer。
func New(cfg *config.Config) *Syncer {
	return &Syncer{cfg: cfg}
}

// SetProgress 设置进度回调，透传给 copier。
func (s *Syncer) SetProgress(fn copier.ProgressFunc) { s.progress = fn }

// Report 是同步结果（copier.Result 的副本，避免跨包依赖）。
type Report struct {
	Copied     int64
	Skipped    int64
	Failed     int64
	Bytes      int64
	DedupSaved int64
	Errors     []copier.FileError
	Locked     []string // 被占用/锁定而跳过的文件
}

type syncCandidate struct {
	fi      scanner.FileInfo
	relPath string
	destAbs string
	old     index.FileRecord
	oldOK   bool
}

type hashResult struct {
	candidate syncCandidate
	key       string
	err       error
}

// Sync 执行完整同步。
func (s *Syncer) Sync() (Report, error) {
	return s.SyncWithContext(context.Background())
}

// SyncWithContext 执行完整同步，支持 context 取消（SIGINT 优雅停止）。
func (s *Syncer) SyncWithContext(ctx context.Context) (Report, error) {
	return s.run(ctx, false)
}

// SyncDryRun 只扫描与生成任务，不拷贝。
func (s *Syncer) SyncDryRun() (Report, error) {
	return s.run(context.Background(), true)
}

func (s *Syncer) run(ctx context.Context, dryRun bool) (Report, error) {
	filesyncDir := filepath.Join(s.cfg.TargetRoot, config.FilesyncDir)
	objectsRoot := filepath.Join(filesyncDir, config.ObjectsDir)
	indexPath := filepath.Join(filesyncDir, config.IndexFile)
	if err := os.MkdirAll(objectsRoot, 0755); err != nil {
		return Report{}, fmt.Errorf("mkdir objects: %w", err)
	}

	idx, err := index.Open(indexPath)
	if err != nil {
		return Report{}, fmt.Errorf("open index: %w", err)
	}
	defer idx.Close()

	h := hasher.New()
	var candidates []syncCandidate
	var tasks []copier.Task
	var skipped int64
	fastSkip := metadataFastSkip(s.cfg)

	for _, src := range s.cfg.Sources {
		files, dirs, err := scanner.Scan(src.Src, s.cfg.Exclude)
		if err != nil {
			return Report{}, fmt.Errorf("scan %s: %w", src.Src, err)
		}
		// 保留目录结构（含空目录）
		if !dryRun {
			for _, d := range dirs {
				rel, _ := filepath.Rel(src.Src, d)
				destDir := filepath.Join(s.cfg.TargetRoot, src.Dest, rel)
				if err := os.MkdirAll(paths.Long(destDir), 0755); err != nil {
					// 目录创建失败不中断整体，后续拷贝时会报明确错误
					fmt.Fprintf(os.Stderr, "警告: 创建目录 %s 失败: %v\n", destDir, err)
				}
			}
		}

		for _, fi := range files {
			relPath := filepath.ToSlash(filepath.Join(src.Dest, fi.RelPath))
			// 查索引：断点续传判定
			old, ok, _ := idx.GetFile(relPath)
			if fastSkip && ok && old.Size == fi.Size && old.ObjectKey != "" && paths.MtimeClose(old.Mtime, fi.Mtime) {
				skipped++
				continue
			}
			candidates = append(candidates, syncCandidate{
				fi:      fi,
				relPath: relPath,
				destAbs: filepath.Join(s.cfg.TargetRoot, src.Dest, fi.RelPath),
				old:     old,
				oldOK:   ok,
			})
		}
	}

	hashResults := hashCandidates(ctx, h, candidates, s.cfg.Workers)
	var metadataOps []index.SyncOp
	for _, r := range hashResults {
		if r.err != nil {
			skipped++
			continue
		}
		fi := r.candidate.fi
		key := r.key
		old := r.candidate.old
		ok := r.candidate.oldOK
		relPath := r.candidate.relPath

		// (size, objectKey) 匹配但 mtime 不同 → 仅更新索引 mtime，跳过拷贝
		if ok && old.Size == fi.Size && old.ObjectKey == key && !paths.MtimeClose(old.Mtime, fi.Mtime) {
			if !dryRun {
				metadataOps = append(metadataOps, index.SyncOp{
					RelPath: relPath,
					NewRecord: index.FileRecord{
						Size: fi.Size, Mtime: fi.Mtime, ObjectKey: key, SyncedAt: nowFn(),
					},
					OldObjectKey: old.ObjectKey,
				})
			}
			skipped++
			continue
		}
		tasks = append(tasks, copier.Task{
			SrcAbs:       fi.AbsPath,
			DestAbs:      r.candidate.destAbs,
			RelPath:      relPath,
			ObjectKey:    key,
			OldObjectKey: old.ObjectKey,
			Size:         fi.Size,
			Mtime:        fi.Mtime,
		})
	}

	if dryRun {
		return Report{Skipped: skipped}, nil
	}
	if err := idx.ApplySyncResults(metadataOps); err != nil {
		return Report{}, fmt.Errorf("update metadata-only records: %w", err)
	}

	c, err := cas.New(s.cfg.TargetRoot, objectsRoot)
	if err != nil {
		return Report{}, err
	}

	// 拷贝前空间预估（设计 §10）
	// NTFS: 仅新增 object 大小之和（硬链接零额外空间，已存在的 object 不重拷）
	// exFAT: 临时 object 拷后即删，峰值占用≈并发 worker 数 × 最大文件大小（保守上界）
	needed := estimateSpaceNeeded(c, tasks, s.cfg.Workers)
	if needed > 0 {
		free, err := disk.FreeSpace(s.cfg.TargetRoot)
		if err != nil {
			return Report{}, fmt.Errorf("check free space: %w", err)
		}
		if free < uint64(needed) {
			return Report{}, fmt.Errorf("目标盘空间不足: 需要 %d 字节, 可用 %d 字节 (差 %d)；请清理空间后重试",
				needed, free, needed-int64(free))
		}
	}

	cp := copier.New(c, idx, h, s.cfg.Workers)
	cp.SetVerify(s.cfg.Verify)
	cp.SetSmallFileVerify(smallFileVerify(s.cfg))
	cp.SetTargetRoot(s.cfg.TargetRoot)
	if s.progress != nil {
		cp.SetProgress(s.progress)
	}
	res := cp.RunWithContext(ctx, tasks)

	return Report{
		Copied:     res.Copied,
		Skipped:    skipped + res.Skipped,
		Failed:     res.Failed,
		Bytes:      res.Bytes,
		DedupSaved: res.DedupSaved,
		Errors:     res.Errors,
		Locked:     res.Locked,
	}, nil
}

func hashCandidates(ctx context.Context, h hasher.Hasher, candidates []syncCandidate, workers int) []hashResult {
	results := make([]hashResult, len(candidates))
	if len(candidates) == 0 {
		return results
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(candidates) {
		workers = len(candidates)
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				candidate := candidates[idx]
				results[idx].candidate = candidate
				if err := ctx.Err(); err != nil {
					results[idx].err = err
					continue
				}
				key, err := h.HashFile(paths.Long(candidate.fi.AbsPath))
				results[idx].key = key
				results[idx].err = err
			}
		}()
	}
	for i := range candidates {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}

func smallFileVerify(cfg *config.Config) bool {
	if cfg.VerifySmallFiles == nil {
		return true
	}
	return *cfg.VerifySmallFiles
}

func metadataFastSkip(cfg *config.Config) bool {
	if cfg.MetadataFastSkip == nil {
		return true
	}
	return *cfg.MetadataFastSkip
}

// estimateSpaceNeeded 预估同步所需空间（设计 §10）。
// NTFS: 仅新增 object 大小之和（已存在的 object 不重拷，硬链接零额外空间）。
// exFAT: 临时 object 拷后即删，峰值≈并发 worker 数 × 最大文件大小（保守上界，
//
//	避免多 worker 同时拷入大文件时预估不足导致磁盘中途写满）。
func estimateSpaceNeeded(c cas.CAS, tasks []copier.Task, workers int) int64 {
	if c.Mode() == cas.ModeHardlink {
		// NTFS: 仅统计 object 物理不存在的任务 size
		var sum int64
		seen := map[string]bool{}
		for _, t := range tasks {
			if seen[t.ObjectKey] {
				continue // 同 key 已计
			}
			seen[t.ObjectKey] = true
			if _, err := os.Stat(paths.Long(c.ObjectPath(t.ObjectKey))); err != nil {
				sum += t.Size // object 不存在，需拷入
			}
		}
		return sum
	}
	// exFAT: 峰值≈并发 worker 数 × 最大文件大小（保守上界）
	if workers < 1 {
		workers = 1
	}
	var max int64
	for _, t := range tasks {
		if t.Size > max {
			max = t.Size
		}
	}
	return max * int64(workers)
}
