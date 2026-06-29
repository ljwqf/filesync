//go:build !windows

package lock

import "syscall"

// isProcessAlive 检查指定 PID 的进程是否仍在运行。
// 在 Unix 上使用 kill(pid, 0) 检测进程存在性。
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil // nil = 进程存在且有权信号
}
