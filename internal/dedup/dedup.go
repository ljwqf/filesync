// Package dedup 扫描文件夹，对内容重复的文件用硬链接去重。
// NTFS: 将重复文件替换为指向同一物理副本的硬链接（所有原始路径仍可访问，磁盘只存一份）。
// exFAT/FAT32: 不支持硬链接，仅报告重复组不做修改。
//
// 支持增量模式：传入 FileIndex 时，仅重算变化文件的哈希，大幅加速重复运行。
package dedup

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ljwqf/filesync/internal/fileindex"
	"github.com/ljwqf/filesync/internal/hasher"
	"github.com/ljwqf/filesync/internal/paths"
	"github.com/ljwqf/filesync/internal/scanner"
)

// DupGroup 描述一组内容完全相同的文件。
type DupGroup struct {
	Hash          string   // 内容哈希（objectKey，h3:<hex>）
	Size          int64    // 文件大小
	Files         []string // 组内所有文件绝对路径
	Representative string  // 选作硬链接源的代表文件（组内第一个）
	Deduped       []string // 已成功去重（替换为硬链接）的文件
}

// Stats 是 dedup 统计结果。
type Stats struct {
	Scanned        int64      // 扫描的文件总数
	DuplicateFiles int64      // 重复文件总数（每组中除代表外的文件数之和）
	DedupedFiles   int64      // 实际去重的文件数（已替换为硬链接）
	BytesSaved     int64      // 节省的磁盘空间字节数
	Groups         []DupGroup // 重复组
	ReadonlyFailed int64      // --readonly 模式下设只读失败的文件数
}

// Deduper 执行重复文件去重。
type Deduper struct {
	hasher   hasher.Hasher
	workers  int
	hardlink bool // 目标文件系统是否支持硬链接
	readonly bool // 去重后是否将文件设为只读（归档场景防误编辑）
	// linkFn 创建硬链接，默认 os.Link。可由测试替换为失败实现以验证回滚路径。
	linkFn func(oldname, newname string) error
}

// New 创建 Deduper。hardlink=false 时仅报告不修改。
func New(h hasher.Hasher, workers int, hardlink bool) *Deduper {
	if workers < 1 {
		workers = 1
	}
	return &Deduper{hasher: h, workers: workers, hardlink: hardlink, linkFn: os.Link}
}

// SetReadonly 设置去重后是否将整组文件设为只读。
// 归档场景建议开启（防止误编辑污染所有硬链接副本）；
// 工作目录场景保持 false（默认，允许修改）。
func (d *Deduper) SetReadonly(v bool) { d.readonly = v }

// setLinkFn 替换硬链接创建函数，仅供测试用于模拟 Link 失败以验证回滚路径。
func (d *Deduper) setLinkFn(fn func(oldname, newname string) error) { d.linkFn = fn }

