//go:build windows

package lock

/*
#include <windows.h>

static int isProcessAlive(DWORD pid) {
    if (pid <= 0) return 0;
    HANDLE h = OpenProcess(SYNCHRONIZE, FALSE, pid);
    if (h == NULL) return 0;
    CloseHandle(h);
    return 1;
}
*/
import "C"

func isProcessAlive(pid int) bool {
	return C.isProcessAlive(C.DWORD(pid)) != 0
}
