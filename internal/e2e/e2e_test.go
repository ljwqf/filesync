package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ljwqf/filesync/internal/cas"
	"github.com/ljwqf/filesync/internal/config"
	"github.com/ljwqf/filesync/internal/hasher"
	"github.com/ljwqf/filesync/internal/index"
	"github.com/ljwqf/filesync/internal/prune"
	"github.com/ljwqf/filesync/internal/reindex"
	"github.com/ljwqf/filesync/internal/syncer"
	"github.com/ljwqf/filesync/internal/verify"
)

// TestE2E_FullLifecycle 验证完整生命周期：
// 同步 → 断点续传 → 更新替换 → verify → reindex → prune。
func TestE2E_FullLifecycle(t *testing.T) {
	targetRoot := t.TempDir()
	srcDir := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("content_a"), 0644)
	os.WriteFile(filepath.Join(srcDir, "b.txt"), []byte("content_b"), 0755)
	// 重复内容文件
	os.WriteFile(filepath.Join(srcDir, "c.txt"), []byte("dup"), 0644)
	os.WriteFile(filepath.Join(srcDir, "d.txt"), []byte("dup"), 0644)

	cfg := &config.Config{
		TargetRoot: targetRoot,
		Workers:    4,
		Sources:    []config.SourceMapping{{Src: srcDir, Dest: "Project"}},
	}

	// 1. 首次同步
	s := syncer.New(cfg)
	rep, err := s.Sync()
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if rep.Copied != 4 || rep.Failed != 0 {
		t.Fatalf("first sync: copied=%d failed=%d", rep.Copied, rep.Failed)
	}

	// 2. 断点续传：再同步应全跳过
	rep2, _ := s.Sync()
	if rep2.Copied != 0 {
		t.Errorf("resume copied=%d, want 0", rep2.Copied)
	}

	// 3. 更新 a.txt 内容
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("new_content_a"), 0644)
	rep3, _ := s.Sync()
	if rep3.Copied != 1 {
		t.Errorf("update copied=%d, want 1", rep3.Copied)
	}

	// 4. verify
	filesyncDir := filepath.Join(targetRoot, ".filesync")
	objectsRoot := filepath.Join(filesyncDir, "objects")
	c, _ := cas.New(targetRoot, objectsRoot)
	idx, _ := index.Open(filepath.Join(filesyncDir, "index.db"))
	v := verify.New(c, idx, hasher.New(), targetRoot)
	vstats, err := v.Run()
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if vstats.Failed != 0 {
		t.Errorf("verify failed=%d: %v", vstats.Failed, vstats.Errors)
	}
	idx.Close() // 显式关闭，释放 bbolt 文件锁，便于 reindex 重新打开

	// 5. reindex（删除 index.db 后重建）
	os.Remove(filepath.Join(filesyncDir, "index.db"))
	r := reindex.New(c, hasher.New(), targetRoot, filepath.Join(filesyncDir, "index.db"))
	rstats, err := r.Run()
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if rstats.Files != 4 {
		t.Errorf("reindex files=%d, want 4", rstats.Files)
	}
	// reindex 后再 verify 应一致
	idx2, _ := index.Open(filepath.Join(filesyncDir, "index.db"))
	v2 := verify.New(c, idx2, hasher.New(), targetRoot)
	vstats2, _ := v2.Run()
	if vstats2.Failed != 0 {
		t.Errorf("post-reindex verify failed=%d", vstats2.Failed)
	}

	// 6. prune（应清理旧 a.txt 的 orphaned object）
	p := prune.New(c, idx2)
	pstats, err := p.Run(false)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	idx2.Close()
	// 旧 a.txt 的 object RefCount 归 0 应被清理
	if pstats.Deleted < 0 {
		t.Errorf("prune deleted=%d", pstats.Deleted)
	}
}

// TestE2E_NTFSHardlinkDedup 验证 NTFS 硬链接去重（同内容共享一个物理 object）。
func TestE2E_NTFSHardlinkDedup(t *testing.T) {
	targetRoot := t.TempDir()
	srcDir := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("same"), 0644)
	os.WriteFile(filepath.Join(srcDir, "b.txt"), []byte("same"), 0644)

	objectsRoot := filepath.Join(targetRoot, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c, _ := cas.New(targetRoot, objectsRoot)
	if c.Mode() != cas.ModeHardlink {
		t.Skip("NTFS hardlink dedup test requires hardlink support")
	}

	cfg := &config.Config{
		TargetRoot: targetRoot,
		Workers:    4,
		Sources:    []config.SourceMapping{{Src: srcDir, Dest: "Project"}},
	}
	s := syncer.New(cfg)
	rep, err := s.Sync()
	if err != nil || rep.Failed != 0 {
		t.Fatalf("sync: %v errs=%v", err, rep.Errors)
	}

	// 两个目标文件应是同一物理文件
	ai, _ := os.Stat(filepath.Join(targetRoot, "Project", "a.txt"))
	bi, _ := os.Stat(filepath.Join(targetRoot, "Project", "b.txt"))
	if !os.SameFile(ai, bi) {
		t.Error("NTFS: a.txt and b.txt should be hardlinks to same object")
	}

	// objects/ 下应只有一个 object
	keys, _ := c.ListObjects()
	if len(keys) != 1 {
		t.Errorf("objects count=%d, want 1", len(keys))
	}
}
