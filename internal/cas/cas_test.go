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

// TestDeleteObject_MissingTolerant 验证删除已不存在的 object 不报错（容错）。
// prune.go 注释声称 DeleteObject 对不存在文件返回 nil；prune 重跑/崩溃恢复会重复删除同一 object。
func TestDeleteObject_MissingTolerant(t *testing.T) {
	// 直接构造 copy 模式 CAS，绕过本地文件系统检测（NTFS 上 object 是只读 0444，
	// chmod 分支同样会因 ENOENT 报错，两种模式都受影响）。
	root := t.TempDir()
	objectsRoot := filepath.Join(root, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c := &fileCAS{targetRoot: root, objectsRoot: objectsRoot, mode: ModeCopy}

	// object 从未创建，删除应容错返回 nil（与 prune.go:33 注释契约一致）
	if err := c.DeleteObject("h3:aabbccdd"); err != nil {
		t.Errorf("DeleteObject on missing object should be tolerant, got: %v", err)
	}
	// 二次删除同样成功
	if err := c.DeleteObject("h3:aabbccdd"); err != nil {
		t.Errorf("second DeleteObject should still be tolerant, got: %v", err)
	}
}

// TestRemoveTempObject_InvalidKey 验证非法 objectKey（路径穿越）被拒绝而非删除外部文件。
func TestRemoveTempObject_InvalidKey(t *testing.T) {
	c, root := newTestCAS(t)
	// NTFS 模式下 RemoveTempObject 是 no-op，不触发校验；仅在 copy 模式下校验生效。
	if c.Mode() != ModeCopy {
		t.Skip("invalid-key validation only reached in copy mode")
	}
	// 诱饵文件，确保不被删除
	baitFile := filepath.Join(filepath.Dir(root), "bait-temp-do-not-delete")
	os.WriteFile(baitFile, []byte("bait"), 0644)
	defer os.Remove(baitFile)

	if err := c.RemoveTempObject("h3:../../" + filepath.Base(baitFile)); err == nil {
		t.Fatal("RemoveTempObject with traversal key should return error")
	}
	if _, err := os.Stat(baitFile); err != nil {
		t.Errorf("bait file should not be deleted: %v", err)
	}
}

// TestRemoveTempObject_MissingIdempotent 验证删除已不存在的临时 object 不报错（幂等）。
// exFAT 崩溃恢复重试场景：前次运行已清理临时 object，重试删除应成功而非卡在 chmod。
func TestRemoveTempObject_MissingIdempotent(t *testing.T) {
	// 直接构造 copy 模式 CAS，绕过本地文件系统检测（NTFS 也能跑 copy 分支）。
	root := t.TempDir()
	objectsRoot := filepath.Join(root, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c := &fileCAS{targetRoot: root, objectsRoot: objectsRoot, mode: ModeCopy}

	// object 从未创建，直接删除应幂等成功
	if err := c.RemoveTempObject("h3:aabbccdd"); err != nil {
		t.Errorf("RemoveTempObject on missing object should be idempotent, got: %v", err)
	}
	// 二次删除同样应成功
	if err := c.RemoveTempObject("h3:aabbccdd"); err != nil {
		t.Errorf("second RemoveTempObject should still be idempotent, got: %v", err)
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

// TestObjectPath_RejectsTraversal 验证恶意 objectKey（含 ../）不会生成逃逸路径。
// 这是 P1 安全修复的核心测试：防止篡改索引后通过 prune/DeleteObject 删除任意文件。
func TestObjectPath_RejectsTraversal(t *testing.T) {
	c, _ := newTestCAS(t)
	maliciousKeys := []string{
		"h3:../../etc/passwd",
		"h3:..",
		"h3:../..",
		"h3:..%2f..%2f",
		"h3:gg/../hh", // 含非 hex 字符
		"h3:ab/cd",   // 含路径分隔符
		"badprefix:aabbccdd",
		"h3:abc", // 不足 4 字符
	}
	for _, key := range maliciousKeys {
		got := c.ObjectPath(key)
		if got != "" {
			t.Errorf("ObjectPath(%q) = %q, want empty (rejected)", key, got)
		}
	}
}

// TestDeleteObject_RejectsTraversal 验证 DeleteObject 对恶意 key 返回错误而非删除外部文件。
func TestDeleteObject_RejectsTraversal(t *testing.T) {
	c, root := newTestCAS(t)
	// 在 objectsRoot 之上创建一个诱饵文件，确保它不被删除
	baitDir := filepath.Dir(filepath.Dir(root))
	baitFile := filepath.Join(baitDir, "bait-do-not-delete")
	os.WriteFile(baitFile, []byte("bait"), 0644)
	defer os.Remove(baitFile)

	err := c.DeleteObject("h3:../../" + filepath.Base(baitFile))
	if err == nil {
		t.Fatal("DeleteObject with traversal key should return error")
	}
	// 诱饵文件应仍存在
	if _, err := os.Stat(baitFile); err != nil {
		t.Errorf("bait file should not be deleted: %v", err)
	}
}
