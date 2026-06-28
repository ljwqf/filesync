// Package report 输出同步进度与最终统计报告。
package report

import (
	"fmt"
	"io"
	"sync"
)

// Reporter 收集同步事件并输出报告。
type Reporter struct {
	mu         sync.Mutex
	w          io.Writer
	copied     int64
	skipped    int64
	failed     int64
	bytes      int64
	dedupSaved int64
	errors     []string
	locked     []string
}

// New 创建 Reporter，输出到 w。
func New(w io.Writer) *Reporter {
	return &Reporter{w: w}
}

// SetCopied 设置已拷贝文件数与总字节数。
func (r *Reporter) SetCopied(count, bytes int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.copied = count
	r.bytes = bytes
}

// SetSkipped 设置已跳过文件数。
func (r *Reporter) SetSkipped(count int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skipped = count
}

func (r *Reporter) AddFailed(relPath string, _ error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed++
	r.errors = append(r.errors, relPath)
}

func (r *Reporter) SetDedupSaved(bytes int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dedupSaved = bytes
}

// SetLocked 设置被占用/锁定而跳过的文件列表。
func (r *Reporter) SetLocked(files []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.locked = files
}

// Finish 输出最终报告。
func (r *Reporter) Finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, "\n=== 同步完成 ===\n")
	fmt.Fprintf(r.w, "已拷贝: %d 个文件 (%s)\n", r.copied, formatBytes(r.bytes))
	fmt.Fprintf(r.w, "已跳过: %d 个文件\n", r.skipped)
	fmt.Fprintf(r.w, "去重节省: %s\n", formatBytes(r.dedupSaved))
	fmt.Fprintf(r.w, "失败: %d 个文件\n", r.failed)
	fmt.Fprintf(r.w, "锁定跳过: %d 个文件\n", len(r.locked))
	if len(r.errors) > 0 {
		fmt.Fprintf(r.w, "\n失败文件列表:\n")
		for _, e := range r.errors {
			fmt.Fprintf(r.w, "  %s\n", e)
		}
	}
	if len(r.locked) > 0 {
		fmt.Fprintf(r.w, "\n锁定文件列表（被占用，未同步）:\n")
		for _, l := range r.locked {
			fmt.Fprintf(r.w, "  %s\n", l)
		}
	}
}

// formatBytes 将字节数格式化为人类可读。
func formatBytes(b int64) string {
	const (
		KiB = 1024
		MiB = 1024 * 1024
		GiB = 1024 * 1024 * 1024
	)
	switch {
	case b >= GiB:
		return fmt.Sprintf("%.1f GiB", float64(b)/GiB)
	case b >= MiB:
		return fmt.Sprintf("%.1f MiB", float64(b)/MiB)
	case b >= KiB:
		return fmt.Sprintf("%.1f KiB", float64(b)/KiB)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
