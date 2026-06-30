package index

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestIndex(t *testing.T) Index {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.db")
	idx, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func TestFileRecord_PutGet(t *testing.T) {
	idx := newTestIndex(t)
	rec := FileRecord{
		Size:      100,
		Mtime:     time.Unix(1000, 0),
		ObjectKey: "h3:abcd",
		SyncedAt:  time.Unix(2000, 0),
	}
	if err := idx.PutFile("a/b.txt", rec); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	got, ok, err := idx.GetFile("a/b.txt")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !ok {
		t.Fatal("record not found")
	}
	if got.Size != 100 || got.ObjectKey != "h3:abcd" {
		t.Errorf("got = %+v", got)
	}
}

func TestFileRecord_NotFound(t *testing.T) {
	idx := newTestIndex(t)
	_, ok, err := idx.GetFile("missing")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if ok {
		t.Error("expected not found")
	}
}

func TestObjectRecord_PutGet(t *testing.T) {
	idx := newTestIndex(t)
	rec := ObjectRecord{Size: 200, RefCount: 1, StoredAt: time.Unix(3000, 0)}
	if err := idx.PutObject("h3:obj1", rec); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	got, ok, err := idx.GetObject("h3:obj1")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if !ok {
		t.Fatal("object not found")
	}
	if got.RefCount != 1 || got.Size != 200 {
		t.Errorf("got = %+v", got)
	}
}

func TestDeleteFile(t *testing.T) {
	idx := newTestIndex(t)
	idx.PutFile("x.txt", FileRecord{Size: 1, ObjectKey: "h3:k"})
	if err := idx.DeleteFile("x.txt"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	_, ok, _ := idx.GetFile("x.txt")
	if ok {
		t.Error("record still exists after delete")
	}
}

// TestApplySyncResult_AtomicTransaction 验证 PutFile + PutObject 在单事务内原子完成，
// 含旧 object RefCount 递减。
func TestApplySyncResult_AtomicTransaction(t *testing.T) {
	idx := newTestIndex(t)
	// 旧记录：a.txt 指向 h3:old
	idx.PutFile("a.txt", FileRecord{Size: 10, ObjectKey: "h3:old", Mtime: time.Unix(1, 0)})
	idx.PutObject("h3:old", ObjectRecord{Size: 10, RefCount: 1})
	idx.PutObject("h3:new", ObjectRecord{Size: 10, RefCount: 0})

	// 同步：a.txt 改指向 h3:new
	op := SyncOp{
		RelPath:      "a.txt",
		NewRecord:    FileRecord{Size: 10, ObjectKey: "h3:new", Mtime: time.Unix(2, 0)},
		OldObjectKey: "h3:old", // 旧 object 需递减
	}
	if err := idx.ApplySyncResult(op); err != nil {
		t.Fatalf("ApplySyncResult: %v", err)
	}

	// 新文件记录
	got, ok, _ := idx.GetFile("a.txt")
	if !ok || got.ObjectKey != "h3:new" {
		t.Errorf("file record = %+v", got)
	}
	// 旧 object RefCount -> 0
	old, _, _ := idx.GetObject("h3:old")
	if old.RefCount != 0 {
		t.Errorf("old RefCount = %d, want 0", old.RefCount)
	}
	// 新 object RefCount -> 1
	newObj, _, _ := idx.GetObject("h3:new")
	if newObj.RefCount != 1 {
		t.Errorf("new RefCount = %d, want 1", newObj.RefCount)
	}
}

// TestApplySyncResult_NewFileNoOld 验证全新文件（无旧记录）的 RefCount 递增。
func TestApplySyncResult_NewFileNoOld(t *testing.T) {
	idx := newTestIndex(t)
	op := SyncOp{
		RelPath:   "fresh.txt",
		NewRecord: FileRecord{Size: 5, ObjectKey: "h3:fresh"},
	}
	if err := idx.ApplySyncResult(op); err != nil {
		t.Fatalf("ApplySyncResult: %v", err)
	}
	got, ok, _ := idx.GetObject("h3:fresh")
	if !ok || got.RefCount != 1 {
		t.Errorf("fresh RefCount = %+v", got)
	}
}

// TestApplySyncResult_SameObjectKey 验证覆盖但 objectKey 不变（旧=新）时 RefCount 不双变。
func TestApplySyncResult_SameObjectKey(t *testing.T) {
	idx := newTestIndex(t)
	idx.PutFile("a.txt", FileRecord{Size: 10, ObjectKey: "h3:same"})
	idx.PutObject("h3:same", ObjectRecord{RefCount: 1})
	op := SyncOp{
		RelPath:      "a.txt",
		NewRecord:    FileRecord{Size: 10, ObjectKey: "h3:same"},
		OldObjectKey: "h3:same",
	}
	if err := idx.ApplySyncResult(op); err != nil {
		t.Fatalf("ApplySyncResult: %v", err)
	}
	got, _, _ := idx.GetObject("h3:same")
	if got.RefCount != 1 {
		t.Errorf("same RefCount = %d, want 1 (unchanged)", got.RefCount)
	}
}

func TestApplySyncResults_Batch(t *testing.T) {
	idx := newTestIndex(t)
	idx.PutFile("old.txt", FileRecord{Size: 3, ObjectKey: "h3:old"})
	idx.PutObject("h3:old", ObjectRecord{RefCount: 1, Size: 3})

	ops := []SyncOp{
		{
			RelPath:      "old.txt",
			NewRecord:    FileRecord{Size: 3, ObjectKey: "h3:new"},
			OldObjectKey: "h3:old",
		},
		{
			RelPath:   "fresh.txt",
			NewRecord: FileRecord{Size: 5, ObjectKey: "h3:fresh"},
		},
	}
	if err := idx.ApplySyncResults(ops); err != nil {
		t.Fatalf("ApplySyncResults: %v", err)
	}

	oldObj, _, _ := idx.GetObject("h3:old")
	if oldObj.RefCount != 0 {
		t.Errorf("old RefCount = %d, want 0", oldObj.RefCount)
	}
	newObj, _, _ := idx.GetObject("h3:new")
	if newObj.RefCount != 1 {
		t.Errorf("new RefCount = %d, want 1", newObj.RefCount)
	}
	freshObj, _, _ := idx.GetObject("h3:fresh")
	if freshObj.RefCount != 1 {
		t.Errorf("fresh RefCount = %d, want 1", freshObj.RefCount)
	}
}

func TestIterateFiles(t *testing.T) {
	idx := newTestIndex(t)
	idx.PutFile("a.txt", FileRecord{Size: 1, ObjectKey: "h3:a"})
	idx.PutFile("b.txt", FileRecord{Size: 2, ObjectKey: "h3:b"})
	var paths []string
	idx.IterateFiles(func(relPath string, r FileRecord) bool {
		paths = append(paths, relPath)
		return true
	})
	if len(paths) != 2 {
		t.Errorf("iter len = %d, want 2: %v", len(paths), paths)
	}
}

func TestIterateObjects(t *testing.T) {
	idx := newTestIndex(t)
	idx.PutObject("h3:a", ObjectRecord{RefCount: 1})
	idx.PutObject("h3:b", ObjectRecord{RefCount: 0})
	var keys []string
	idx.IterateObjects(func(key string, r ObjectRecord) bool {
		keys = append(keys, key)
		return true
	})
	if len(keys) != 2 {
		t.Errorf("iter len = %d, want 2", len(keys))
	}
}

func TestDeleteObject(t *testing.T) {
	idx := newTestIndex(t)
	idx.PutObject("h3:x", ObjectRecord{RefCount: 0})
	if err := idx.DeleteObject("h3:x"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	_, ok, _ := idx.GetObject("h3:x")
	if ok {
		t.Error("object still exists")
	}
}
