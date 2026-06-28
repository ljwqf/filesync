package disk

import (
	"testing"
)

func TestFreeSpace_NonZero(t *testing.T) {
	// 临时目录所在卷应有可用空间
	free, err := FreeSpace(".")
	if err != nil {
		t.Fatalf("FreeSpace failed: %v", err)
	}
	if free == 0 {
		t.Error("free space = 0, expected non-zero")
	}
	t.Logf("free space: %d bytes", free)
}