// Run 扫描 dir，识别重复文件并（若支持硬链接）去重。
// dryRun=true 时仅报告不修改任何文件。
// idx 可选：传入时启用增量模式，仅重算变化文件的哈希；nil 则全量扫描。
func (d *Deduper) Run(dir string, exclude []string, dryRun bool, idx fileindex.FileIndex) (Stats, error) {
	var stats Stats

	// 1. 扫描目录
	files, _, err := scanner.Scan(dir, exclude)
	if err != nil {
		return stats, fmt.Errorf("scan %s: %w", dir, err)
	}
	stats.Scanned = int64(len(files))

	// 2. 构建当前文件索引 map（absPath → scanner.FileInfo）
	currentFiles := make(map[string]scanner.FileInfo, len(files))
	for _, f := range files {
		currentFiles[f.AbsPath] = f
	}

	// 3. 增量检测：确定需要计算哈希的文件
	type needHash struct {
		fi   scanner.FileInfo
		hash string // 若从索引复用则非空
	}

	if idx == nil {
		// 无索引：全量模式，所有 size>=2 的候选文件都需哈希
		return d.runFull(files, dryRun)
	}

	// 增量模式：读取索引，对比 (size, mtime) 确定变化文件
	indexed := make(map[string]fileindex.FileState)
	if err := idx.Iterate(func(path string, s fileindex.FileState) bool {
		indexed[path] = s
		return true
	}); err != nil {
		return stats, fmt.Errorf("read index: %w", err)
	}

	// 收集需要哈希的文件（新增或变化）
	var toHash []scanner.FileInfo
	puts := make(map[string]fileindex.FileState)
	var deletes []string

	for absPath, fi := range currentFiles {
		if s, ok := indexed[absPath]; ok {
			if fileindex.IsUnchanged(s, fi.Size, fi.Mtime) && s.Hash != "" {
				// 未变化且有哈希记录：复用
				continue
			}
			// 变化：需重算哈希
			toHash = append(toHash, fi)
		} else {
			// 新增文件
			toHash = append(toHash, fi)
		}
	}

	// 已删除的文件
	for absPath := range indexed {
		if _, ok := currentFiles[absPath]; !ok {
			deletes = append(deletes, absPath)
		}
	}

	// 4. 并发计算哈希（仅变化文件）
	hashResults := d.hashFiles(toHash)

	// 将哈希结果写入 puts
	for _, r := range hashResults {
		if r.err != nil {
			continue
		}
		puts[r.fi.AbsPath] = fileindex.FileState{
			Size:   r.fi.Size,
			Mtime:  r.fi.Mtime,
			Hash:   r.hash,
		}
	}

	// 5. 按 (size, hash) 分组查找重复
	type groupKey struct {
		size int64
		hash string
	}
	hashGroups := map[groupKey][]scanner.FileInfo{}

	// 先加入未变化的文件（复用索引哈希）
	for absPath, fi := range currentFiles {
		if s, ok := indexed[absPath]; ok && s.Hash != "" && fileindex.IsUnchanged(s, fi.Size, fi.Mtime) {
			k := groupKey{size: fi.Size, hash: s.Hash}
			hashGroups[k] = append(hashGroups[k], fi)
		}
	}

	// 再加入变化/新增文件的哈希结果
	for _, r := range hashResults {
		if r.err != nil {
			continue
		}
		k := groupKey{size: r.fi.Size, hash: r.hash}
		hashGroups[k] = append(hashGroups[k], r.fi)
	}

	// 6. 收集重复组（len>1）
	for k, group := range hashGroups {
		if len(group) < 2 {
			continue
		}
		dg := DupGroup{
			Hash: k.hash,
			Size: k.size,
		}
		for _, fi := range group {
			dg.Files = append(dg.Files, fi.AbsPath)
		}
		dg.Representative = dg.Files[0]
		stats.DuplicateFiles += int64(len(dg.Files) - 1)
		stats.Groups = append(stats.Groups, dg)
	}

	// 7. 去重（仅 hardlink 模式且非 dryRun）
	if !d.hardlink || dryRun {
		// 即使不执行去重，也要更新索引
		if !dryRun && len(puts) > 0 || len(deletes) > 0 {
			idx.ApplyBatch(puts, deletes)
		}
		return stats, nil
	}
	for i := range stats.Groups {
		deduped, err := d.hardlinkGroup(&stats.Groups[i])
		if err != nil {
			continue // 单组失败不影响其他组
		}
		stats.DedupedFiles += int64(len(deduped))
		stats.BytesSaved += int64(len(deduped)) * stats.Groups[i].Size
		stats.Groups[i].Deduped = deduped
		// 归档场景：去重后将整组设为只读，防止误编辑污染所有硬链接副本。
		if d.readonly && len(deduped) > 0 {
			for _, f := range stats.Groups[i].Files {
				if err := os.Chmod(paths.Long(f), 0444); err != nil {
					stats.ReadonlyFailed++
				}
			}
		}
	}

	// 8. 更新索引
	if len(puts) > 0 || len(deletes) > 0 {
		idx.ApplyBatch(puts, deletes)
	}

	return stats, nil
}

// fileHashResult 是单个文件的哈希计算结果。
type fileHashResult struct {
	fi   scanner.FileInfo
	hash string
	err  error
}

// hashFiles 并发计算一批文件的哈希，返回结果列表（保持原始顺序）。
func (d *Deduper) hashFiles(files []scanner.FileInfo) []fileHashResult {
	if len(files) == 0 {
		return nil
	}

	results := make([]fileHashResult, len(files))
	hashCh := make(chan int, len(files))
	var wg sync.WaitGroup

	// 发送所有索引
	for i := range files {
		hashCh <- i
	}
	close(hashCh)

	// worker 池
	for i := 0; i < d.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range hashCh {
				fi := files[idx]
				key, err := d.hasher.HashFile(paths.Long(fi.AbsPath))
				results[idx] = fileHashResult{fi: fi, hash: key, err: err}
			}
		}()
	}
	wg.Wait()

	return results
}

