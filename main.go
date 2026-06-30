// Command filesync 将多源目录增量同步到移动 SSD，CAS 去重 + 断点续传。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ljwqf/filesync/internal/bisync"
	"github.com/ljwqf/filesync/internal/cas"
	"github.com/ljwqf/filesync/internal/config"
	"github.com/ljwqf/filesync/internal/dedup"
	"github.com/ljwqf/filesync/internal/fileindex"
	"github.com/ljwqf/filesync/internal/hasher"
	"github.com/ljwqf/filesync/internal/index"
	"github.com/ljwqf/filesync/internal/lock"
	"github.com/ljwqf/filesync/internal/prune"
	"github.com/ljwqf/filesync/internal/reindex"
	"github.com/ljwqf/filesync/internal/report"
	"github.com/ljwqf/filesync/internal/syncer"
	"github.com/ljwqf/filesync/internal/verify"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmd := os.Args[1]

	// dedup 命令不依赖 config，独立处理
	if cmd == "dedup" {
		runDedup(os.Args[2:])
		return
	}

	// bisync 命令独立处理
	if cmd == "bisync" {
		runBisync(os.Args[2:])
		return
	}

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "配置文件路径")
	workers := fs.Int("workers", 0, "并发数（0=用配置默认）")
	dryRun := fs.Bool("dry-run", false, "只扫描不拷贝")
	verifyFlag := fs.Bool("verify", false, "拷贝后校验哈希（小文件默认强制校验）")
	noVerifyFlag := fs.Bool("no-verify", false, "禁用大文件校验（小文件默认仍强制校验）")
	noSmallVerifyFlag := fs.Bool("no-small-verify", false, "禁用小文件强制校验")
	noMetadataFastSkipFlag := fs.Bool("no-metadata-fast-skip", false, "禁用 size+mtime 快速跳过，所有候选文件均计算内容哈希")
	fs.Parse(os.Args[2:])

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("加载配置失败: %v", err)
	}
	if *workers > 0 {
		cfg.Workers = *workers
	}
	// verify CLI 覆盖：--verify 强制开，--no-verify 强制关，均不指定用配置默认
	cfg.ApplyVerifyOverride(*verifyFlag, *noVerifyFlag)
	if *noSmallVerifyFlag {
		f := false
		cfg.VerifySmallFiles = &f
	}
	if *noMetadataFastSkipFlag {
		f := false
		cfg.MetadataFastSkip = &f
	}

	switch cmd {
	case "sync":
		// 获取进程锁，防止多个实例同时操作同一目标盘
		filesyncDir := filepath.Join(cfg.TargetRoot, config.FilesyncDir)
		if err := os.MkdirAll(filesyncDir, 0755); err != nil {
			fatal("创建元数据目录失败: %v", err)
		}
		lk, err := lock.Acquire(filesyncDir)
		if err != nil {
			fatal("%v", err)
		}
		defer lk.Release()

		// 捕获 SIGINT（Ctrl+C），优雅停止分发新任务，等待进行中 worker 完成
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		s := syncer.New(cfg)
		// 进度回调：节流输出，避免大量小文件时控制台刷新成为瓶颈。
		var done int64
		var lastProgress time.Time
		s.SetProgress(func(e syncer.ProgressEvent) {
			done++
			status := "✓"
			if !e.Copied {
				status = "✗"
			}
			now := time.Now()
			if done > 1 && e.Copied && now.Sub(lastProgress) < 200*time.Millisecond {
				return
			}
			lastProgress = now
			fmt.Fprintf(os.Stderr, "\r[%d] %s %s", done, status, e.RelPath)
		})
		var rep syncer.Report
		if *dryRun {
			rep, err = s.SyncDryRun()
		} else {
			rep, err = s.SyncWithContext(ctx)
		}
		fmt.Fprintln(os.Stderr) // 进度行换行
		if err != nil {
			fatal("同步失败: %v", err)
		}
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\n同步已中断（已完成的文件下次运行将跳过）")
		}
		// 用 report 包输出最终报告
		r := report.New(os.Stdout)
		r.SetCopied(rep.Copied, rep.Bytes)
		r.SetSkipped(rep.Skipped)
		r.SetDedupSaved(rep.DedupSaved)
		r.SetLocked(rep.Locked)
		for _, e := range rep.Errors {
			r.AddFailed(e.RelPath, e.Err)
		}
		r.Finish()
		if rep.Failed > 0 {
			os.Exit(1)
		}

	case "status":
		filesyncDir := filepath.Join(cfg.TargetRoot, config.FilesyncDir)
		idx, err := index.Open(filepath.Join(filesyncDir, config.IndexFile))
		if err != nil {
			fatal("打开索引失败: %v", err)
		}
		defer idx.Close()
		var fileCount, objCount, totalSize, refSize int64
		idx.IterateFiles(func(rel string, r index.FileRecord) bool {
			fileCount++
			totalSize += r.Size
			return true
		})
		idx.IterateObjects(func(key string, r index.ObjectRecord) bool {
			objCount++
			if r.RefCount > 0 {
				refSize += r.Size * int64(r.RefCount)
			}
			return true
		})
		fmt.Printf("已同步文件: %d\n", fileCount)
		fmt.Printf("object 数: %d\n", objCount)
		fmt.Printf("引用总大小: %d 字节\n", refSize)
		fmt.Printf("去重节省: %d 字节\n", refSize-totalSize)

	case "verify":
		filesyncDir := filepath.Join(cfg.TargetRoot, config.FilesyncDir)
		// verify 是只读操作，不需要进程锁
		objectsRoot := filepath.Join(filesyncDir, config.ObjectsDir)
		c, err := cas.New(cfg.TargetRoot, objectsRoot)
		if err != nil {
			fatal("初始化 CAS 失败: %v", err)
		}
		idx, err := index.Open(filepath.Join(filesyncDir, config.IndexFile))
		if err != nil {
			fatal("打开索引失败: %v", err)
		}
		defer idx.Close()
		v := verify.New(c, idx, hasher.New(), cfg.TargetRoot)
		stats, err := v.Run()
		if err != nil {
			fatal("校验失败: %v", err)
		}
		fmt.Printf("已校验: %d\n失败: %d\n缺失: %d\n", stats.Checked, stats.Failed, stats.Missing)
		for _, e := range stats.Errors {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
		if stats.Failed > 0 || stats.Missing > 0 {
			os.Exit(1)
		}

	case "reindex":
		filesyncDir := filepath.Join(cfg.TargetRoot, config.FilesyncDir)
		// 获取进程锁：reindex 会重写整个索引
		if err := os.MkdirAll(filesyncDir, 0755); err != nil {
			fatal("创建元数据目录失败: %v", err)
		}
		lk, err := lock.Acquire(filesyncDir)
		if err != nil {
			fatal("%v", err)
		}
		defer lk.Release()
		objectsRoot := filepath.Join(filesyncDir, config.ObjectsDir)
		c, err := cas.New(cfg.TargetRoot, objectsRoot)
		if err != nil {
			fatal("初始化 CAS 失败: %v", err)
		}
		r := reindex.New(c, hasher.New(), cfg.TargetRoot, filepath.Join(filesyncDir, config.IndexFile))
		stats, err := r.Run()
		if err != nil {
			fatal("重建索引失败: %v", err)
		}
		fmt.Printf("已重建: %d 个文件, %d 个 object\n", stats.Files, stats.Objects)

	case "prune":
		filesyncDir := filepath.Join(cfg.TargetRoot, config.FilesyncDir)
		// 获取进程锁：prune 会删除 object 文件并修改索引
		if err := os.MkdirAll(filesyncDir, 0755); err != nil {
			fatal("创建元数据目录失败: %v", err)
		}
		lk, err := lock.Acquire(filesyncDir)
		if err != nil {
			fatal("%v", err)
		}
		defer lk.Release()
		objectsRoot := filepath.Join(filesyncDir, config.ObjectsDir)
		c, err := cas.New(cfg.TargetRoot, objectsRoot)
		if err != nil {
			fatal("初始化 CAS 失败: %v", err)
		}
		idx, err := index.Open(filepath.Join(filesyncDir, config.IndexFile))
		if err != nil {
			fatal("打开索引失败: %v", err)
		}
		defer idx.Close()
		p := prune.New(c, idx)
		stats, err := p.Run(*dryRun)
		if err != nil {
			fatal("清理失败: %v", err)
		}
		fmt.Printf("扫描: %d\n删除: %d\n失败: %d\n", stats.Scanned, stats.Deleted, stats.Failed)

	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `filesync - 文件同步备份工具

用法:
  filesync sync [--config FILE] [--workers N] [--dry-run] [--verify | --no-verify] [--no-small-verify] [--no-metadata-fast-skip]
  filesync status [--config FILE]
  filesync verify [--config FILE]
  filesync reindex [--config FILE]
  filesync prune [--config FILE] [--dry-run]
  filesync dedup <目录> [--index PATH] [--dry-run] [--readonly] [--exclude PATTERN]...
  filesync bisync --left DIR --right DIR [--dry-run] [--workers N] [--conflict STRATEGY] [--exclude PATTERN]...

选项:
  --config FILE    配置文件路径（默认 config.yaml）
  --workers N      并发拷贝数（0=用配置默认）
  --dry-run        只扫描不拷贝
  --verify         强制开启拷贝后哈希校验（小文件默认强制校验）
  --no-verify      禁用大文件校验（小文件默认仍强制校验）
  --no-small-verify 禁用小文件强制校验
  --no-metadata-fast-skip 禁用 size+mtime 快速跳过，所有候选文件均计算内容哈希
  --index PATH     增量索引路径（dedup 用，默认 .dedup-index.db）
  --exclude        排除模式（dedup/bisync 用，可重复）
  --readonly       去重后将文件设为只读（dedup 归档场景）
  --left DIR       左端目录（bisync 用）
  --right DIR      右端目录（bisync 用）
  --conflict STR   冲突策略（bisync 用）: keep-both / left-wins / right-wins / newer-wins`)
}

