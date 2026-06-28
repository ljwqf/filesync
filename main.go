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

	"github.com/ljw/filesync/internal/cas"
	"github.com/ljw/filesync/internal/config"
	"github.com/ljw/filesync/internal/dedup"
	"github.com/ljw/filesync/internal/hasher"
	"github.com/ljw/filesync/internal/index"
	"github.com/ljw/filesync/internal/lock"
	"github.com/ljw/filesync/internal/prune"
	"github.com/ljw/filesync/internal/reindex"
	"github.com/ljw/filesync/internal/report"
	"github.com/ljw/filesync/internal/syncer"
	"github.com/ljw/filesync/internal/verify"
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

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "配置文件路径")
	workers := fs.Int("workers", 0, "并发数（0=用配置默认）")
	dryRun := fs.Bool("dry-run", false, "只扫描不拷贝")
	verifyFlag := fs.Bool("verify", false, "拷贝后校验哈希（小文件始终强制校验）")
	noVerifyFlag := fs.Bool("no-verify", false, "禁用大文件校验（小文件仍强制校验）")
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
		// 进度回调：每完成一个文件输出一行
		var done int64
		s.SetProgress(func(e syncer.ProgressEvent) {
			done++
			status := "✓"
			if !e.Copied {
				status = "✗"
			}
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
  filesync sync [--config FILE] [--workers N] [--dry-run] [--verify | --no-verify]
  filesync status [--config FILE]
  filesync verify [--config FILE]
  filesync reindex [--config FILE]
  filesync prune [--config FILE] [--dry-run]
  filesync dedup <目录> [--dry-run] [--readonly] [--exclude PATTERN]...

选项:
  --config FILE    配置文件路径（默认 config.yaml）
  --workers N      并发拷贝数（0=用配置默认）
  --dry-run        只扫描不拷贝
  --verify         强制开启拷贝后哈希校验（小文件始终强制校验）
  --no-verify      禁用大文件校验（小文件仍强制校验）
  --exclude        排除模式（dedup 用，可重复）
  --readonly       去重后将文件设为只读（dedup 归档场景）`)
}

// runDedup 执行 dedup 子命令：扫描目录去重重复文件。
func runDedup(args []string) {
	fs := flag.NewFlagSet("dedup", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "只报告不修改")
	readonly := fs.Bool("readonly", false, "去重后将文件设为只读（归档场景，防止误编辑污染硬链接副本）")
	var exclude multiFlag
	fs.Var(&exclude, "exclude", "排除模式（** 递归 glob，可重复）")
	fs.Parse(args)

	posArgs := fs.Args()
	if len(posArgs) < 1 {
		fatal("用法: filesync dedup <目录> [--dry-run] [--readonly] [--exclude PATTERN]...")
	}
	dir := posArgs[0]

	// 检测文件系统是否支持硬链接
	hardlink := cas.DetectMode(dir) == cas.ModeHardlink
	d := dedup.New(hasher.New(), 8, hardlink)
	d.SetReadonly(*readonly)
	stats, err := d.Run(dir, exclude, *dryRun)
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

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
