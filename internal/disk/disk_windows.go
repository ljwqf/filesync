//go:build windows

package disk

import (
	"fmt"
	"syscall"
)

/*
#include <windows.h>

static int getDiskFreeSpaceExW(const wchar_t* path, DWORD* low, DWORD* high) {
    ULARGE_INTEGER free;
    if (!GetDiskFreeSpaceExW(path, NULL, NULL, &free)) return 0;
    *low = free.LowPart;
    *high = free.HighPart;
    return 1;
}
*/
import "C"

// FreeSpace 返回 path 所在卷的可用字节数。
func FreeSpace(path string) (uint64, error) {
	u16, err := syscall.UTF16FromString(path)
	if err != nil {
		return 0, fmt.Errorf("utf16 path: %w", err)
	}

	var low, high C.DWORD
	if C.getDiskFreeSpaceExW((*C.wchar_t)(&u16[0]), &low, &high) == 0 {
		return 0, fmt.Errorf("get free space %s: %w", path, syscall.Errno(C.GetLastError()))
	}
	return uint64(high)<<32 | uint64(low), nil
}
