package paths

import (
	"runtime"
	"strings"
	"testing"
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
