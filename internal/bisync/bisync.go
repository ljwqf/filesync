// Package bisync 实现双向同步：扫描两端目录，检测变化并合并。
// 两端各存一份索引（冗余容灾），记录各自上次同步后的状态。
// 支持多种冲突策略：keep-both / left-wins / right-wins / newer-wins。
package bisync

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ljwqf/filesync/internal/fileindex"
	"github.com/ljwqf/filesync/internal/paths"
	"github.com/ljwqf/filesync/internal/scanner"
)

// ConflictStrategy 冲突解决策略。
type ConflictStrategy string

const (
	KeepBoth  ConflictStrategy = "keep-both"  // 两端都保留，冲突文件重命名
	LeftWins  ConflictStrategy = "left-wins"  // 左端覆盖右端
	RightWins ConflictStrategy = "right-wins" // 右端覆盖左端
	NewerWins ConflictStrategy = "newer-wins" // mtime 更新的一端覆盖
)

// Config 是双向同步配置。
type Config struct {
	Left     string           // 左端目录
	Right    string           // 右端目录
	Workers  int              // 并发数
	Verify   bool             // 拷贝后校验
	Conflict ConflictStrategy // 冲突策略
	Exclude  []string         // 排除模式
}

// ChangeType 变更类型。
type ChangeType int

const (
	ChangeNone     ChangeType = iota
	ChangeNew                 // 新增
	ChangeModified            // 修改
	ChangeDeleted             // 删除
)

// FileChange 描述单个文件的变更。
type FileChange struct {
	Path      string
	Left      ChangeType
	Right     ChangeType
	LeftSize  int64
	RightSize int64
}

// Stats 是双向同步统计结果。
type Stats struct {
	ScannedLeft  int64
	ScannedRight int64
	LeftToRight  int64 // 从左端复制到右端的文件数
	RightToLeft  int64 // 从右端复制到左端的文件数
	Conflicts    int64
	DeletedLeft  int64
	DeletedRight int64
	Unchanged    int64
	BytesCopied  int64
	Changes      []FileChange
}

// Bisync 是双向同步器。
type Bisync struct {
	cfg *Config
}

// New 创建 Bisync。
func New(cfg *Config) *Bisync {
	if cfg.Workers < 1 {
		cfg.Workers = 8
	}
	if cfg.Conflict == "" {
		cfg.Conflict = KeepBoth
	}
	return &Bisync{cfg: cfg}
}

// Sync 执行双向同步。dryRun=true 时仅检测不执行。
func (b *Bisync) Sync(dryRun bool) (Stats, error) {
	leftIdx, rightIdx, err := b.openIndexes()
	if err != nil {
		return Stats{}, err
	}
	defer leftIdx.Close()
	defer rightIdx.Close()

	return b.sync(leftIdx, rightIdx, dryRun)
}

// openIndexes 打开两端索引文件。
func (b *Bisync) openIndexes() (fileindex.FileIndex, fileindex.FileIndex, error) {
	leftIdxPath := filepath.Join(b.cfg.Left, ".bisync-index.db")
	rightIdxPath := filepath.Join(b.cfg.Right, ".bisync-index.db")

	leftIdx, err := fileindex.Open(leftIdxPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open left index: %w", err)
	}
	rightIdx, err := fileindex.Open(rightIdxPath)
	if err != nil {
		leftIdx.Close()
		return nil, nil, fmt.Errorf("open right index: %w", err)
	}
	return leftIdx, rightIdx, nil
}

