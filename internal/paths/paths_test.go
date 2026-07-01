package paths

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLongPath_AlreadyAbsolute(t *testing.T) {
	in := `D:\Project\file.txt`
	got := Long(in)
	if runtime.GOOS == "windows" {
		want := `\\?\`
		if !strings.HasPrefix(got, want) {
			t.Errorf("Long(%q) = %q, want %s prefix", in, got, want)
		}
	} else {
		if got != in {
			t.Errorf("Long(%q) = %q, want unchanged on Unix", in, got)
		}
	}
}

func TestLongPath_UNC(t *testing.T) {
	in := `\\server\share\file.txt`
	got := Long(in)
	if runtime.GOOS == "windows" {
		want := `\\?\UNC\`
		if !strings.HasPrefix(got, want) {
			t.Errorf("Long(UNC %q) = %q, want %s prefix", in, got, want)
		}
	} else {
		if got != in {
			t.Errorf("Long(UNC %q) = %q, want unchanged on Unix", in, got)
		}
	}
}

func TestLongPath_AlreadyPrefixed(t *testing.T) {
	in := `\\?\D:\file.txt`
	got := Long(in)
	if got != in {
		t.Errorf("Long(already-prefixed %q) = %q, want unchanged", in, got)
	}
}

func TestSanitized_Basic(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`D:\Project\file.txt`, func() string {
			if runtime.GOOS == "windows" {
				return "D__Project_file.txt"
			}
			return `D:\Project\file.txt`
		}()},
		{`..\..\evil`, func() string {
			if runtime.GOOS == "windows" {
				return "____evil"
			}
			return `..\..\evil`
		}()},
		{`a:b:c`, func() string {
			if runtime.GOOS == "windows" {
				return "a_b_c"
			}
			return "a:b:c"
		}()},
		{`path/with/slashes`, "path_with_slashes"},
	}
	for _, c := range cases {
		got := Sanitized(c.in)
		if got != c.want {
			t.Errorf("Sanitized(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitized_TooLong(t *testing.T) {
	long := strings.Repeat("a", 300)
	got := Sanitized(long)
	if len(got) > 200 {
		t.Errorf("Sanitized(long) len = %d, want <= 200", len(got))
	}
	if !strings.Contains(got, "_") {
		t.Errorf("Sanitized(long) should contain hash suffix separator")
	}
}

func TestJoinObjectPath(t *testing.T) {
	got := ObjectPath("objects", "h3:a1b2c3d4")
	// 物理文件名用纯 hex（Windows 不允许冒号）
	sep := "/"
	if runtime.GOOS == "windows" {
		sep = `\`
	}
	want := "objects" + sep + "a1" + sep + "a1b2" + sep + "a1b2c3d4"
	if got != want {
		t.Errorf("ObjectPath = %q, want %q", got, want)
	}
}

func TestIsLongPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		if !IsLong(`\\?\D:\file.txt`) {
			t.Error("IsLong should be true for \\?\\ prefixed")
		}
		if IsLong(`D:\file.txt`) {
			t.Error("IsLong should be false for plain path")
		}
	}
}

// TestMtimeClose 验证 FAT/exFAT 2 秒容差比较，覆盖 syncer 快速跳过路径依赖的边界。
func TestMtimeClose(t *testing.T) {
	base := time.Unix(1000, 0)
	cases := []struct {
		name string
		a, b time.Time
		want bool
	}{
		{"equal", base, base, true},
		{"within_tolerance", base, base.Add(1 * time.Second), true},
		{"exactly_2s", base, base.Add(2 * time.Second), true},
		{"just_over_2s", base, base.Add(2*time.Second + 1*time.Nanosecond), false},
		{"far_apart", base, base.Add(10 * time.Second), false},
		{"negative_diff_symmetric", base.Add(1500 * time.Millisecond), base, true},
		{"negative_diff_over", base.Add(5 * time.Second), base, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MtimeClose(c.a, c.b); got != c.want {
				t.Errorf("MtimeClose(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestObjectBuckets 验证从 objectKey 提取两层分桶目录名，含非法 key 拒绝。
func TestObjectBuckets(t *testing.T) {
	cases := []struct {
		name           string
		objectKey      string
		wantB1, wantB2 string
	}{
		{"valid", "h3:a1b2c3d4", "a1", "a1b2"},
		{"minimal_4hex", "h3:abcd", "ab", "abcd"},
		{"missing_prefix_still_hex", "a1b2c3d4", "a1", "a1b2"},
		{"too_short", "h3:abc", "", ""},
		{"empty", "", "", ""},
		{"traversal_non_hex", "h3:../../etc", "", ""},
		{"slash_in_hex", "h3:ab/cd", "", ""},
		{"uppercase_hex", "h3:A1B2C3D4", "A1", "A1B2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b1, b2 := ObjectBuckets(c.objectKey)
			if b1 != c.wantB1 || b2 != c.wantB2 {
				t.Errorf("ObjectBuckets(%q) = (%q, %q), want (%q, %q)",
					c.objectKey, b1, b2, c.wantB1, c.wantB2)
			}
		})
	}
}
