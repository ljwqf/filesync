package hasher

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHash_BasicContent(t *testing.T) {
	h := New()
	content := []byte("hello world")
	key, err := h.Hash(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	if !strings.HasPrefix(key, "h3:") {
		t.Errorf("key = %q, want h3: prefix", key)
	}
	// 相同内容应得相同 key
	key2, _ := h.Hash(bytes.NewReader(content))
	if key != key2 {
		t.Errorf("non-deterministic: %q != %q", key, key2)
	}
}

func TestHash_DifferentContent(t *testing.T) {
	h := New()
	k1, _ := h.Hash(bytes.NewReader([]byte("aaa")))
	k2, _ := h.Hash(bytes.NewReader([]byte("bbb")))
	if k1 == k2 {
		t.Error("different content should yield different keys")
	}
}

func TestHash_EmptyFile(t *testing.T) {
	h := New()
	key, err := h.Hash(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("Hash empty failed: %v", err)
	}
	if key != EmptyObjectKey {
		t.Errorf("empty file key = %q, want %q", key, EmptyObjectKey)
	}
}

func TestHash_FromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("file content"), 0644)

	h := New()
	key, err := h.HashFile(p)
	if err != nil {
		t.Fatalf("HashFile failed: %v", err)
	}
	if !strings.HasPrefix(key, "h3:") {
		t.Errorf("key = %q, want h3: prefix", key)
	}
}

func TestHash_LargeFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.bin")
	// 4MB 内容，验证分块读取
	data := make([]byte, 4*1024*1024)
	for i := range data {
		data[i] = byte(i)
	}
	os.WriteFile(p, data, 0644)

	h := New()
	// 文件哈希应与内存哈希一致
	wantKey, _ := h.Hash(bytes.NewReader(data))
	key, err := h.HashFile(p)
	if err != nil {
		t.Fatalf("HashFile failed: %v", err)
	}
	if key != wantKey {
		t.Errorf("HashFile(%q) = %q, want %q (memory)", p, key, wantKey)
	}
	if !strings.HasPrefix(key, "h3:") {
		t.Errorf("key = %q, want h3: prefix", key)
	}
	if len(strings.TrimPrefix(key, "h3:")) < 8 {
		t.Errorf("key hex too short: %q", key)
	}
}

func TestObjectKeyFormat(t *testing.T) {
	h := New()
	key, _ := h.Hash(bytes.NewReader([]byte("x")))
	if !strings.HasPrefix(key, "h3:") {
		t.Errorf("key = %q, want h3: prefix", key)
	}
	hex := strings.TrimPrefix(key, "h3:")
	if len(hex) < 16 {
		t.Errorf("hex part too short: %q", hex)
	}
}
