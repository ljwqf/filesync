package paths

import "time"

// MtimeClose 比较两个 mtime 是否在容差内（2 秒，FAT/exFAT 精度）。
func MtimeClose(a, b time.Time) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d <= 2*time.Second
}