// sync 执行同步核心逻辑。
// 索引策略：每端索引记录自己上次同步后的状态。
// 左端索引 = 左端文件在上次同步后的快照。
// 右端索引 = 右端文件在上次同步后的快照。
func (b *Bisync) sync(leftIdx, rightIdx fileindex.FileIndex, dryRun bool) (Stats, error) {
	var stats Stats

	// 1. 扫描两端当前状态
	leftFiles, err := b.scanDir(b.cfg.Left)
	if err != nil {
		return stats, fmt.Errorf("scan left: %w", err)
	}
	rightFiles, err := b.scanDir(b.cfg.Right)
	if err != nil {
		return stats, fmt.Errorf("scan right: %w", err)
	}
	stats.ScannedLeft = int64(len(leftFiles))
	stats.ScannedRight = int64(len(rightFiles))

	// 2. 读取两端索引（各自上次同步后的状态）
	leftIndexed := make(map[string]fileindex.FileState)
	rightIndexed := make(map[string]fileindex.FileState)
	leftIdx.Iterate(func(path string, s fileindex.FileState) bool {
		leftIndexed[path] = s
		return true
	})
	rightIdx.Iterate(func(path string, s fileindex.FileState) bool {
		rightIndexed[path] = s
		return true
	})

	// 3. 收集所有路径
	allPaths := make(map[string]bool)
	for p := range leftFiles {
		allPaths[p] = true
	}
	for p := range rightFiles {
		allPaths[p] = true
	}
	for p := range leftIndexed {
		allPaths[p] = true
	}
	for p := range rightIndexed {
		allPaths[p] = true
	}

	// 4. 检测变更：对比当前状态 vs 上次同步状态
	var toLeft []copyTask
	var toRight []copyTask
	var deletes []deleteTask

	for path := range allPaths {
		inLeft := leftFiles[path]
		inRight := rightFiles[path]
		wasLeft := leftIndexed[path]
		wasRight := rightIndexed[path]

		leftChanged := b.detectChanges(inLeft, wasLeft)
		rightChanged := b.detectChanges(inRight, wasRight)

		change := FileChange{Path: path}
		if leftChanged != ChangeNone {
			change.Left = leftChanged
			change.LeftSize = 0
			if inLeft != nil {
				change.LeftSize = inLeft.Size
			}
		}
		if rightChanged != ChangeNone {
			change.Right = rightChanged
			change.RightSize = 0
			if inRight != nil {
				change.RightSize = inRight.Size
			}
		}
		stats.Changes = append(stats.Changes, change)

		switch {
		case leftChanged == ChangeNone && rightChanged == ChangeNone:
			// 两端都没变化
			stats.Unchanged++

		case leftChanged != ChangeNone && rightChanged == ChangeNone:
			// 只有左端变化 → 复制左→右
			if inLeft != nil {
				toRight = append(toRight, copyTask{
					Path: path, SrcRoot: b.cfg.Left, DestRoot: b.cfg.Right,
					Size: inLeft.Size, Mtime: inLeft.Mtime,
				})
			} else {
				deletes = append(deletes, deleteTask{Path: path, Side: "right"})
				stats.DeletedRight++
			}

		case leftChanged == ChangeNone && rightChanged != ChangeNone:
			// 只有右端变化 → 复制右→左
			if inRight != nil {
				toLeft = append(toLeft, copyTask{
					Path: path, SrcRoot: b.cfg.Right, DestRoot: b.cfg.Left,
					Size: inRight.Size, Mtime: inRight.Mtime,
				})
			} else {
				deletes = append(deletes, deleteTask{Path: path, Side: "left"})
				stats.DeletedLeft++
			}

		default:
			// 两端都变化 → 冲突
			stats.Conflicts++
			if !dryRun {
				b.resolveConflict(path, inLeft, inRight)
			}
		}
	}

	// 5. 设置统计（无论是否 dryRun 都记录检测到的变更）
	stats.LeftToRight = int64(len(toRight))
	stats.RightToLeft = int64(len(toLeft))

	// 6. 执行复制（dryRun 模式下跳过）
	if !dryRun {
		for _, t := range toRight {
			if err := b.copyFile(t); err != nil {
				fmt.Fprintf(os.Stderr, "警告: 复制 %s -> right 失败: %v\n", t.Path, err)
				continue
			}
			stats.BytesCopied += t.Size
		}
		for _, t := range toLeft {
			if err := b.copyFile(t); err != nil {
				fmt.Fprintf(os.Stderr, "警告: 复制 %s -> left 失败: %v\n", t.Path, err)
				continue
			}
			stats.BytesCopied += t.Size
		}

		// 执行删除
		for _, d := range deletes {
			b.deleteFile(d)
		}

		// 7. 更新索引：基于操作结果构建最终状态
		b.updateIndexes(leftIdx, rightIdx, leftFiles, rightFiles, toLeft, toRight, deletes)
	}

	return stats, nil
}

