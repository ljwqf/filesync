package report

import (
	"bytes"
	"strings"
	"testing"
)

func TestReport_Summary(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf)
	r.SetCopied(2, 300) // 2 个文件，共 300 字节
	r.SetSkipped(1)
	r.AddFailed("err.txt", nil)
	r.SetDedupSaved(500)
	r.Finish()

	out := buf.String()
	if !strings.Contains(out, "300") { // copied bytes
		t.Errorf("missing copied bytes: %s", out)
	}
	if !strings.Contains(out, "2 个文件") {
		t.Errorf("missing copied count: %s", out)
	}
	if !strings.Contains(out, "err.txt") {
		t.Errorf("missing failed file: %s", out)
	}
	if !strings.Contains(out, "500") {
		t.Errorf("missing dedup saved: %s", out)
	}
}

func TestReport_LockedFiles(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf)
	r.SetCopied(1, 100)
	r.SetLocked([]string{"locked/a.txt", "locked/b.txt"})
	r.Finish()
	out := buf.String()
	if !strings.Contains(out, "锁定跳过: 2 个文件") {
		t.Errorf("missing locked count: %s", out)
	}
	if !strings.Contains(out, "locked/a.txt") || !strings.Contains(out, "locked/b.txt") {
		t.Errorf("missing locked file list: %s", out)
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{1073741824, "1.0 GiB"},
	}
	for _, c := range cases {
		got := formatBytes(c.in)
		if got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
