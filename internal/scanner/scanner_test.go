package scanner

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestScan_BasicFiles(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0644)
	os.WriteFile(filepath.Join(src, "b.txt"), []byte("world"), 0644)
	os.MkdirAll(filepath.Join(src, "sub"), 0755)
	os.WriteFile(filepath.Join(src, "sub", "c.txt"), []byte("deep"), 0644)

	files, dirs, err := Scan(src, nil)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("files len = %d, want 3: %+v", len(files), files)
	}
	if len(dirs) != 1 { // sub（根目录本身不计入）
		t.Errorf("dirs len = %d, want 1", len(dirs))
	}
	// 验证字段
	var foundA bool
	for _, f := range files {
		if f.RelPath == "a.txt" {
			foundA = true
			if f.Size != 5 {
				t.Errorf("a.txt size = %d, want 5", f.Size)
			}
			if f.AbsPath == "" {
				t.Error("a.txt AbsPath empty")
			}
		}
	}
	if !foundA {
		t.Error("a.txt not found")
	}
}

func TestScan_Exclude(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "keep.txt"), []byte("k"), 0644)
	os.MkdirAll(filepath.Join(src, ".git", "objects"), 0755)
	os.WriteFile(filepath.Join(src, ".git", "config"), []byte("c"), 0644)
	os.WriteFile(filepath.Join(src, "data.tmp"), []byte("t"), 0644)
	os.MkdirAll(filepath.Join(src, "node_modules", "pkg"), 0755)
	os.WriteFile(filepath.Join(src, "node_modules", "pkg", "index.js"), []byte("x"), 0644)

	exclude := []string{"**/.git/**", "**/node_modules/**", "**/*.tmp"}
	files, _, err := Scan(src, exclude)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	for _, f := range files {
		if f.RelPath == "data.tmp" || filepath.Dir(f.RelPath) == ".git" ||
			filepath.Dir(f.RelPath) == filepath.Join("node_modules", "pkg") ||
			f.RelPath == filepath.Join(".git", "config") {
			t.Errorf("excluded file leaked: %s", f.RelPath)
		}
	}
	if len(files) != 1 {
		t.Errorf("files len = %d, want 1 (keep.txt only): %+v", len(files), files)
	}
}

func TestScan_ExcludeCaseInsensitive(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "keep.txt"), []byte("k"), 0644)
	os.MkdirAll(filepath.Join(src, ".GIT"), 0755)
	os.WriteFile(filepath.Join(src, ".GIT", "config"), []byte("c"), 0644)

	exclude := []string{"**/.git/**"}
	files, _, err := Scan(src, exclude)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	for _, f := range files {
		if f.RelPath == filepath.Join(".GIT", "config") {
			t.Errorf(".GIT (uppercase) should match .git exclude rule: %s", f.RelPath)
		}
	}
}

func TestScan_EmptyDir(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0644)
	os.MkdirAll(filepath.Join(src, "empty"), 0755)

	_, dirs, err := Scan(src, nil)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	var foundEmpty bool
	for _, d := range dirs {
		if d == filepath.Join(src, "empty") || filepath.Base(d) == "empty" {
			foundEmpty = true
		}
	}
	if !foundEmpty {
		t.Error("empty dir not recorded in dirs")
	}
}

func TestScan_EmptyFile(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "empty.txt"), []byte{}, 0644)

	files, _, err := Scan(src, nil)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files len = %d, want 1", len(files))
	}
	if files[0].Size != 0 {
		t.Errorf("empty file size = %d, want 0", files[0].Size)
	}
}

// TestScan_SymlinkLoop 验证目录符号链接不导致无限递归。
// 需要文件系统支持符号链接；不支持时跳过。
func TestScan_SymlinkLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows 普通用户无符号链接权限时跳过
		// 尝试创建，失败则 skip
	}
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "real"), 0755)
	os.WriteFile(filepath.Join(src, "real", "f.txt"), []byte("f"), 0644)
	// 创建指向父目录的符号链接（构成循环）
	linkPath := filepath.Join(src, "real", "looplink")
	if err := os.Symlink(src, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	files, _, err := Scan(src, nil)
	if err != nil {
		t.Fatalf("Scan failed on loop: %v", err)
	}
	// 不应 panic 或无限递归；real/f.txt 应出现一次
	var count int
	for _, f := range files {
		if f.RelPath == "real/f.txt" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("real/f.txt count = %d, want 1 (no loop duplication)", count)
	}
}