// scanDir 扫描目录，返回 relPath → *FileInfo 映射。
// 排除 .bisync-index.db 文件。
func (b *Bisync) scanDir(dir string) (map[string]*scanner.FileInfo, error) {
	files, _, err := scanner.Scan(dir, b.cfg.Exclude)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*scanner.FileInfo, len(files))
	for i := range files {
		// 跳过索引文件
		if files[i].RelPath == ".bisync-index.db" {
			continue
		}
		result[files[i].RelPath] = &files[i]
	}
	return result, nil
}

// detectChanges 检测单个文件相对于上次同步状态的变更。
func (b *Bisync) detectChanges(current *scanner.FileInfo, was fileindex.FileState) ChangeType {
	switch {
	case current == nil && was.Hash == "" && was.Size == 0:
		// 从未存在过
		return ChangeNone

	case current == nil && (was.Hash != "" || was.Size > 0):
		// 曾经存在，现在没了 → 删除
		return ChangeDeleted

	case current != nil && was.Hash == "" && was.Size == 0:
		// 之前没有记录，现在有了 → 新增
		return ChangeNew

	case current != nil:
		// 都存在，检查是否变化
		if fileindex.IsUnchanged(was, current.Size, current.Mtime) {
			return ChangeNone
		}
		return ChangeModified
	}

	return ChangeNone
}

// copyTask 描述一次复制任务。
type copyTask struct {
	Path     string // 目标路径（相对 DestRoot）
	SrcRoot  string
	SrcPath  string // 源路径（相对 SrcRoot），为空时用 Path
	DestRoot string
	Size     int64
	Mtime    time.Time
}

// srcRelPath 返回源文件的相对路径。
func (t copyTask) srcRelPath() string {
	if t.SrcPath != "" {
		return t.SrcPath
	}
	return t.Path
}

// deleteTask 描述一次删除任务。
type deleteTask struct {
	Path string
	Side string // "left" or "right"
}

// copyFile 执行文件复制。
func (b *Bisync) copyFile(t copyTask) error {
	src := paths.Long(filepath.Join(t.SrcRoot, t.srcRelPath()))
	dest := paths.Long(filepath.Join(t.DestRoot, t.Path))

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	if err := copyFileContent(src, dest); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	if err := os.Chtimes(dest, time.Now(), t.Mtime); err != nil {
		fmt.Fprintf(os.Stderr, "警告: 保留 mtime 失败 %s: %v\n", t.Path, err)
	}

	return nil
}

// deleteFile 执行文件删除。
func (b *Bisync) deleteFile(d deleteTask) {
	dir := b.cfg.Left
	if d.Side == "right" {
		dir = b.cfg.Right
	}
	path := paths.Long(filepath.Join(dir, d.Path))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "警告: 删除 %s 失败: %v\n", d.Path, err)
	}
}

