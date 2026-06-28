package paths

import (
	"strings"
	"testing"
)

func TestLongPath_AlreadyAbsolute(t *testing.T) {
	in := `D:\Project\file.txt`
	got := Long(in)
	want := `\\?\`
	if !strings.HasPrefix(got, want) {
		t.Errorf("Long(%q) = %q, want %s prefix", in, got, want)
	}
}

func TestLongPath_UNC(t *testing.T) {
	in := `\\server\share\file.txt`
	got := Long(in)
	want := `\\?\UNC\`
	if !strings.HasPrefix(got, want) {
		t.Errorf("Long(UNC %q) = %q, want %s prefix", in, got, want)
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
		{`D:\Project\file.txt`, "D__Project_file.txt"},
		{`..\..\evil`, "____evil"},
		{`a:b:c`, "a_b_c"},
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
	// 截断后总长应受控
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
	want := `objects\a1\a1b2\a1b2c3d4`
	if got != want {
		t.Errorf("ObjectPath = %q, want %q", got, want)
	}
}

func TestIsLongPath(t *testing.T) {
	if !IsLong(`\\?\D:\file.txt`) {
		t.Error("IsLong should be true for \\\\?\\ prefixed")
	}
	if IsLong(`D:\file.txt`) {
		t.Error("IsLong should be false for plain path")
	}
}
