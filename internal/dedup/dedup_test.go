package dedup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ljwqf/filesync/internal/cas"
	"github.com/ljwqf/filesync/internal/hasher"
)

func newDeduper(t *testing.T) *Deduper {
	t.Helper()
	hardlink := cas.DetectMode(t.TempDir()) == cas.ModeHardlink
	return New(hasher.New(), 4, hardlink)
}

func TestDedup_BasicDedup(t *testing.T) {
	dir := t.TempDir()
	// 3 个内容相同的文件
	content := []byte("duplicate content here")
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		os.WriteFile(filepath.Join(dir, name), content, 0644)
	}
	// 1 个不同内容文件
	os.WriteFile(filepath.Join(dir, "unique.txt"), []byte("unique"), 0644)

	d := newDeduper(t)
	stats, err := d.Run(dir, nil, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Scanned != 4 {
		t.Errorf("Scanned = %d, want 4", stats.Scanned)
	}
	if len(stats.Groups) != 1 {
		t.Fatalf("Groups = %d, want 1: %+v", len(stats.Groups), stats.Groups)
	}
	g := stats.Groups[0]
	if len(g.Files) != 3 {
		t.Errorf("group files = %d, want 3", len(g.Files))
	}

	// 硬链接模式下应去重
	if d.hardlink {
		if stats.DedupedFiles != 2 {
			t.Errorf("DedupedFiles = %d, want 2", stats.DedupedFiles)
		}
		// 验证三个文件现在是同一物理文件
		ai, _ := os.Stat(filepath.Join(dir, "a.txt"))
		bi, _ := os.Stat(filepath.Join(dir, "b.txt"))
		ci, _ := os.Stat(filepath.Join(dir, "c.txt"))
		if !os.SameFile(ai, bi) || !os.SameFile(ai, ci) {
			t.Error("a/b/c should be hardlinks to same file after dedup")
		}
		// 内容仍可正常读取
		got, _ := os.ReadFile(filepath.Join(dir, "b.txt"))
		if string(got) != string(content) {
			t.Errorf("b.txt content = %q, want %q", got, content)
		}
	}
}

func TestDedup_DifferentContentNotDeduped(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("content_x"), 0644)
	os.WriteFile(filepath.Join(dir, "y.txt"), []byte("content_y"), 0644)
	// 同 size 但内容不同
	os.WriteFile(filepath.Join(dir, "z.txt"), []byte("content_z"), 0644)

	d := newDeduper(t)
	stats, err := d.Run(dir, nil, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(stats.Groups) != 0 {
		t.Errorf("Groups = %d, want 0 (all different content): %+v", len(stats.Groups), stats.Groups)
	}
	if stats.DedupedFiles != 0 {
		t.Errorf("DedupedFiles = %d, want 0", stats.DedupedFiles)
	}
}

func TestDedup_AlreadyHardlinked(t *testing.T) {
	if cas.DetectMode(t.TempDir()) != cas.ModeHardlink {
		t.Skip("requires hardlink support")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "orig.txt"), []byte("same"), 0644)
	// 创建硬链接（已是同一物理文件）
	os.Link(filepath.Join(dir, "orig.txt"), filepath.Join(dir, "link.txt"))

	d := newDeduper(t)
	stats, err := d.Run(dir, nil, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 已是硬链接，不应重复处理
	if stats.DedupedFiles != 0 {
		t.Errorf("DedupedFiles = %d, want 0 (already hardlinked)", stats.DedupedFiles)
	}
}

func TestDedup_DryRun(t *testing.T) {
	dir := t.TempDir()
	content := []byte("dup content")
	os.WriteFile(filepath.Join(dir, "a.txt"), content, 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), content, 0644)

	d := newDeduper(t)
	stats, err := d.Run(dir, nil, true) // dryRun
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 应报告重复组但不修改文件
	if len(stats.Groups) != 1 {
		t.Errorf("Groups = %d, want 1", len(stats.Groups))
	}
	if stats.DedupedFiles != 0 {
		t.Errorf("DedupedFiles = %d, want 0 (dry-run)", stats.DedupedFiles)
	}
	// 文件未被修改（仍是独立文件，非硬链接）
	ai, _ := os.Stat(filepath.Join(dir, "a.txt"))
	bi, _ := os.Stat(filepath.Join(dir, "b.txt"))
	if d.hardlink && os.SameFile(ai, bi) {
		t.Error("dry-run should not create hardlinks")
	}
}

