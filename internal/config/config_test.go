package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `
target_root: "E:\\PSSD_sync"
workers: 8
verify: true
sources:
  - src: "D:\\Project"
    dest: "Project"
  - src: "D:\\Docs"
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
	if cfg.TargetRoot != `E:\PSSD_sync` {
		t.Errorf("TargetRoot = %q, want E:\\PSSD_sync", cfg.TargetRoot)
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
	if cfg.Sources[0].Src != `D:\Project` || cfg.Sources[0].Dest != "Project" {
		t.Errorf("Source[0] = %+v", cfg.Sources[0])
	}
	if len(cfg.Exclude) != 2 {
		t.Errorf("Exclude len = %d, want 2", len(cfg.Exclude))
	}
}

func TestLoad_DefaultWorkersAndVerify(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `
target_root: "E:\\PSSD_sync"
sources:
  - src: "D:\\Project"
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
	content := `
target_root: "E:\\PSSD_sync"
verify: false
sources:
  - src: "D:\\Project"
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
	content := `
sources:
  - src: "D:\\Project"
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
	content := `target_root: "E:\\PSSD_sync"`
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
	content := `
target_root: "E:\\PSSD_sync"
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
	content := `
target_root: "E:\\PSSD_sync"
sources:
  - src: "D:\\Project"
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
