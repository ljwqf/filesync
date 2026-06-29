//go:build !windows

package disk

import (
	"fmt"
	"syscall"
)

// FreeSpace 返回 path 所在卷的可用字节数。
func FreeSpace(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
