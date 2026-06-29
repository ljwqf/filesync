package copier

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ljwqf/filesync/internal/cas"
	"github.com/ljwqf/filesync/internal/hasher"
	"github.com/ljwqf/filesync/internal/index"
	"github.com/ljwqf/filesync/internal/scanner"
)

func setup(t *testing.T) (root, srcDir string, c cas.CAS, idx index.Index, h hasher.Hasher) {
	t.Helper()
	root = t.TempDir()
	srcDir = filepath.Join(t.TempDir(), "src")
	os.MkdirAll(srcDir, 0755)
	objectsRoot := filepath.Join(root, ".filesync", "objects")
	os.MkdirAll(objectsRoot, 0755)
	c, _ = cas.New(root, objectsRoot)
	idx, _ = index.Open(filepath.Join(root, ".filesync", "index.db"))
	t.Cleanup(func() { idx.Close() })
	h = hasher.New()
	return
}

func TestCopier_BasicSync(t *testing.T) {
	root, srcDir, c, idx, h := setup(t)
	srcFile := filepath.Join(srcDir, "a.txt")
	os.WriteFile(srcFile, []byte("hello"), 0644)

	fi := scanner.FileInfo{RelPath: "a.txt", AbsPath: srcFile, Size: 5, Mtime: time.Now()}
	key, _ := h.HashFile(srcFile)
	task := Task{
		SrcAbs:    srcFile,
		DestAbs:   filepath.Join(root, "Project", "a.txt"),
		RelPath:   "Project/a.txt",
		ObjectKey: key,
		Size:      5,
		Mtime:     fi.Mtime,
	}
	cp := New(c, idx, h, 4)
	cp.SetTargetRoot(root)
	res := cp.Run([]Task{task})
	if res.Failed > 0 {
		t.Fatalf("failed = %d, errs = %v", res.Failed, res.Errors)
	}
	if res.Copied != 1 {
		t.Errorf("copied = %d, want 1", res.Copied)
	}
	// 目标文件存在且内容正确
	got, _ := os.ReadFile(task.DestAbs)
	if string(got) != "hello" {
		t.Errorf("dest content = %q", got)
	}
	// 索引已记录
	rec, ok, _ := idx.GetFile("Project/a.txt")
	if !ok || rec.ObjectKey != key {
		t.Errorf("index record = %+v", rec)
	}
}

func TestCopier_DedupSameContent(t *testing.T) {
	root, srcDir, c, idx, h := setup(t)
	// 两个源文件内容相同
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("same"), 0644)
	os.WriteFile(filepath.Join(srcDir, "b.txt"), []byte("same"), 0644)
	key, _ := h.HashFile(filepath.Join(srcDir, "a.txt"))

	tasks := []Task{
		{SrcAbs: filepath.Join(srcDir, "a.txt"), DestAbs: filepath.Join(root, "a.txt"), RelPath: "a.txt", ObjectKey: key, Size: 4, Mtime: time.Now()},
		{SrcAbs: filepath.Join(srcDir, "b.txt"), DestAbs: filepath.Join(root, "b.txt"), RelPath: "b.txt", ObjectKey: key, Size: 4, Mtime: time.Now()},
	}
	cp := New(c, idx, h, 4)
	cp.SetTargetRoot(root)
	res := cp.Run(tasks)
	if res.Failed != 0 {
		t.Fatalf("failed = %d", res.Failed)
	}
	// 两个目标文件内容一致
	a, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	b, _ := os.ReadFile(filepath.Join(root, "b.txt"))
	if string(a) != "same" || string(b) != "same" {
		t.Errorf("contents wrong: %q %q", a, b)
	}
	// object RefCount 应为 2
	obj, _, _ := idx.GetObject(key)
	if obj.RefCount != 2 {
		t.Errorf("RefCount = %d, want 2", obj.RefCount)
	}
}

func TestCopier_UpdateReplacesOld(t *testing.T) {
	root, srcDir, c, idx, h := setup(t)
	// 旧记录：a.txt -> h3:old
	idx.PutFile("a.txt", index.FileRecord{Size: 3, ObjectKey: "h3:old"})
	idx.PutObject("h3:old", index.ObjectRecord{RefCount: 1, Size: 3})

	// 现在源 a.txt 内容变了
	srcFile := filepath.Join(srcDir, "a.txt")
	os.WriteFile(srcFile, []byte("newcontent"), 0644)
	key, _ := h.HashFile(srcFile)

	task := Task{
		SrcAbs:    srcFile,
		DestAbs:   filepath.Join(root, "a.txt"),
		RelPath:   "a.txt",
		ObjectKey: key,
		Size:      10,
		Mtime:     time.Now(),
	}
	cp := New(c, idx, h, 4)
	cp.SetTargetRoot(root)
	res := cp.Run([]Task{task})
	if res.Failed != 0 {
		t.Fatalf("failed = %d: %v", res.Failed, res.Errors)
	}
	// 旧 object RefCount -> 0
	old, _, _ := idx.GetObject("h3:old")
	if old.RefCount != 0 {
		t.Errorf("old RefCount = %d, want 0", old.RefCount)
	}
}

