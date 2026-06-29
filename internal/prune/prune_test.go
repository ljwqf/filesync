package prune

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ljwqf/filesync/internal/cas"
	"github.com/ljwqf/filesync/internal/index"
)

func setup(t *testing.T) (p *Pruner, targetRoot string, c cas.CAS, idx index.Index) {
	t.Helper()
	targetRoot = t.TempDir()
	objectsRoot := filepath.Join(targetRoot, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c, _ = cas.New(targetRoot, objectsRoot)
	idx, _ = index.Open(filepath.Join(targetRoot, ".filesync", "index.db"))
	t.Cleanup(func() { idx.Close() })
	p = New(c, idx)
	return
}

func TestPrune_OrphanedObject(t *testing.T) {
	p, _, c, idx := setup(t)
	// 创建一个 RefCount=0 的 object（合法 hex objectKey）
	src := filepath.Join(t.TempDir(), "s")
	os.WriteFile(src, []byte("x"), 0644)
	c.EnsureObject(src, "h3:01234567")
	idx.PutObject("h3:01234567", index.ObjectRecord{RefCount: 0, Orphaned: true})

	// 还有 RefCount=1 的不应删
	os.WriteFile(src, []byte("y"), 0644)
	c.EnsureObject(src, "h3:89abcdef")
	idx.PutObject("h3:89abcdef", index.ObjectRecord{RefCount: 1})

	stats, err := p.Run(false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Deleted != 1 {
		t.Errorf("deleted = %d, want 1", stats.Deleted)
	}
	// orphan 已删，keep 保留
	if _, err := os.Stat(c.ObjectPath("h3:01234567")); !os.IsNotExist(err) {
		t.Errorf("orphan not deleted")
	}
	if _, err := os.Stat(c.ObjectPath("h3:89abcdef")); err != nil {
		t.Errorf("keep should remain: %v", err)
	}
}

func TestPrune_DryRun(t *testing.T) {
	p, _, c, idx := setup(t)
	src := filepath.Join(t.TempDir(), "s")
	os.WriteFile(src, []byte("x"), 0644)
	c.EnsureObject(src, "h3:0123abcd")
	idx.PutObject("h3:0123abcd", index.ObjectRecord{RefCount: 0, Orphaned: true})

	stats, err := p.Run(true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// dry-run 统计将删除的数量，但物理不删
	if stats.Deleted != 1 {
		t.Errorf("dry-run deleted count = %d, want 1 (will-delete)", stats.Deleted)
	}
	// 文件仍在（dry-run 不实际删除）
	if _, err := os.Stat(c.ObjectPath("h3:0123abcd")); err != nil {
		t.Errorf("dry-run should not delete")
	}
}

func TestPrune_ResidualTempObject(t *testing.T) {
	// exFAT 场景：objects/ 下有残留临时 object 但索引无记录
	p, _, c, idx := setup(t)
	src := filepath.Join(t.TempDir(), "s")
	os.WriteFile(src, []byte("temp"), 0644)
	// 创建临时 object 但不写索引（用合法 hex objectKey）
	c.EnsureObject(src, "h3:deadbeef")

	stats, err := p.Run(false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 索引无记录的物理 object 应被清理
	_, _, _ = idx.GetObject("h3:deadbeef")
	if stats.Deleted < 1 {
		t.Errorf("expected residual temp object deleted, got %d", stats.Deleted)
	}
}
