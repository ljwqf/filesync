// Package lock 提供基于文件锁的多进程互斥机制。
// 防止多个 filesync 实例同时操作同一目标盘导致索引损坏或 CAS 对象竞态。
//
// 锁文件写入当前进程 PID 与启动时间戳。启动时若发现锁文件存在且持有者进程仍存活，
// 则拒绝启动并返回清晰错误信息。若持有者进程已退出（崩溃残留），则自动接管。
package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// lockFileName 是锁文件名（相对于 .filesync 目录）。
	lockFileName = "sync.lock"
)

// Lock 代表一个已获取的进程锁。
type Lock struct {
	path string
}

// Acquire 尝试在 dir 目录下获取锁文件。
// 使用 O_CREATE|O_EXCL 原子创建：只有一个进程能成功创建文件，其余收到“已存在”错误。
// 若锁文件已存在但持有者进程已退出（崩溃残留），则删除残留后重试一次原子创建。
//
// dir 通常是 .filesync 目录的绝对路径。
func Acquire(dir string) (*Lock, error) {
	lockPath := filepath.Join(dir, lockFileName)
	pid := os.Getpid()
	content := fmt.Sprintf("%d\n%s\n", pid, time.Now().Format(time.RFC3339))

	// 尝试原子创建锁文件
	l, err := tryCreate(lockPath, content)
	if err == nil {
		return l, nil
	}
	if !os.IsExist(err) {
		return nil, fmt.Errorf("create lock file %s: %w", lockPath, err)
	}

	// 锁文件已存在：检查持有者是否存活
	existingPID, startTime, rerr := readLock(lockPath)
	if rerr != nil {
		// 锁文件格式损坏，无法判断持有者：保守拒绝，提示用户手动处理
		return nil, fmt.Errorf("锁文件 %s 已存在但格式损坏，无法判断持有者是否存活；如确认无实例运行，请删除该文件后重试", lockPath)
	}
	if isProcessAlive(existingPID) {
		return nil, fmt.Errorf("另一个 filesync 实例正在运行 (PID %d, 启动于 %s)；如确认已退出，请删除 %s",
			existingPID, startTime.Format("2006-01-02 15:04:05"), lockPath)
	}

	// 持有者进程已退出，锁文件是崩溃残留：删除后重试一次原子创建
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale lock %s: %w", lockPath, err)
	}
	l, err = tryCreate(lockPath, content)
	if err != nil {
		return nil, fmt.Errorf("acquire lock after stale cleanup: %w", err)
	}
	return l, nil
}

// tryCreate 用 O_CREATE|O_EXCL 原子创建锁文件并写入进程信息。
// 若文件已存在返回 os.ErrExist（由 os.IsExist 识别）。
func tryCreate(lockPath, content string) (*Lock, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(lockPath)
		return nil, fmt.Errorf("write lock file %s: %w", lockPath, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(lockPath)
		return nil, fmt.Errorf("close lock file %s: %w", lockPath, err)
	}
	return &Lock{path: lockPath}, nil
}

// Release 释放锁文件。正常退出时调用。
func (l *Lock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove lock file %s: %w", l.path, err)
	}
	return nil
}

// readLock 读取锁文件，返回 PID 与启动时间。
func readLock(path string) (pid int, startTime time.Time, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, time.Time{}, err
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) < 1 {
		return 0, time.Time{}, fmt.Errorf("invalid lock file format")
	}
	pid, err = strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("parse PID: %w", err)
	}
	if len(lines) >= 2 {
		startTime, _ = time.Parse(time.RFC3339, strings.TrimSpace(lines[1]))
	}
	return pid, startTime, nil
}
