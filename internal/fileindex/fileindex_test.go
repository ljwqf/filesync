package fileindex

import (
	"path/filepath"
	"testing"
	"time"
)

func tempIndex(t *testing.T) FileIndex {
	t.Helper()
	dir := t.TempDir()
	idx, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func TestPutGet(t *testing.T) {
	idx := tempIndex(t)
	now := time.Now().Truncate(time.Second)
	s := FileState{Size: 1024, Mtime: now, Hash: "h3:abc123"}

	if err := idx.Put("/foo/bar.txt", s); err != nil {
		t.Fatal(err)
	}

	got, ok, err := idx.Get("/foo/bar.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected to find /foo/bar.txt")
	}
	if got.Size != 1024 {
		t.Errorf("size = %d, want 1024", got.Size)
	}
	if got.Hash != "h3:abc123" {
		t.Errorf("hash = %s, want h3:abc123", got.Hash)
	}
	if !got.Mtime.Equal(now) {
		t.Errorf("mtime = %v, want %v", got.Mtime, now)
	}
}

func TestGetNotFound(t *testing.T) {
	idx := tempIndex(t)
	_, ok, err := idx.Get("/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected not found")
	}
}

func TestDelete(t *testing.T) {
	idx := tempIndex(t)
	if err := idx.Put("/del.txt", FileState{Size: 100}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Delete("/del.txt"); err != nil {
		t.Fatal(err)
	}
	_, ok, err := idx.Get("/del.txt")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected deleted")
	}
}

func TestIterate(t *testing.T) {
	idx := tempIndex(t)
	entries := map[string]FileState{
		"/a.txt": {Size: 1},
		"/b.txt": {Size: 2},
		"/c.txt": {Size: 3},
	}
	for k, v := range entries {
		if err := idx.Put(k, v); err != nil {
			t.Fatal(err)
		}
	}

	got := map[string]FileState{}
	err := idx.Iterate(func(path string, s FileState) bool {
		got[path] = s
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("iterated %d entries, want 3", len(got))
	}
	for k, v := range entries {
		if g, ok := got[k]; !ok || g.Size != v.Size {
			t.Errorf("entry %s: got %+v, want %+v", k, g, v)
		}
	}
}

func TestIterateStop(t *testing.T) {
	idx := tempIndex(t)
	for i := 0; i < 10; i++ {
		idx.Put("/"+string(rune('a'+i))+".txt", FileState{Size: int64(i)})
	}

	count := 0
	idx.Iterate(func(path string, s FileState) bool {
		count++
		return count < 3
	})
	if count != 3 {
		t.Errorf("iterated %d, want 3", count)
	}
}

func TestApplyBatch(t *testing.T) {
	idx := tempIndex(t)
	idx.Put("/old.txt", FileState{Size: 100})
	idx.Put("/keep.txt", FileState{Size: 200})

	puts := map[string]FileState{
		"/new.txt":  {Size: 300},
		"/keep.txt": {Size: 200, Hash: "updated"},
	}
	deletes := []string{"/old.txt"}

	if err := idx.ApplyBatch(puts, deletes); err != nil {
		t.Fatal(err)
	}

	// /old.txt should be deleted
	_, ok, _ := idx.Get("/old.txt")
	if ok {
		t.Error("/old.txt should be deleted")
	}

	// /new.txt should exist
	s, ok, _ := idx.Get("/new.txt")
	if !ok || s.Size != 300 {
		t.Errorf("/new.txt: got %+v, want Size=300", s)
	}

	// /keep.txt should be updated
	s, ok, _ = idx.Get("/keep.txt")
	if !ok || s.Hash != "updated" {
		t.Errorf("/keep.txt: got %+v, want Hash=updated", s)
	}
}

func TestIsUnchanged(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	indexed := FileState{Size: 1024, Mtime: now}

	// exact match
	if !IsUnchanged(indexed, 1024, now) {
		t.Error("expected unchanged for exact match")
	}

	// size differs
	if IsUnchanged(indexed, 2048, now) {
		t.Error("expected changed for size diff")
	}

	// mtime differs by > 2s
	if IsUnchanged(indexed, 1024, now.Add(5*time.Second)) {
		t.Error("expected changed for mtime diff > 2s")
	}

	// mtime differs by <= 2s (should be unchanged)
	if IsUnchanged(indexed, 1024, now.Add(1*time.Second)) {
		// This should be unchanged (within tolerance)
	} else {
		t.Error("expected unchanged for mtime within 2s tolerance")
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	// Open, write, close
	idx1, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	idx1.Put("/persist.txt", FileState{Size: 999, Hash: "h3:persist"})
	idx1.Close()

	// Reopen, read
	idx2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx2.Close()

	s, ok, err := idx2.Get("/persist.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected persisted entry")
	}
	if s.Size != 999 || s.Hash != "h3:persist" {
		t.Errorf("got %+v, want Size=999 Hash=h3:persist", s)
	}
}

func TestOverwrite(t *testing.T) {
	idx := tempIndex(t)
	idx.Put("/x.txt", FileState{Size: 1})
	idx.Put("/x.txt", FileState{Size: 2})

	s, ok, _ := idx.Get("/x.txt")
	if !ok || s.Size != 2 {
		t.Errorf("got %+v, want Size=2", s)
	}
}

func BenchmarkPut(b *testing.B) {
	dir := b.TempDir()
	idx, err := Open(filepath.Join(dir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer idx.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Put("/bench/file.txt", FileState{Size: int64(i)})
	}
}

func BenchmarkGet(b *testing.B) {
	dir := b.TempDir()
	idx, _ := Open(filepath.Join(dir, "bench.db"))
	defer idx.Close()
	idx.Put("/bench/file.txt", FileState{Size: 12345})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Get("/bench/file.txt")
	}
}