func TestDedup_EmptyFiles(t *testing.T) {
	dir := t.TempDir()
	// 多个空文件（size=0）
	os.WriteFile(filepath.Join(dir, "e1.txt"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "e2.txt"), []byte{}, 0644)
	os.WriteFile(filepath.Join(dir, "e3.txt"), []byte{}, 0644)

	d := newDeduper(t)
	stats, err := d.Run(dir, nil, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 空文件 size=0，组内文件 ≥2 仍应被识别为重复组
	if len(stats.Groups) != 1 {
		t.Fatalf("Groups = %d, want 1 (empty files): %+v", len(stats.Groups), stats.Groups)
	}
	if d.hardlink && stats.DedupedFiles != 2 {
		t.Errorf("DedupedFiles = %d, want 2", stats.DedupedFiles)
	}
}

func TestDedup_PreservesMtime(t *testing.T) {
	if cas.DetectMode(t.TempDir()) != cas.ModeHardlink {
		t.Skip("requires hardlink support")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("mtime test"), 0644)
	// b.txt 设置特定 mtime
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("mtime test"), 0644)
	specificTime := time.Date(2020, 1, 15, 10, 30, 0, 0, time.UTC)
	os.Chtimes(filepath.Join(dir, "b.txt"), specificTime, specificTime)

	d := newDeduper(t)
	stats, err := d.Run(dir, nil, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(stats.Groups) != 1 {
		t.Fatalf("Groups = %d, want 1", len(stats.Groups))
	}
	// 硬链接共享 inode，mtime 也共享。去重后整组统一用代表文件 mtime。
	// 验证代表文件 mtime 被 rename/link 操作后仍保留原始值。
	rep := stats.Groups[0].Representative
	repInfo, _ := os.Stat(rep)
	got := repInfo.ModTime().UTC()
	// 代表文件的 mtime 应保留：a.txt(当前) 或 b.txt(2020)，取决于谁被选为代表
	repBase := filepath.Base(rep)
	wantTime := time.Now().UTC()
	if repBase == "b.txt" {
		wantTime = specificTime
	}
	diff := got.Sub(wantTime)
	if diff < 0 {
		diff = -diff
	}
	// 容差：代表为 b.txt 时 2 秒；代表为 a.txt 时允许较大窗口（当前时间漂移）
	if diff > 5*time.Second {
		t.Errorf("representative %s mtime = %v, want ~%v (diff %v)", repBase, got, wantTime, diff)
	}
	// 验证组内所有文件共享同一 mtime（硬链接特性）
	for _, f := range stats.Groups[0].Files {
		fi, _ := os.Stat(f)
		fd := fi.ModTime().Sub(repInfo.ModTime())
		if fd < 0 {
			fd = -fd
		}
		if fd > 2*time.Second {
			t.Errorf("%s mtime = %v differs from representative %v (should share inode)", filepath.Base(f), fi.ModTime(), repInfo.ModTime())
		}
	}
}

// TestDedup_PreservesMtimeBothRoles 验证 shared-inode mtime 覆盖修复：
// 无论哪个文件被选作代表，去重后代表文件的 mtime 都应保留，
// 且组内所有硬链接文件共享同一 mtime（硬链接物理特性）。
func TestDedup_PreservesMtimeBothRoles(t *testing.T) {
	if cas.DetectMode(t.TempDir()) != cas.ModeHardlink {
		t.Skip("requires hardlink support")
	}
	for _, scenario := range []string{"a_custom", "b_custom"} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "a.txt"), []byte("mtime test"), 0644)
		os.WriteFile(filepath.Join(dir, "b.txt"), []byte("mtime test"), 0644)
		customTime := time.Date(2020, 1, 15, 10, 30, 0, 0, time.UTC)
		customFile := "a.txt"
		if scenario == "b_custom" {
			customFile = "b.txt"
		}
		os.Chtimes(filepath.Join(dir, customFile), customTime, customTime)

		d := newDeduper(t)
		stats, err := d.Run(dir, nil, false)
		if err != nil {
			t.Fatalf("Run (%s): %v", scenario, err)
		}
		if stats.DedupedFiles != 1 {
			t.Fatalf("(%s) DedupedFiles = %d, want 1", scenario, stats.DedupedFiles)
		}
		// 代表文件 mtime 必须保留（无论代表是谁）
		rep := stats.Groups[0].Representative
		repInfo, _ := os.Stat(rep)
		repBase := filepath.Base(rep)
		wantTime := time.Now().UTC()
		if repBase == customFile {
			wantTime = customTime
		}
		diff := repInfo.ModTime().UTC().Sub(wantTime)
		if diff < 0 {
			diff = -diff
		}
		if diff > 5*time.Second {
			t.Errorf("(%s) representative %s mtime = %v, want ~%v (diff %v)", scenario, repBase, repInfo.ModTime().UTC(), wantTime, diff)
		}
		// 所有硬链接文件共享同一 mtime
		for _, f := range stats.Groups[0].Files {
			fi, _ := os.Stat(f)
			fd := fi.ModTime().Sub(repInfo.ModTime())
			if fd < 0 {
				fd = -fd
			}
			if fd > 2*time.Second {
				t.Errorf("(%s) %s mtime differs from representative (should share inode)", scenario, filepath.Base(f))
			}
		}
	}
}

