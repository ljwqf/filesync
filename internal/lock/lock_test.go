package lock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAcquireAndRelease 验证正常获取与释放锁的流程。
func TestAcquireAndRelease(t *testing.T) {
	dir := t.TempDir()

	l, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// 锁文件应存在
	if _, err := os.Stat(filepath.Join(dir, lockFileName)); err != nil {
		t.Errorf("lock file not created: %v", err)
	}

	// 释放锁
	if err := l.Release(); err != nil {
		t.Errorf("Release: %v", err)
	}

	// 锁文件应被删除
	if _, err := os.Stat(filepath.Join(dir, lockFileName)); !os.IsNotExist(err) {
		t.Errorf("lock file not removed after Release")
	}
}

// TestAcquire_DuplicateRejected 验证当持有者进程仍存活时拒绝第二次获取。
func TestAcquire_DuplicateRejected(t *testing.T) {
	dir := t.TempDir()

	// 第一次获取
	l1, err := Acquire(dir)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer l1.Release()

	// 第二次获取应被拒绝（当前进程仍在运行）
	_, err = Acquire(dir)
	if err == nil {
		t.Error("second Acquire should fail when first holder is alive")
	}
}

// TestAcquire_StaleLockReacquired 验证崩溃残留锁文件可被重新获取。
func TestAcquire_StaleLockReacquired(t *testing.T) {
	dir := t.TempDir()

	// 写入一个不存在的 PID 的锁文件（模拟崩溃残留）
	staleContent := "999999999\n2026-01-01T00:00:00Z\n"
	stalePath := filepath.Join(dir, lockFileName)
	if err := os.WriteFile(stalePath, []byte(staleContent), 0644); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	// 应能成功获取（PID 不存在）
	l, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire with stale lock: %v", err)
	}
	defer l.Release()
}

// TestRelease_NilSafe 验证对 nil Lock 调用 Release 不会 panic。
func TestRelease_NilSafe(t *testing.T) {
	var l *Lock
	if err := l.Release(); err != nil {
		t.Errorf("nil Release should return nil, got %v", err)
	}
}

// TestAcquire_CorruptLockRejected 验证锁文件格式损坏（PID 无法解析）时保守拒绝，
// 而非误判为可回收的崩溃残留。覆盖 readLock 解析错误路径。
func TestAcquire_CorruptLockRejected(t *testing.T) {
	dir := t.TempDir()
	// 写入无法解析为 PID 的损坏内容
	corruptPath := filepath.Join(dir, lockFileName)
	if err := os.WriteFile(corruptPath, []byte("not-a-pid\ngarbage\n"), 0644); err != nil {
		t.Fatalf("write corrupt lock: %v", err)
	}

	_, err := Acquire(dir)
	if err == nil {
		t.Fatal("Acquire with corrupt lock should return error")
	}
	// 应提示格式损坏而非尝试回收
	if !strings.Contains(err.Error(), "格式损坏") {
		t.Errorf("error should mention corrupt format, got: %v", err)
	}
	// 损坏文件应原样保留（不自动删除，交由用户判断）
	if _, err := os.Stat(corruptPath); err != nil {
		t.Errorf("corrupt lock file should be preserved, got: %v", err)
	}
}

// TestReadLock_EmptyFile 验证空锁文件被识别为格式损坏。
func TestReadLock_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	emptyPath := filepath.Join(dir, lockFileName)
	os.WriteFile(emptyPath, []byte(""), 0644)

	_, err := Acquire(dir)
	if err == nil {
		t.Fatal("Acquire with empty lock file should return error")
	}
}
