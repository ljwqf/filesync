//go:build windows

package copier

import (
	"errors"
	"syscall"
)

// Windows 共享/锁定违规错误码。
const (
	errSharingViolation = 32 // ERROR_SHARING_VIOLATION
	errLockViolation    = 33 // ERROR_LOCK_VIOLATION
)

// isLockedError 判断错误是否为文件被占用/锁定（设计 §10：跳过并记录）。
func isLockedError(err error) bool {
	var sysErr syscall.Errno
	if errors.As(err, &sysErr) {
		return sysErr == errSharingViolation || sysErr == errLockViolation
	}
	return false
}
