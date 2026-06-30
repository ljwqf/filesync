package bisync

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ljwqf/filesync/internal/fileindex"
)

func setupBisync(t *testing.T) (left, right string, cfg *Config) {
	t.Helper()
	left = t.TempDir()
	right = t.TempDir()
	cfg = &Config{
		Left:     left,
		Right:    right,
		Workers:  2,
		Conflict: KeepBoth,
	}
	return
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(path), 0755)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBisync_LeftToRight(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "a.txt", "hello from left")

	b := New(cfg)
	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}

	if stats.LeftToRight != 1 {
		t.Errorf("LeftToRight = %d, want 1", stats.LeftToRight)
	}
	if readFile(t, right, "a.txt") != "hello from left" {
		t.Error("a.txt not copied to right")
	}
}

func TestBisync_RightToLeft(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, right, "b.txt", "hello from right")

	b := New(cfg)
	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}

	if stats.RightToLeft != 1 {
		t.Errorf("RightToLeft = %d, want 1", stats.RightToLeft)
	}
	if readFile(t, left, "b.txt") != "hello from right" {
		t.Error("b.txt not copied to left")
	}
}

func TestBisync_BothSides(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "a.txt", "from left")
	writeFile(t, right, "b.txt", "from right")

	b := New(cfg)
	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}

	// 左端新增 → 复制到右端；右端新增 → 复制到左端
	if stats.LeftToRight != 1 {
		t.Errorf("LeftToRight = %d, want 1", stats.LeftToRight)
	}
	if stats.RightToLeft != 1 {
		t.Errorf("RightToLeft = %d, want 1", stats.RightToLeft)
	}
	if readFile(t, right, "a.txt") != "from left" {
		t.Error("a.txt not in right")
	}
	if readFile(t, left, "b.txt") != "from right" {
		t.Error("b.txt not in left")
	}
}

func TestBisync_UnchangedSecondRun(t *testing.T) {
	left, _, cfg := setupBisync(t)
	writeFile(t, left, "a.txt", "content")

	b := New(cfg)
	b.Sync(false)

	// 第二次运行，无变化
	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.LeftToRight != 0 || stats.RightToLeft != 0 {
		t.Errorf("second run should have no copies: L2R=%d, R2L=%d", stats.LeftToRight, stats.RightToLeft)
	}
	if stats.Unchanged != 1 {
		t.Errorf("Unchanged = %d, want 1", stats.Unchanged)
	}
}

func TestBisync_DetectsModification(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "a.txt", "original")
	b := New(cfg)
	b.Sync(false)

	// 修改左端文件
	futureTime := time.Now().Add(5 * time.Second)
	os.WriteFile(filepath.Join(left, "a.txt"), []byte("modified"), 0644)
	os.Chtimes(filepath.Join(left, "a.txt"), futureTime, futureTime)

	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.LeftToRight != 1 {
		t.Errorf("LeftToRight = %d, want 1", stats.LeftToRight)
	}
	if readFile(t, right, "a.txt") != "modified" {
		t.Error("modification not synced to right")
	}
}

func TestBisync_DryRun(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "a.txt", "hello")

	b := New(cfg)
	stats, err := b.Sync(true) // dry run
	if err != nil {
		t.Fatal(err)
	}

	if stats.LeftToRight != 1 {
		t.Errorf("LeftToRight = %d, want 1 (dry run should detect)", stats.LeftToRight)
	}
	// 文件不应被复制
	if _, err := os.Stat(filepath.Join(right, "a.txt")); !os.IsNotExist(err) {
		t.Error("dry run should not copy files")
	}
}

func TestBisync_DeleteLeft(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "a.txt", "to be deleted")
	b := New(cfg)
	b.Sync(false)

	// 删除左端文件
	os.Remove(filepath.Join(left, "a.txt"))

	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DeletedRight != 1 {
		t.Errorf("DeletedRight = %d, want 1", stats.DeletedRight)
	}
	if _, err := os.Stat(filepath.Join(right, "a.txt")); !os.IsNotExist(err) {
		t.Error("a.txt should be deleted from right")
	}
}