// resolveConflict 处理冲突文件。
func (b *Bisync) resolveConflict(path string, left, right *scanner.FileInfo) {
	switch b.cfg.Conflict {
	case LeftWins:
		if left != nil {
			b.copyFile(copyTask{Path: path, SrcRoot: b.cfg.Left, DestRoot: b.cfg.Right, Size: left.Size, Mtime: left.Mtime})
		} else {
			b.deleteFile(deleteTask{Path: path, Side: "right"})
		}

	case RightWins:
		if right != nil {
			b.copyFile(copyTask{Path: path, SrcRoot: b.cfg.Right, DestRoot: b.cfg.Left, Size: right.Size, Mtime: right.Mtime})
		} else {
			b.deleteFile(deleteTask{Path: path, Side: "left"})
		}

	case NewerWins:
		if left == nil {
			b.deleteFile(deleteTask{Path: path, Side: "left"})
		} else if right == nil {
			b.deleteFile(deleteTask{Path: path, Side: "right"})
		} else if left.Mtime.After(right.Mtime) {
			b.copyFile(copyTask{Path: path, SrcRoot: b.cfg.Left, DestRoot: b.cfg.Right, Size: left.Size, Mtime: left.Mtime})
		} else {
			b.copyFile(copyTask{Path: path, SrcRoot: b.cfg.Right, DestRoot: b.cfg.Left, Size: right.Size, Mtime: right.Mtime})
		}

	case KeepBoth:
		if left != nil {
			b.copyFile(copyTask{Path: path + ".conflict-left", SrcRoot: b.cfg.Left, SrcPath: path, DestRoot: b.cfg.Right, Size: left.Size, Mtime: left.Mtime})
		}
		if right != nil {
			b.copyFile(copyTask{Path: path + ".conflict-right", SrcRoot: b.cfg.Right, SrcPath: path, DestRoot: b.cfg.Left, Size: right.Size, Mtime: right.Mtime})
		}
	}
}

// updateIndexes 同步完成后更新两端索引，记录各自同步后的状态。
// 左端最终状态 = 原左端文件 + 从右端复制的文件 - 从左端删除的文件。
// 右端最终状态 = 原右端文件 + 从左端复制的文件 - 从右端删除的文件。
func (b *Bisync) updateIndexes(
	leftIdx, rightIdx fileindex.FileIndex,
	leftFiles, rightFiles map[string]*scanner.FileInfo,
	toLeft, toRight []copyTask,
	deletes []deleteTask,
) {
	// 构建左端最终状态
	leftFinal := make(map[string]fileindex.FileState)
	for path, fi := range leftFiles {
		leftFinal[path] = fileindex.FileState{Size: fi.Size, Mtime: fi.Mtime}
	}
	// 从右端复制到左端的文件：用右端的原始状态
	for _, t := range toLeft {
		if fi, ok := rightFiles[t.Path]; ok {
			leftFinal[t.Path] = fileindex.FileState{Size: fi.Size, Mtime: fi.Mtime}
		} else {
			leftFinal[t.Path] = fileindex.FileState{Size: t.Size, Mtime: t.Mtime}
		}
	}
	// 从左端删除的文件
	for _, d := range deletes {
		if d.Side == "left" {
			delete(leftFinal, d.Path)
		}
	}

	// 构建右端最终状态
	rightFinal := make(map[string]fileindex.FileState)
	for path, fi := range rightFiles {
		rightFinal[path] = fileindex.FileState{Size: fi.Size, Mtime: fi.Mtime}
	}
	// 从左端复制到右端的文件：用左端的原始状态
	for _, t := range toRight {
		if fi, ok := leftFiles[t.Path]; ok {
			rightFinal[t.Path] = fileindex.FileState{Size: fi.Size, Mtime: fi.Mtime}
		} else {
			rightFinal[t.Path] = fileindex.FileState{Size: t.Size, Mtime: t.Mtime}
		}
	}
	// 从右端删除的文件
	for _, d := range deletes {
		if d.Side == "right" {
			delete(rightFinal, d.Path)
		}
	}

	leftIdx.ApplyBatch(leftFinal, nil)
	rightIdx.ApplyBatch(rightFinal, nil)
}

// copyFileContent 复制文件内容。
func copyFileContent(src, dst string) error {
	sf, err := os.Open(paths.Long(src))
	if err != nil {
		return err
	}
	defer sf.Close()

	df, err := os.OpenFile(paths.Long(dst), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer df.Close()

	buf := make([]byte, 1024*1024)
	_, err = io.CopyBuffer(df, sf, buf)
	return err
}
