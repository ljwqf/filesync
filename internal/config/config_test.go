package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// testRoot 返回当前平台适用的绝对路径。
func testRoot() string {
	if runtime.GOOS == "windows" {
		return `E:\PSSD_sync`
	}
	return "/tmp/filesync-test"
}

// testSrc 返回当前平台适用的绝对源路径。
func testSrc() string {
	if runtime.GOOS == "windows" {
		return `D:\Project`
	}
	return "/tmp/project"
}

// yamlString 将 Go 字符串转为 YAML 安全引用的值（避免转义问题）。
func yamlString(s string) string {
	// 含反斜杠或冒号的路径用单引号包裹（YAML 单引号不处理转义）
	if strings.ContainsAny(s, `\:`) {
		return "'" + s + "'"
	}
	b, _ := yaml.Marshal(s)
	return string(b[:len(b)-1]) // strip newline
}

func TestLoad_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `target_root: ` + yamlString(testRoot()) + `
workers: 8
verify: true
sources:
  - src: ` + yamlString(testSrc()) + `
    dest: "Project"
  - src: ` + yamlString(testSrc()+"/Docs") + `
    dest: "Docs"
exclude:
  - "**/.git/**"
  - "**/*.tmp"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.TargetRoot != testRoot() {
		t.Errorf("TargetRoot = %q, want %q", cfg.TargetRoot, testRoot())
	}
	if cfg.Workers != 8 {
		t.Errorf("Workers = %d, want 8", cfg.Workers)
	}
	if cfg.Verify == nil || !*cfg.Verify {
		t.Errorf("Verify = %v, want true", cfg.Verify)
	}
	if len(cfg.Sources) != 2 {
		t.Fatalf("Sources len = %d, want 2", len(cfg.Sources))
	}
	if cfg.Sources[0].Src != testSrc() || cfg.Sources[0].Dest != "Project" {
		t.Errorf("Source[0] = %+v", cfg.Sources[0])
	}
	if len(cfg.Exclude) != 2 {
		t.Fatalf("Exclude len = %d, want 2", len(cfg.Exclude))
	}
}

func TestLoad_DefaultWorkersAndVerify(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `target_root: ` + yamlString(testRoot()) + `
sources:
  - src: ` + yamlString(testSrc()) + `
    dest: "Project"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Workers != 8 {
		t.Errorf("default Workers = %d, want 8", cfg.Workers)
	}
	if cfg.Verify == nil {
		t.Errorf("default Verify = nil, want non-nil (true)")
	} else if !*cfg.Verify {
		t.Errorf("default Verify = false, want true")
	}
}

func TestLoad_VerifyExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `target_root: ` + yamlString(testRoot()) + `
verify: false
sources:
  - src: ` + yamlString(testSrc()) + `
    dest: "Project"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Verify == nil {
		t.Fatal("Verify = nil, want non-nil (false)")
	}
	if *cfg.Verify {
		t.Errorf("Verify = true, want false")
	}
}

func TestLoad_MissingTargetRoot(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `sources:
  - src: ` + yamlString(testSrc()) + `
    dest: "Project"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for missing target_root, got nil")
	}
}

func TestLoad_NoSources(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `target_root: ` + yamlString(testRoot()) + ``
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for no sources, got nil")
	}
}

func TestLoad_SrcNotAbsolute(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `target_root: ` + yamlString(testRoot()) + `
sources:
  - src: "relative/path"
    dest: "Project"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for relative src, got nil")
	}
}

func TestLoad_DestHasBackslash(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `target_root: ` + yamlString(testRoot()) + `
sources:
  - src: ` + yamlString(testSrc()) + `
    dest: "Project\\Sub"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Sources[0].Dest != "Project/Sub" {
		t.Errorf("Dest = %q, want 'Project/Sub' (backslash normalized)", cfg.Sources[0].Dest)
	}
}

func TestApplyVerifyOverride(t *testing.T) {
	t.Run("no override keeps default", func(t *testing.T) {
		t1 := true
		cfg := &Config{Verify: &t1}
		cfg.ApplyVerifyOverride(false, false)
		if cfg.Verify == nil || !*cfg.Verify {
			t.Error("Verify should remain true when no override")
		}
	})
	t.Run("verify flag forces true", func(t *testing.T) {
		f := false
		cfg := &Config{Verify: &f}
		cfg.ApplyVerifyOverride(true, false)
		if cfg.Verify == nil || !*cfg.Verify {
			t.Error("Verify should become true after --verify")
		}
	})
	t.Run("no-verify flag forces false", func(t *testing.T) {
		t1 := true
		cfg := &Config{Verify: &t1}
		cfg.ApplyVerifyOverride(false, true)
		if cfg.Verify == nil || *cfg.Verify {
			t.Error("Verify should become false after --no-verify")
		}
	})
	t.Run("both flags no-verify wins", func(t *testing.T) {
		t1 := true
		cfg := &Config{Verify: &t1}
		cfg.ApplyVerifyOverride(true, true)
		if cfg.Verify == nil || *cfg.Verify {
			t.Error("Verify should be false when both flags set (no-verify wins)")
		}
	})
}