// runDedup 执行 dedup 子命令：扫描目录去重重复文件。
func runDedup(args []string) {
	fs := flag.NewFlagSet("dedup", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "只报告不修改")
	readonly := fs.Bool("readonly", false, "去重后将文件设为只读（归档场景，防止误编辑污染硬链接副本）")
	indexPath := fs.String("index", "", "增量索引路径（可选，默认在扫描目录同级创建 .dedup-index.db）")
	var exclude multiFlag
	fs.Var(&exclude, "exclude", "排除模式（** 递归 glob，可重复）")
	fs.Parse(args)

	posArgs := fs.Args()
	if len(posArgs) < 1 {
		fatal("用法: filesync dedup <目录> [--index PATH] [--dry-run] [--readonly] [--exclude PATTERN]...")
	}
	dir := posArgs[0]

	// 确定索引路径
	dbPath := *indexPath
	if dbPath == "" {
		dbPath = filepath.Join(dir, ".dedup-index.db")
	}

	// 打开索引（不存在则创建）
	idx, err := fileindex.Open(dbPath)
	if err != nil {
		fatal("打开索引失败: %v", err)
	}
	defer idx.Close()

	// 检测文件系统是否支持硬链接
	hardlink := cas.DetectMode(dir) == cas.ModeHardlink
	d := dedup.New(hasher.New(), 8, hardlink)
	d.SetReadonly(*readonly)
	stats, err := d.Run(dir, exclude, *dryRun, idx)
	if err != nil {
		fatal("去重失败: %v", err)
	}

	// 输出报告
	fmt.Printf("\n=== 去重完成 ===\n")
	fmt.Printf("扫描文件: %d\n", stats.Scanned)
	fmt.Printf("重复文件: %d（%d 组）\n", stats.DuplicateFiles, len(stats.Groups))
	if hardlink && !*dryRun {
		fmt.Printf("已去重: %d 个文件\n", stats.DedupedFiles)
		fmt.Printf("节省空间: %d 字节\n", stats.BytesSaved)
		if *readonly {
			if stats.ReadonlyFailed > 0 {
				fmt.Printf("警告: %d 个文件设只读失败（权限不足），未受归档保护\n", stats.ReadonlyFailed)
			} else {
				fmt.Println("文件已设为只读（归档保护）")
			}
		}
	} else if *dryRun {
		fmt.Println("（dry-run 模式，未修改文件）")
	} else {
		fmt.Println("（当前文件系统不支持硬链接，仅报告未修改）")
	}

	// 列出重复组
	if len(stats.Groups) > 0 {
		fmt.Printf("\n重复文件组:\n")
		for i, g := range stats.Groups {
			fmt.Printf("  组 %d（%d 字节, %d 文件）:\n", i+1, g.Size, len(g.Files))
			for _, f := range g.Files {
				mark := "  "
				for _, dd := range g.Deduped {
					if dd == f {
						mark = "→ "
						break
					}
				}
				fmt.Printf("    %s%s\n", mark, f)
			}
		}
	}
}