func TestBisync_DeleteRight(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, right, "a.txt", "to be deleted")
	b := New(cfg)
	b.Sync(false)

	// 删除右端文件
	os.Remove(filepath.Join(right, "a.txt"))

	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DeletedLeft != 1 {
		t.Errorf("DeletedLeft = %d, want 1", stats.DeletedLeft)
	}
	if _, err := os.Stat(filepath.Join(left, "a.txt")); !os.IsNotExist(err) {
		t.Error("a.txt should be deleted from left")
	}
}

func TestBisync_ConflictLeftWins(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "a.txt", "original")
	b := New(cfg)
	b.Sync(false)

	// 两端同时修改
	os.WriteFile(filepath.Join(left, "a.txt"), []byte("left version"), 0644)
	os.Chtimes(filepath.Join(left, "a.txt"), time.Now().Add(5*time.Second), time.Now().Add(5*time.Second))
	os.WriteFile(filepath.Join(right, "a.txt"), []byte("right version"), 0644)
	os.Chtimes(filepath.Join(right, "a.txt"), time.Now().Add(6*time.Second), time.Now().Add(6*time.Second))

	cfg.Conflict = LeftWins
	b = New(cfg)
	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Conflicts != 1 {
		t.Errorf("Conflicts = %d, want 1", stats.Conflicts)
	}
	// 左端赢：右端应被左端覆盖
	if readFile(t, right, "a.txt") != "left version" {
		t.Error("right should have left version (left-wins)")
	}
}

func TestBisync_ConflictRightWins(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "a.txt", "original")
	b := New(cfg)
	b.Sync(false)

	os.WriteFile(filepath.Join(left, "a.txt"), []byte("left version"), 0644)
	os.Chtimes(filepath.Join(left, "a.txt"), time.Now().Add(5*time.Second), time.Now().Add(5*time.Second))
	os.WriteFile(filepath.Join(right, "a.txt"), []byte("right version"), 0644)
	os.Chtimes(filepath.Join(right, "a.txt"), time.Now().Add(6*time.Second), time.Now().Add(6*time.Second))

	cfg.Conflict = RightWins
	b = New(cfg)
	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Conflicts != 1 {
		t.Errorf("Conflicts = %d, want 1", stats.Conflicts)
	}
	if readFile(t, left, "a.txt") != "right version" {
		t.Error("left should have right version (right-wins)")
	}
}

func TestBisync_ConflictNewerWins(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "a.txt", "original")
	b := New(cfg)
	b.Sync(false)

	// 左端更新
	os.WriteFile(filepath.Join(left, "a.txt"), []byte("left newer"), 0644)
	os.Chtimes(filepath.Join(left, "a.txt"), time.Now().Add(10*time.Second), time.Now().Add(10*time.Second))
	// 右端较旧
	os.WriteFile(filepath.Join(right, "a.txt"), []byte("right older"), 0644)
	os.Chtimes(filepath.Join(right, "a.txt"), time.Now().Add(5*time.Second), time.Now().Add(5*time.Second))

	cfg.Conflict = NewerWins
	b = New(cfg)
	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Conflicts != 1 {
		t.Errorf("Conflicts = %d, want 1", stats.Conflicts)
	}
	// 左端更新，应覆盖右端
	if readFile(t, right, "a.txt") != "left newer" {
		t.Error("right should have left newer (newer-wins)")
	}
}

func TestBisync_ConflictKeepBoth(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "a.txt", "original")
	b := New(cfg)
	b.Sync(false)

	os.WriteFile(filepath.Join(left, "a.txt"), []byte("left version"), 0644)
	os.Chtimes(filepath.Join(left, "a.txt"), time.Now().Add(5*time.Second), time.Now().Add(5*time.Second))
	os.WriteFile(filepath.Join(right, "a.txt"), []byte("right version"), 0644)
	os.Chtimes(filepath.Join(right, "a.txt"), time.Now().Add(6*time.Second), time.Now().Add(6*time.Second))

	cfg.Conflict = KeepBoth
	b = New(cfg)
	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Conflicts != 1 {
		t.Errorf("Conflicts = %d, want 1", stats.Conflicts)
	}
	// 两端都保留，冲突文件重命名
	if _, err := os.Stat(filepath.Join(right, "a.txt.conflict-left")); err != nil {
		t.Error("conflict-left should exist in right")
	}
	if _, err := os.Stat(filepath.Join(left, "a.txt.conflict-right")); err != nil {
		t.Error("conflict-right should exist in left")
	}
}

