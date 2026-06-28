package reindex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ljw/filesync/internal/cas"
	"github.com/ljw/filesync/internal/hasher"
	"github.com/ljw/filesync/internal/index"
)

func TestReindex_ExFAT(t *testing.T) {
	targetRoot := t.TempDir()
	objectsRoot := filepath.Join(targetRoot, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c, _ := cas.New(targetRoot, objectsRoot)
	h := hasher.New()

	// 准备目标盘数据：镜像文件存在，无 object，无索引
	src := filepath.Join(t.TempDir(), "s")
	os.WriteFile(src, []byte("content"), 0644)
	key, _ := h.HashFile(src)
	dest := filepath.Join(targetRoot, "Project", "a.txt")
	os.MkdirAll(filepath.Dir(dest), 0755)
	c.EnsureObject(src, key)
	c.PlaceFileCopy(key, dest)
	c.RemoveTempObject(key) // exFAT 临时 object 已删

	indexPath := filepath.Join(targetRoot, ".filesync", "index.db")
	r := New(c, h, targetRoot, indexPath)
	stats, err := r.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Files != 1 {
		t.Errorf("files = %d, want 1", stats.Files)
	}

	// 验证索引重建正确
	idx, _ := index.Open(indexPath)
	defer idx.Close()
	rec, ok, _ := idx.GetFile("Project/a.txt")
	if !ok || rec.ObjectKey != key {
		t.Errorf("reindex record = %+v, want key %s", rec, key)
	}
}

func TestReindex_NTFSSameFile(t *testing.T) {
	targetRoot := t.TempDir()
	objectsRoot := filepath.Join(targetRoot, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c, _ := cas.New(targetRoot, objectsRoot)
	h := hasher.New()

	if c.Mode() != cas.ModeHardlink {
		t.Skip("NTFS same-file test requires hardlink support")
	}
	src := filepath.Join(t.TempDir(), "s")
	os.WriteFile(src, []byte("content"), 0644)
	key, _ := h.HashFile(src)
	c.EnsureObject(src, key)
	dest := filepath.Join(targetRoot, "Project", "a.txt")
	os.MkdirAll(filepath.Dir(dest), 0755)
	c.PlaceFileHardlink(key, dest)

	indexPath := filepath.Join(targetRoot, ".filesync", "index.db")
	r := New(c, h, targetRoot, indexPath)
	stats, err := r.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Files != 1 {
		t.Errorf("files = %d, want 1", stats.Files)
	}
	idx, _ := index.Open(indexPath)
	defer idx.Close()
	rec, ok, _ := idx.GetFile("Project/a.txt")
	if !ok || rec.ObjectKey != key {
		t.Errorf("reindex record = %+v", rec)
	}
}