func TestDedup_Report(t *testing.T) {
	dir := t.TempDir()
	// 组1: 3 个相同文件（10 字节）
	os.WriteFile(filepath.Join(dir, "a1.txt"), []byte("0123456789"), 0644)
	os.WriteFile(filepath.Join(dir, "a2.txt"), []byte("0123456789"), 0644)
	os.WriteFile(filepath.Join(dir, "a3.txt"), []byte("0123456789"), 0644)
	// 组2: 2 个相同文件（5 字节）
	os.WriteFile(filepath.Join(dir, "b1.txt"), []byte("abcde"), 0644)
	os.WriteFile(filepath.Join(dir, "b2.txt"), []byte("abcde"), 0644)

	d := newDeduper(t)
	stats, err := d.Run(dir, nil, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(stats.Groups) != 2 {
		t.Fatalf("Groups = %d, want 2", len(stats.Groups))
	}
	// 重复文件总数 = (3-1) + (2-1) = 3
	if stats.DuplicateFiles != 3 {
		t.Errorf("DuplicateFiles = %d, want 3", stats.DuplicateFiles)
	}
	if d.hardlink {
		// 去重文件数 = 2 + 1 = 3
		if stats.DedupedFiles != 3 {
			t.Errorf("DedupedFiles = %d, want 3", stats.DedupedFiles)
		}
		// 节省空间 = 2*10 + 1*5 = 25
		if stats.BytesSaved != 25 {
			t.Errorf("BytesSaved = %d, want 25", stats.BytesSaved)
		}
	}
}

func TestDedup_NestedDirs(t *testing.T) {
	dir := t.TempDir()
	content := []byte("nested dup")
	os.MkdirAll(filepath.Join(dir, "sub1"), 0755)
	os.MkdirAll(filepath.Join(dir, "sub2"), 0755)
	os.WriteFile(filepath.Join(dir, "sub1", "a.txt"), content, 0644)
	os.WriteFile(filepath.Join(dir, "sub2", "b.txt"), content, 0644)

	d := newDeduper(t)
	stats, err := d.Run(dir, nil, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(stats.Groups) != 1 {
		t.Fatalf("Groups = %d, want 1 (cross-dir dup): %+v", len(stats.Groups), stats.Groups)
	}
}

func TestDedup_ExFATReportOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("report only"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("report only"), 0644)

	// hardlink=false 模拟 exFAT
	d := New(hasher.New(), 4, false)
	stats, err := d.Run(dir, nil, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 应报告重复组但不修改
	if len(stats.Groups) != 1 {
		t.Errorf("Groups = %d, want 1", len(stats.Groups))
	}
	if stats.DedupedFiles != 0 {
		t.Errorf("DedupedFiles = %d, want 0 (exFAT report only)", stats.DedupedFiles)
	}
}

func TestDedup_ReadonlyArchive(t *testing.T) {
	if cas.DetectMode(t.TempDir()) != cas.ModeHardlink {
		t.Skip("requires hardlink support")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("archive content"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("archive content"), 0644)
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("archive content"), 0644)

	d := newDeduper(t)
	d.SetReadonly(true)
	_, err := d.Run(dir, nil, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 去重后整组文件应为只读（0444）
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if fi.Mode()&0200 != 0 {
			t.Errorf("%s should be readonly (0444) in archive mode, mode=%v", name, fi.Mode())
		}
	}
	// 仍是硬链接
	ai, _ := os.Stat(filepath.Join(dir, "a.txt"))
	bi, _ := os.Stat(filepath.Join(dir, "b.txt"))
	if !os.SameFile(ai, bi) {
		t.Error("files should be hardlinked")
	}
}

func TestDedup_WritableByDefault(t *testing.T) {
	if cas.DetectMode(t.TempDir()) != cas.ModeHardlink {
		t.Skip("requires hardlink support")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("work content"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("work content"), 0644)

	d := newDeduper(t)
	// 不设 readonly（默认 false，工作目录场景）
	_, err := d.Run(dir, nil, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 去重后文件应保持可写
	for _, name := range []string{"a.txt", "b.txt"} {
		fi, _ := os.Stat(filepath.Join(dir, name))
		if fi.Mode()&0200 == 0 {
			t.Errorf("%s should remain writable by default, mode=%v", name, fi.Mode())
		}
	}
}

// TestDedup_LinkFailureRollback 验证 FINDING-001 修复的回滚路径：
// 当 os.Link 失败时，被去重的文件必须完整恢复（内容不变、非空、可读）。
// 通过注入一个总是失败的 linkFn 模拟磁盘满/权限错误场景。
func TestDedup_LinkFailureRollback(t *testing.T) {
	if cas.DetectMode(t.TempDir()) != cas.ModeHardlink {
		t.Skip("requires hardlink support")
	}
	dir := t.TempDir()
	content := []byte("rollback test content")
	os.WriteFile(filepath.Join(dir, "a.txt"), content, 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), content, 0644)

	d := newDeduper(t)
	// 注入总是失败的 linkFn，模拟磁盘满等场景下 Link 失败
	d.setLinkFn(func(oldname, newname string) error {
		return fmt.Errorf("simulated link failure (disk full)")
	})

	stats, err := d.Run(dir, nil, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Link 全部失败，不应有任何文件被去重
	if stats.DedupedFiles != 0 {
		t.Errorf("DedupedFiles = %d, want 0 (link should fail)", stats.DedupedFiles)
	}

	// 关键验证：b.txt 必须完整恢复（回滚成功）
	// 1. 文件存在
	bInfo, err := os.Stat(filepath.Join(dir, "b.txt"))
	if err != nil {
		t.Fatalf("b.txt should exist after rollback, got: %v", err)
	}
	// 2. 内容完整
	got, err := os.ReadFile(filepath.Join(dir, "b.txt"))
	if err != nil {
		t.Fatalf("read b.txt after rollback: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("b.txt content = %q, want %q (rollback should preserve content)", got, content)
	}
	// 3. 非空（大小与原始一致）
	if bInfo.Size() != int64(len(content)) {
		t.Errorf("b.txt size = %d, want %d (rollback should preserve size)", bInfo.Size(), len(content))
	}
	// 4. 不应是硬链接到 a.txt（Link 失败，未建立链接）
	aInfo, _ := os.Stat(filepath.Join(dir, "a.txt"))
	if os.SameFile(aInfo, bInfo) {
		t.Error("b.txt should NOT be hardlinked to a.txt (link failed)")
	}
	// 5. 无残留临时文件
	tmpPath := filepath.Join(dir, "b.txt") + ".filesync.dedup"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("temp file %s should be cleaned up after rollback", tmpPath)
	}
}