// runFull 全量模式：无索引时的原始逻辑。
func (d *Deduper) runFull(files []scanner.FileInfo, dryRun bool) (Stats, error) {
	var stats Stats
	stats.Scanned = int64(len(files))

	// 按 size 分组
	sizeGroups := map[int64][]scanner.FileInfo{}
	for _, f := range files {
		sizeGroups[f.Size] = append(sizeGroups[f.Size], f)
	}

	// 对 size>=2 的组并发算哈希
	var candidates []scanner.FileInfo
	for _, group := range sizeGroups {
		if len(group) < 2 {
			continue
		}
		candidates = append(candidates, group...)
	}

	hashResults := d.hashFiles(candidates)

	// 按 (size, hash) 分组
	type groupKey struct {
		size int64
		hash string
	}
	hashGroups := map[groupKey][]scanner.FileInfo{}
	for _, r := range hashResults {
		if r.err != nil {
			continue
		}
		k := groupKey{size: r.fi.Size, hash: r.hash}
		hashGroups[k] = append(hashGroups[k], r.fi)
	}

	// 收集重复组
	for k, group := range hashGroups {
		if len(group) < 2 {
			continue
		}
		dg := DupGroup{
			Hash: k.hash,
			Size: k.size,
		}
		for _, fi := range group {
			dg.Files = append(dg.Files, fi.AbsPath)
		}
		dg.Representative = dg.Files[0]
		stats.DuplicateFiles += int64(len(dg.Files) - 1)
		stats.Groups = append(stats.Groups, dg)
	}

	// 去重
	if !d.hardlink || dryRun {
		return stats, nil
	}
	for i := range stats.Groups {
		deduped, err := d.hardlinkGroup(&stats.Groups[i])
		if err != nil {
			continue
		}
		stats.DedupedFiles += int64(len(deduped))
		stats.BytesSaved += int64(len(deduped)) * stats.Groups[i].Size
		stats.Groups[i].Deduped = deduped
		if d.readonly && len(deduped) > 0 {
			for _, f := range stats.Groups[i].Files {
				if err := os.Chmod(paths.Long(f), 0444); err != nil {
					stats.ReadonlyFailed++
				}
			}
		}
	}

	return stats, nil
}

// hardlinkGroup 对单个重复组执行硬链接去重。
// 保留代表文件，将其余文件替换为指向代表的硬链接。
// 已是同一物理文件（os.SameFile）的跳过。
//
// 安全去重策略（FINDING-001 修复）：
// 使用 rename-then-link 模式而非 remove-then-link，确保链接创建失败时可回滚。
// 1. 将原文件 rename 到临时路径（同卷原子操作）
// 2. 创建指向代表文件的硬链接
// 3. 若链接成功，删除临时文件；若链接失败，将临时文件 rename 回原路径恢复
//
// mtime 处理（修复 shared-inode 覆盖问题）：
// 硬链接文件共享同一 inode，mtime 也共享——无法让组内各文件保留不同 mtime。
// 因此去重后整组统一使用代表文件的 mtime：组内去重完成后再还原代表文件 mtime
// （rename/link 操作会修改 inode 的 mtime，需在最后统一恢复）。
// 同步工具本身的增量判定基于 (size, objectKey, mtime)，去重后 mtime 统一不影响
// 正确性（内容哈希未变，下次 sync 会通过 mtime-update 跳过路径修正索引）。
func (d *Deduper) hardlinkGroup(group *DupGroup) ([]string, error) {
	var deduped []string
	repLong := paths.Long(group.Representative)

	// 校验代表文件仍存在且 size 未变（防中途修改）
	repInfo, err := os.Stat(repLong)
	if err != nil {
		return deduped, fmt.Errorf("stat representative %s: %w", group.Representative, err)
	}
	if repInfo.Size() != group.Size {
		return deduped, fmt.Errorf("representative size changed: %d != %d", repInfo.Size(), group.Size)
	}

	// 备份代表文件 mtime：rename/link 会修改 inode mtime，需在去重后统一还原。
	repMtime := repInfo.ModTime()

	for i := 1; i < len(group.Files); i++ {
		filePath := group.Files[i]
		fileLong := paths.Long(filePath)

		// 已是同一物理文件（已是硬链接）则跳过
		fi, err := os.Stat(fileLong)
		if err != nil {
			continue
		}
		if os.SameFile(repInfo, fi) {
			continue
		}

		// 解除只读（若只读则无法 rename）
		if fi.Mode()&0200 == 0 {
			if err := os.Chmod(fileLong, fi.Mode()|0200); err != nil {
				fmt.Fprintf(os.Stderr, "警告: 解除只读失败 %s: %v\n", filePath, err)
			}
		}

		// 安全去重：先 rename 到临时路径，再创建硬链接，失败则回滚
		tmpPath := fileLong + ".filesync.dedup"
		if err := os.Rename(fileLong, tmpPath); err != nil {
			continue
		}

		// 创建指向代表文件的硬链接
		if err := d.linkFn(repLong, fileLong); err != nil {
			if rerr := os.Rename(tmpPath, fileLong); rerr != nil {
				fmt.Fprintf(os.Stderr, "警告: 回滚失败，原始文件已移至 %s，请手动恢复: %v\n", tmpPath, rerr)
			}
			continue
		}

		// 链接成功：删除临时文件
		if err := os.Remove(tmpPath); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 删除临时文件失败 %s: %v\n", tmpPath, err)
		}
		deduped = append(deduped, filePath)
	}

	// 组内去重全部完成：还原代表文件 mtime
	if len(deduped) > 0 {
		if err := os.Chtimes(repLong, time.Now(), repMtime); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 还原 mtime 失败 %s: %v\n", group.Representative, err)
		}
	}

	return deduped, nil
}
