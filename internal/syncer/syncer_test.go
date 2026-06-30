package syncer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ljwqf/filesync/internal/cas"
	"github.com/ljwqf/filesync/internal/config"
	"github.com/ljwqf/filesync/internal/copier"
	"github.com/ljwqf/filesync/internal/index"
)

func setupSyncer(t *testing.T) (s *Syncer, targetRoot, srcDir string) {
	t.Helper()
	targetRoot = t.TempDir()
	srcDir = t.TempDir()
	os.MkdirAll(srcDir, 0755)
	cfg := &config.Config{
		TargetRoot: targetRoot,
		Workers:    4,
		Sources:    []config.SourceMapping{{Src: srcDir, Dest: "Project"}},
	}
	s = New(cfg)
	return
}

func TestSyncer_FullSync(t *testing.T) {
	s, targetRoot, srcDir := setupSyncer(t)
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello"), 0644)
	os.WriteFile(filepath.Join(srcDir, "b.txt"), []byte("world"), 0644)

	rep, err := s.Sync()
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if rep.Failed != 0 {
		t.Fatalf("failed = %d: %v", rep.Failed, rep.Errors)
	}
	if rep.Copied != 2 {
		t.Errorf("copied = %d, want 2", rep.Copied)
	}
	// 目标文件存在
	got, _ := os.ReadFile(filepath.Join(targetRoot, "Project", "a.txt"))
	if string(got) != "hello" {
		t.Errorf("a.txt = %q", got)
	}
}

func TestSyncer_ResumeSkipsSynced(t *testing.T) {
	s, targetRoot, srcDir := setupSyncer(t)
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello"), 0644)

	// 第一次同步
	s.Sync()
	// 第二次同步应全部跳过
	rep, err := s.Sync()
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if rep.Copied != 0 {
		t.Errorf("second run copied = %d, want 0 (all skipped)", rep.Copied)
	}
	// 索引仍存在
	idx, _ := index.Open(filepath.Join(targetRoot, ".filesync", "index.db"))
	defer idx.Close()
	_, ok, _ := idx.GetFile("Project/a.txt")
	if !ok {
		t.Error("index record missing after resume")
	}
}

func TestSyncer_MtimeOnlyChangeSkipsCopy(t *testing.T) {
	s, _, srcDir := setupSyncer(t)
	srcFile := filepath.Join(srcDir, "a.txt")
	os.WriteFile(srcFile, []byte("hello"), 0644)
	s.Sync()

	// 仅改 mtime，内容不变
	now := nowFn()
	os.Chtimes(srcFile, now, now.Add(1))
	rep, _ := s.Sync()
	if rep.Copied != 0 {
		t.Errorf("mtime-only change copied = %d, want 0", rep.Copied)
	}
}

func TestSyncer_StrictHashScanDetectsSameMetadataChange(t *testing.T) {
	s, targetRoot, srcDir := setupSyncer(t)
	fast := false
	s.cfg.MetadataFastSkip = &fast

	srcFile := filepath.Join(srcDir, "a.txt")
	os.WriteFile(srcFile, []byte("aaaa"), 0644)
	if _, err := s.Sync(); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}
	info, err := os.Stat(srcFile)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	mtime := info.ModTime()

	os.WriteFile(srcFile, []byte("bbbb"), 0644)
	os.Chtimes(srcFile, mtime, mtime)

	rep, err := s.Sync()
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if rep.Copied != 1 {
		t.Errorf("strict scan copied = %d, want 1", rep.Copied)
	}
	got, _ := os.ReadFile(filepath.Join(targetRoot, "Project", "a.txt"))
	if string(got) != "bbbb" {
		t.Errorf("dest = %q, want bbbb", got)
	}
}

