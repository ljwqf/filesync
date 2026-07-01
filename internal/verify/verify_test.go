package verify

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ljwqf/filesync/internal/cas"
	"github.com/ljwqf/filesync/internal/hasher"
	"github.com/ljwqf/filesync/internal/index"
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

// TestVerify_RejectsTraversalRelPath 验证含 ../ 的恶意 relPath 不会读取 targetRoot 外的文件。
// 索引被篡改写入 "../../<external>" 后，verify 应报 failed 而非读取外部文件。
func TestVerify_RejectsTraversalRelPath(t *testing.T) {
	targetRoot := t.TempDir()
	objectsRoot := filepath.Join(targetRoot, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c, _ := cas.New(targetRoot, objectsRoot)
	idx, _ := index.Open(filepath.Join(targetRoot, ".filesync", "index.db"))
	defer idx.Close()
	h := hasher.New()

	// 在 targetRoot 之外创建一个文件
	externalDir := t.TempDir()
	externalFile := filepath.Join(externalDir, "secret.txt")
	os.WriteFile(externalFile, []byte("secret"), 0644)

	// 构造逃逸 relPath：从 targetRoot 出发 ../../到达 externalDir
	relPath, err := filepath.Rel(targetRoot, externalFile)
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}
	// relPath 应含 .. 表示逃逸
	idx.PutFile(filepath.ToSlash(relPath), index.FileRecord{Size: 6, ObjectKey: "h3:whatever"})

	v := New(c, idx, h, targetRoot)
	stats, err := v.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 应报 failed（path escapes target root），而非读取外部文件
	if stats.Failed != 1 {
		t.Errorf("failed = %d, want 1 (path escape should be rejected)", stats.Failed)
	}
	if len(stats.Errors) != 1 {
		t.Fatalf("errors = %d, want 1: %+v", len(stats.Errors), stats.Errors)
	}
	if !contains(stats.Errors[0], "escapes target root") {
		t.Errorf("error should mention path escape: %s", stats.Errors[0])
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
