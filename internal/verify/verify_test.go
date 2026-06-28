package verify

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ljw/filesync/internal/cas"
	"github.com/ljw/filesync/internal/hasher"
	"github.com/ljw/filesync/internal/index"
)

func TestVerify_AllConsistent(t *testing.T) {
	targetRoot := t.TempDir()
	objectsRoot := filepath.Join(targetRoot, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c, _ := cas.New(targetRoot, objectsRoot)
	idx, _ := index.Open(filepath.Join(targetRoot, ".filesync", "index.db"))
	defer idx.Close()
	h := hasher.New()

	// 同步一个文件：写 object + 镜像 + 索引
	src := filepath.Join(t.TempDir(), "s")
	os.WriteFile(src, []byte("content"), 0644)
	key, _ := h.HashFile(src)
	c.EnsureObject(src, key)
	dest := filepath.Join(targetRoot, "Project", "a.txt")
	os.MkdirAll(filepath.Dir(dest), 0755)
	if c.Mode() == cas.ModeHardlink {
		c.PlaceFileHardlink(key, dest)
	} else {
		c.PlaceFileCopy(key, dest)
	}
	idx.PutFile("Project/a.txt", index.FileRecord{Size: 7, ObjectKey: key})

	v := New(c, idx, h, targetRoot)
	stats, err := v.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Failed != 0 {
		t.Errorf("failed = %d, want 0: %+v", stats.Failed, stats.Errors)
	}
	if stats.Checked != 1 {
		t.Errorf("checked = %d, want 1", stats.Checked)
	}
}

func TestVerify_DetectMismatch(t *testing.T) {
	targetRoot := t.TempDir()
	objectsRoot := filepath.Join(targetRoot, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c, _ := cas.New(targetRoot, objectsRoot)
	idx, _ := index.Open(filepath.Join(targetRoot, ".filesync", "index.db"))
	defer idx.Close()
	h := hasher.New()

	// 镜像内容与索引记录的 objectKey 不一致
	dest := filepath.Join(targetRoot, "a.txt")
	os.WriteFile(dest, []byte("actual"), 0644)
	idx.PutFile("a.txt", index.FileRecord{Size: 6, ObjectKey: "h3:wrong"})

	v := New(c, idx, h, targetRoot)
	stats, _ := v.Run()
	if stats.Failed != 1 {
		t.Errorf("failed = %d, want 1", stats.Failed)
	}
}
