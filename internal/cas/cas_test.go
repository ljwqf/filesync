package cas

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func newTestCAS(t *testing.T) (CAS, string) {
	t.Helper()
	root := t.TempDir()
	objectsRoot := filepath.Join(root, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c, err := New(root, objectsRoot)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	return c, root
}

func TestEnsureObject_NewFromSrc(t *testing.T) {
	c, _ := newTestCAS(t)
	src := filepath.Join(t.TempDir(), "src.txt")
	os.WriteFile(src, []byte("content"), 0644)

	objectKey := "h3:aabbccdd"
	exists, err := c.EnsureObject(src, objectKey)
	if err != nil {
		t.Fatalf("EnsureObject: %v", err)
	}
	if exists {
		t.Error("first EnsureObject should return exists=false (newly created)")
	}
	// object 文件应存在
	objPath := c.ObjectPath(objectKey)
	if _, err := os.Stat(objPath); err != nil {
		t.Errorf("object file missing: %v", err)
	}
	// 内容一致
	got, _ := os.ReadFile(objPath)
	if string(got) != "content" {
		t.Errorf("object content = %q", got)
	}
}

func TestEnsureObject_ExistingReuse(t *testing.T) {
	c, _ := newTestCAS(t)
	src := filepath.Join(t.TempDir(), "src.txt")
	os.WriteFile(src, []byte("content"), 0644)
	objectKey := "h3:aabbccdd"

	c.EnsureObject(src, objectKey)
	exists, err := c.EnsureObject(src, objectKey)
	if err != nil {
		t.Fatalf("EnsureObject second: %v", err)
	}
	if !exists {
		t.Error("second EnsureObject should return exists=true (reuse)")
	}
}

func TestPlaceFile_CopyMode(t *testing.T) {
	c, _ := newTestCAS(t)
	// 强制 copy 模式测试（无论 FS）
	if c.Mode() != ModeCopy && c.Mode() != ModeHardlink {
		t.Fatalf("unexpected mode %v", c.Mode())
	}

	src := filepath.Join(t.TempDir(), "src.txt")
	os.WriteFile(src, []byte("content"), 0644)
	objectKey := "h3:aabbccdd"
	c.EnsureObject(src, objectKey)

	dest := filepath.Join(t.TempDir(), "dest.txt")
	if err := c.PlaceFileCopy(objectKey, dest); err != nil {
		t.Fatalf("PlaceFileCopy: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "content" {
		t.Errorf("dest content = %q", got)
	}
}

func TestPlaceFile_HardlinkMode(t *testing.T) {
	c, root := newTestCAS(t)
	if c.Mode() != ModeHardlink {
		t.Skip("hardlink mode only on NTFS; test env is not hardlink-capable or FS differs")
	}
	src := filepath.Join(t.TempDir(), "src.txt")
	os.WriteFile(src, []byte("content"), 0644)
	objectKey := "h3:aabbccdd"
	c.EnsureObject(src, objectKey)

	// dest 必须与 object 同卷（Windows 硬链接要求同卷），放在 root 下
	dest := filepath.Join(root, "dest.txt")
	if err := c.PlaceFileHardlink(objectKey, dest); err != nil {
		t.Fatalf("PlaceFileHardlink: %v", err)
	}
	// dest 与 object 应是同一文件（硬链接）
	objPath := c.ObjectPath(objectKey)
	oi, _ := os.Stat(objPath)
	di, _ := os.Stat(dest)
	if !os.SameFile(oi, di) {
		t.Error("hardlink: dest and object should be same file")
	}
}

func TestPlaceFile_OverwriteReadOnly(t *testing.T) {
	c, _ := newTestCAS(t)
	src := filepath.Join(t.TempDir(), "src.txt")
	os.WriteFile(src, []byte("new"), 0644)
	objectKey := "h3:aabbccdd"
	c.EnsureObject(src, objectKey)

	dest := filepath.Join(t.TempDir(), "dest.txt")
	os.WriteFile(dest, []byte("old"), 0444) // 只读

	if err := c.PlaceFileCopy(objectKey, dest); err != nil {
		t.Fatalf("PlaceFileCopy over readonly: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "new" {
		t.Errorf("dest content = %q, want 'new'", got)
	}
}

func TestRemoveTempObject(t *testing.T) {
	c, _ := newTestCAS(t)
	src := filepath.Join(t.TempDir(), "src.txt")
	os.WriteFile(src, []byte("content"), 0644)
	objectKey := "h3:aabbccdd"
	c.EnsureObject(src, objectKey)

	if err := c.RemoveTempObject(objectKey); err != nil {
		t.Fatalf("RemoveTempObject: %v", err)
	}
	objPath := c.ObjectPath(objectKey)
	if c.Mode() == ModeHardlink {
		// NTFS: RemoveTempObject 是 no-op，object 应仍存在
		if _, err := os.Stat(objPath); err != nil {
			t.Errorf("NTFS object should remain (no-op): %v", err)
		}
	} else {
		// exFAT: object 应被删除
		if _, err := os.Stat(objPath); !os.IsNotExist(err) {
			t.Errorf("exFAT object should be deleted, stat err = %v", err)
		}
	}
}

func TestDeleteObject(t *testing.T) {
	c, _ := newTestCAS(t)
	src := filepath.Join(t.TempDir(), "src.txt")
	os.WriteFile(src, []byte("content"), 0644)
	objectKey := "h3:aabbccdd"
	c.EnsureObject(src, objectKey)

	if err := c.DeleteObject(objectKey); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if _, err := os.Stat(c.ObjectPath(objectKey)); !os.IsNotExist(err) {
		t.Errorf("object should be deleted")
	}
}

func TestListObjects(t *testing.T) {
	c, _ := newTestCAS(t)
	src := filepath.Join(t.TempDir(), "src.txt")
	for _, k := range []string{"h3:aabb", "h3:ccdd"} {
		os.WriteFile(src, []byte(k), 0644)
		c.EnsureObject(src, k)
	}
	keys, err := c.ListObjects()
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("list len = %d, want 2: %v", len(keys), keys)
	}
}

func TestObjectPath_Format(t *testing.T) {
	c, root := newTestCAS(t)
	objectsRoot := filepath.Join(root, ".filesync", "objects")
	got := c.ObjectPath("h3:a1b2c3d4")
	// 物理文件名用纯 hex（Windows 不允许冒号）
	want := filepath.Join(objectsRoot, "a1", "a1b2", "a1b2c3d4")
	if got != want {
		t.Errorf("ObjectPath = %q, want %q", got, want)
	}
}

func TestDetectMode(t *testing.T) {
	// 在 tempdir 检测应不 panic
	root := t.TempDir()
	objectsRoot := filepath.Join(root, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	m := detectMode(objectsRoot)
	if m != ModeHardlink && m != ModeCopy {
		t.Errorf("detectMode = %v, unexpected", m)
	}
	_ = runtime.GOOS
}