func TestCopier_SourceChangedMidSync(t *testing.T) {
	root, srcDir, c, idx, h := setup(t)
	srcFile := filepath.Join(srcDir, "a.txt")
	os.WriteFile(srcFile, []byte("original"), 0644)
	fi, _ := os.Stat(srcFile)

	// 模拟扫描后源被修改（内容变 + mtime 明显推后，超出 2s 容差以触发重算）
	os.WriteFile(srcFile, []byte("modified"), 0644)
	futureMtime := fi.ModTime().Add(10 * time.Second)
	os.Chtimes(srcFile, futureMtime, futureMtime)

	task := Task{
		SrcAbs:    srcFile,
		DestAbs:   filepath.Join(root, "a.txt"),
		RelPath:   "a.txt",
		ObjectKey: "h3:wrong", // 旧哈希
		Size:      8,
		Mtime:     fi.ModTime(),
	}
	cp := New(c, idx, h, 4)
	cp.SetTargetRoot(root)
	res := cp.Run([]Task{task})
	// 应重新哈希并正确同步
	if res.Failed != 0 {
		t.Errorf("failed = %d: %v", res.Failed, res.Errors)
	}
	got, _ := os.ReadFile(task.DestAbs)
	if string(got) != "modified" {
		t.Errorf("dest = %q, want 'modified'", got)
	}
}

func TestCopier_Verify(t *testing.T) {
	root, srcDir, c, idx, h := setup(t)
	srcFile := filepath.Join(srcDir, "a.txt")
	os.WriteFile(srcFile, []byte("verifyme"), 0644)
	key, _ := h.HashFile(srcFile)

	verify := true
	task := Task{
		SrcAbs:    srcFile,
		DestAbs:   filepath.Join(root, "a.txt"),
		RelPath:   "a.txt",
		ObjectKey: key,
		Size:      8,
		Mtime:     time.Now(),
	}
	cp := New(c, idx, h, 4)
	cp.SetTargetRoot(root)
	cp.SetVerify(&verify)
	res := cp.Run([]Task{task})
	if res.Failed != 0 {
		t.Fatalf("failed = %d: %v", res.Failed, res.Errors)
	}
}

func TestCopier_ConflictMovedToConflictDir(t *testing.T) {
	root, srcDir, c, idx, h := setup(t)
	srcFile := filepath.Join(srcDir, "a.txt")
	os.WriteFile(srcFile, []byte("source_content"), 0644)
	key, _ := h.HashFile(srcFile)

	// 目标已存在内容不同的同名文件
	destDir := filepath.Join(root, "Project")
	os.MkdirAll(destDir, 0755)
	dest := filepath.Join(destDir, "a.txt")
	os.WriteFile(dest, []byte("existing_different"), 0644)

	task := Task{
		SrcAbs:    srcFile,
		DestAbs:   dest,
		RelPath:   "Project/a.txt",
		ObjectKey: key,
		Size:      14,
		Mtime:     time.Now(),
	}
	cp := New(c, idx, h, 4)
	cp.SetTargetRoot(root)
	res := cp.Run([]Task{task})
	if res.Failed != 0 {
		t.Fatalf("failed = %d: %v", res.Failed, res.Errors)
	}
	// 目标现在应为源内容
	got, _ := os.ReadFile(dest)
	if string(got) != "source_content" {
		t.Errorf("dest = %q, want 'source_content'", got)
	}
	// 旧内容应被移到 conflict 目录
	conflictDir := filepath.Join(root, ".filesync", "conflict")
	entries, _ := os.ReadDir(conflictDir)
	if len(entries) == 0 {
		t.Fatal("conflict dir empty, expected moved file")
	}
	// 验证 conflict 目录下某处存在含 "existing_different" 的文件
	var foundOld bool
	filepath.Walk(conflictDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if string(b) == "existing_different" {
			foundOld = true
		}
		return nil
	})
	if !foundOld {
		t.Error("old conflicting content not found in conflict dir")
	}
}