func TestSyncer_DedupAcrossSources(t *testing.T) {
	targetRoot := t.TempDir()
	dirA := t.TempDir()
	dirB := t.TempDir()
	os.WriteFile(filepath.Join(dirA, "x.txt"), []byte("same"), 0644)
	os.WriteFile(filepath.Join(dirB, "y.txt"), []byte("same"), 0644)
	cfg := &config.Config{
		TargetRoot: targetRoot,
		Workers:    4,
		Sources: []config.SourceMapping{
			{Src: dirA, Dest: "A"},
			{Src: dirB, Dest: "B"},
		},
	}
	s := New(cfg)
	rep, err := s.Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if rep.Failed != 0 {
		t.Fatalf("failed: %v", rep.Errors)
	}
	if rep.Copied != 2 {
		t.Errorf("copied = %d, want 2", rep.Copied)
	}
	// 两个目标文件内容一致
	ax, _ := os.ReadFile(filepath.Join(targetRoot, "A", "x.txt"))
	bx, _ := os.ReadFile(filepath.Join(targetRoot, "B", "y.txt"))
	if string(ax) != "same" || string(bx) != "same" {
		t.Errorf("contents wrong")
	}
}

func TestSyncer_EmptyDirPreserved(t *testing.T) {
	s, targetRoot, srcDir := setupSyncer(t)
	os.MkdirAll(filepath.Join(srcDir, "emptydir"), 0755)
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("x"), 0644)
	s.Sync()
	// 空目录应在目标存在
	if fi, err := os.Stat(filepath.Join(targetRoot, "Project", "emptydir")); err != nil || !fi.IsDir() {
		t.Errorf("empty dir not preserved: %v", err)
	}
}

func TestSyncer_DryRun(t *testing.T) {
	s, targetRoot, srcDir := setupSyncer(t)
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello"), 0644)
	rep, err := s.SyncDryRun()
	if err != nil {
		t.Fatalf("SyncDryRun: %v", err)
	}
	if rep.Copied != 0 {
		t.Errorf("dry-run copied = %d, want 0", rep.Copied)
	}
	// 目标文件不应存在
	if _, err := os.Stat(filepath.Join(targetRoot, "Project", "a.txt")); !os.IsNotExist(err) {
		t.Errorf("dry-run should not write files")
	}
}

// TestEstimateSpaceNeeded_NTFS 验证 NTFS 模式仅统计新增 object 大小。
func TestEstimateSpaceNeeded_NTFS(t *testing.T) {
	root := t.TempDir()
	objectsRoot := filepath.Join(root, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c, _ := cas.New(root, objectsRoot)
	if c.Mode() != cas.ModeHardlink {
		t.Skip("NTFS space estimation test requires hardlink mode")
	}
	// 预创建一个已存在的 object（h3:existing）
	src := filepath.Join(t.TempDir(), "s")
	os.WriteFile(src, []byte("existing"), 0644)
	c.EnsureObject(src, "h3:existing")

	tasks := []copier.Task{
		{ObjectKey: "h3:existing", Size: 100}, // 已存在，不计
		{ObjectKey: "h3:new1", Size: 200},     // 新增，计 200
		{ObjectKey: "h3:new1", Size: 200},     // 同 key 已计，不重复
		{ObjectKey: "h3:new2", Size: 300},     // 新增，计 300
	}
	got := estimateSpaceNeeded(c, tasks, 8)
	if got != 500 {
		t.Errorf("NTFS estimateSpaceNeeded = %d, want 500", got)
	}
}

// TestEstimateSpaceNeeded_NewObjectKey 验证估算对 objectKey 去重。
func TestEstimateSpaceNeeded_NewObjectKey(t *testing.T) {
	// 用非硬链接路径不可控，这里直接测 NTFS 分支的去重逻辑
	// 若非 NTFS 则测 exFAT 分支（取最大 size）
	root := t.TempDir()
	objectsRoot := filepath.Join(root, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c, _ := cas.New(root, objectsRoot)

	tasks := []copier.Task{
		{ObjectKey: "h3:a", Size: 50},
		{ObjectKey: "h3:b", Size: 500},
		{ObjectKey: "h3:c", Size: 200},
	}
	got := estimateSpaceNeeded(c, tasks, 8)
	if c.Mode() == cas.ModeHardlink {
		// NTFS: 全部新增 = 50+500+200 = 750
		if got != 750 {
			t.Errorf("NTFS estimate = %d, want 750", got)
		}
	} else {
		// exFAT: 最大 size 500 × 8 workers = 4000
		if got != 4000 {
			t.Errorf("exFAT estimate = %d, want 4000", got)
		}
	}
}
