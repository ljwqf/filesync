// Package disk 提供磁盘空间查询与同步前空间预估。
package disk

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// FreeSpace 返回 path 所在卷的可用字节数。
func FreeSpace(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("utf16 path: %w", err)
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return 0, fmt.Errorf("get free space %s: %w", path, err)
	}
	return free, nil
}
