//go:build !windows

package copier

import (
	"errors"
	"syscall"
)

// isLockedError 判断错误是否为文件被占用/锁定（设计 §10：跳过并记录）。
// Unix 上检查 EBUSY（资源忙）、EACCES（权限不足，可能被其他进程锁定）、EAGAIN（资源暂不可用）。
func isLockedError(err error) bool {
	var sysErr syscall.Errno
	if errors.As(err, &sysErr) {
		return sysErr == syscall.EBUSY || sysErr == syscall.EACCES || sysErr == syscall.EAGAIN
	}
	return false
}