// multiFlag 实现 flag.Value，支持重复 flag 收集为 []string。
type multiFlag []string

func (m *multiFlag) String() string { return "" }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// runBisync 执行双向同步子命令。
func runBisync(args []string) {
	fs := flag.NewFlagSet("bisync", flag.ExitOnError)
	left := fs.String("left", "", "左端目录")
	right := fs.String("right", "", "右端目录")
	dryRun := fs.Bool("dry-run", false, "只检测不执行")
	workers := fs.Int("workers", 8, "并发数")
	conflict := fs.String("conflict", "keep-both", "冲突策略: keep-both / left-wins / right-wins / newer-wins")
	var exclude multiFlag
	fs.Var(&exclude, "exclude", "排除模式（** 递归 glob，可重复）")
	fs.Parse(args)

	if *left == "" || *right == "" {
		fatal("用法: filesync bisync --left DIR --right DIR [--dry-run] [--workers N] [--conflict STRATEGY] [--exclude PATTERN]...")
	}

	cfg := &bisync.Config{
		Left:     *left,
		Right:    *right,
		Workers:  *workers,
		Conflict: bisync.ConflictStrategy(*conflict),
		Exclude:  exclude,
	}

	b := bisync.New(cfg)
	stats, err := b.Sync(*dryRun)
	if err != nil {
		fatal("双向同步失败: %v", err)
	}

	// 输出报告
	fmt.Printf("\n=== 双向同步完成 ===\n")
	fmt.Printf("左端文件: %d\n", stats.ScannedLeft)
	fmt.Printf("右端文件: %d\n", stats.ScannedRight)
	fmt.Printf("左→右复制: %d\n", stats.LeftToRight)
	fmt.Printf("右→左复制: %d\n", stats.RightToLeft)
	fmt.Printf("冲突: %d\n", stats.Conflicts)
	fmt.Printf("左端删除: %d\n", stats.DeletedLeft)
	fmt.Printf("右端删除: %d\n", stats.DeletedRight)
	fmt.Printf("未变化: %d\n", stats.Unchanged)
	fmt.Printf("复制字节: %d\n", stats.BytesCopied)
	if *dryRun {
		fmt.Println("\n（dry-run 模式，未修改文件）")
	}
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