func TestBisync_NestedDirs(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "sub/deep/nested.txt", "nested content")

	b := New(cfg)
	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.LeftToRight != 1 {
		t.Errorf("LeftToRight = %d, want 1", stats.LeftToRight)
	}
	if readFile(t, right, "sub/deep/nested.txt") != "nested content" {
		t.Error("nested file not synced")
	}
}

func TestBisync_ExcludePatterns(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "keep.txt", "keep")
	writeFile(t, left, "skip.log", "skip")
	writeFile(t, left, "node_modules/pkg.js", "skip")

	cfg.Exclude = []string{"**/*.log", "**/node_modules/**"}
	b := New(cfg)
	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.LeftToRight != 1 {
		t.Errorf("LeftToRight = %d, want 1 (only keep.txt)", stats.LeftToRight)
	}
	if _, err := os.Stat(filepath.Join(right, "skip.log")); !os.IsNotExist(err) {
		t.Error("skip.log should not be synced")
	}
}

func TestBisync_IndexesPersist(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "a.txt", "content")

	b := New(cfg)
	b.Sync(false)

	// 验证索引文件存在
	if _, err := os.Stat(filepath.Join(left, ".bisync-index.db")); err != nil {
		t.Error("left index not created")
	}
	if _, err := os.Stat(filepath.Join(right, ".bisync-index.db")); err != nil {
		t.Error("right index not created")
	}

	// 验证索引内容
	leftIdx, _ := fileindex.Open(filepath.Join(left, ".bisync-index.db"))
	defer leftIdx.Close()
	s, ok, _ := leftIdx.Get("a.txt")
	if !ok {
		t.Error("a.txt not in left index")
	}
	if s.Size != int64(len("content")) {
		t.Errorf("indexed size = %d, want %d", s.Size, len("content"))
	}
}

func TestBisync_EmptyDirs(t *testing.T) {
	_, _, cfg := setupBisync(t)

	b := New(cfg)
	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ScannedLeft != 0 || stats.ScannedRight != 0 {
		t.Errorf("expected empty dirs: L=%d, R=%d", stats.ScannedLeft, stats.ScannedRight)
	}
}

func TestBisync_DeleteBothSides(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "a.txt", "to delete")
	b := New(cfg)
	b.Sync(false)

	// 两端同时删除
	os.Remove(filepath.Join(left, "a.txt"))
	os.Remove(filepath.Join(right, "a.txt"))

	stats, err := b.Sync(false)
	if err != nil {
		t.Fatal(err)
	}
	// 两端都删除：应检测到变更但不执行复制
	if stats.DeletedLeft != 0 || stats.DeletedRight != 0 {
		t.Errorf("deleting both sides should not trigger deletes: L=%d, R=%d", stats.DeletedLeft, stats.DeletedRight)
	}
}

func TestBisync_ThreeWaySync(t *testing.T) {
	left, right, cfg := setupBisync(t)
	writeFile(t, left, "shared.txt", "original")
	b := New(cfg)
	b.Sync(false)

	// 第一次同步后，修改左端
	os.WriteFile(filepath.Join(left, "shared.txt"), []byte("left update"), 0644)
	os.Chtimes(filepath.Join(left, "shared.txt"), time.Now().Add(5*time.Second), time.Now().Add(5*time.Second))
	b.Sync(false)

	// 验证右端已更新
	if readFile(t, right, "shared.txt") != "left update" {
		t.Error("first update not synced")
	}

	// 再修改右端
	os.WriteFile(filepath.Join(right, "shared.txt"), []byte("right update"), 0644)
	os.Chtimes(filepath.Join(right, "shared.txt"), time.Now().Add(10*time.Second), time.Now().Add(10*time.Second))
	b.Sync(false)

	// 验证左端已更新
	if readFile(t, left, "shared.txt") != "right update" {
		t.Error("second update not synced")
	}
}