// TestCopier_ContextCancelSkipsRemaining 验证 ctx 取消后未处理任务被计入 skipped，
// 进行中任务仍完成（不中断当前文件）。
func TestCopier_ContextCancelSkipsRemaining(t *testing.T) {
	root, srcDir, c, idx, h := setup(t)
	// 准备多个文件
	var tasks []Task
	for i := 0; i < 5; i++ {
		name := string(rune('a' + i)) + ".txt"
		os.WriteFile(filepath.Join(srcDir, name), []byte("content"+string(rune('a'+i))), 0644)
		key, _ := h.HashFile(filepath.Join(srcDir, name))
		tasks = append(tasks, Task{
			SrcAbs:    filepath.Join(srcDir, name),
			DestAbs:   filepath.Join(root, name),
			RelPath:   name,
			ObjectKey: key,
			Size:      8,
			Mtime:     time.Now(),
		})
	}
	cp := New(c, idx, h, 1) // 单 worker 保证顺序
	cp.SetTargetRoot(root)

	// 预取消：worker 检查到后立即停止分发
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := cp.RunWithContext(ctx, tasks)
	// 全部应被跳过（单 worker 在首个任务前检测到取消）
	if res.Copied != 0 {
		t.Errorf("copied = %d, want 0 (cancelled before start)", res.Copied)
	}
	if res.Skipped != 5 {
		t.Errorf("skipped = %d, want 5", res.Skipped)
	}
}

// TestCopier_ProgressCallback 验证进度回调对每个文件触发。
func TestCopier_ProgressCallback(t *testing.T) {
	root, srcDir, c, idx, h := setup(t)
	var events []ProgressEvent
	var evMu sync.Mutex
	// 准备 3 个源文件
	for i := 0; i < 3; i++ {
		name := string(rune('a'+i)) + ".txt"
		os.WriteFile(filepath.Join(srcDir, name), []byte(name), 0644)
	}
	var tasks []Task
	for i := 0; i < 3; i++ {
		name := string(rune('a'+i)) + ".txt"
		key, _ := h.HashFile(filepath.Join(srcDir, name))
		tasks = append(tasks, Task{
			SrcAbs: filepath.Join(srcDir, name), DestAbs: filepath.Join(root, name),
			RelPath: name, ObjectKey: key, Size: 1, Mtime: time.Now(),
		})
	}
	cp := New(c, idx, h, 2)
	cp.SetTargetRoot(root)
	cp.SetProgress(func(e ProgressEvent) {
		evMu.Lock()
		events = append(events, e)
		evMu.Unlock()
	})
	cp.Run(tasks)
	if len(events) != 3 {
		t.Errorf("progress events = %d, want 3: %+v", len(events), events)
	}
	for _, e := range events {
		if !e.Copied {
			t.Errorf("event %q should be Copied=true", e.RelPath)
		}
	}
}

// TestCopier_ShouldVerify 验证小文件强制校验、大文件按开关的逻辑。
func TestCopier_ShouldVerify(t *testing.T) {
	small := int64(100)            // < 1 MiB
	large := verifyThreshold + 1   // > 1 MiB
	cases := []struct {
		verify bool
		size   int64
		want   bool
	}{
		{false, small, true},  // 小文件即使 verify=false 也强制校验
		{true, small, true},   // 小文件 verify=true 当然校验
		{false, large, false}, // 大文件 verify=false 不校验
		{true, large, true},   // 大文件 verify=true 校验
		{false, verifyThreshold, true}, // 恰好等于阈值，强制校验
	}
	for _, c := range cases {
		got := shouldVerify(c.verify, c.size)
		if got != c.want {
			t.Errorf("shouldVerify(verify=%v, size=%d) = %v, want %v", c.verify, c.size, got, c.want)
		}
	}
}

// TestCopier_IsLockedError 验证锁定错误识别。
func TestCopier_IsLockedError(t *testing.T) {
	// ERROR_SHARING_VIOLATION = 32
	if !isLockedError(syscall.Errno(32)) {
		t.Error("Errno 32 (sharing violation) should be detected as locked")
	}
	// ERROR_LOCK_VIOLATION = 33
	if !isLockedError(syscall.Errno(33)) {
		t.Error("Errno 33 (lock violation) should be detected as locked")
	}
	// 普通错误不应识别为锁定
	if isLockedError(fmt.Errorf("some other error")) {
		t.Error("generic error should not be detected as locked")
	}
	// 包装后的锁定错误也应识别
	wrapped := fmt.Errorf("open file: %w", syscall.Errno(32))
	if !isLockedError(wrapped) {
		t.Error("wrapped sharing violation should be detected as locked")
	}
}
