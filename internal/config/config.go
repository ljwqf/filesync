package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SourceMapping 描述一个源目录到目标子目录的映射。
type SourceMapping struct {
	Src  string `yaml:"src"`
	Dest string `yaml:"dest"`
}

// Config 是同步工具的运行配置。
// Verify 用 *bool 以区分"配置中省略"(默认 true) 与"显式设为 false"。
type Config struct {
	TargetRoot string          `yaml:"target_root"`
	Workers    int             `yaml:"workers"`
	Verify     *bool           `yaml:"verify"`
	Sources    []SourceMapping `yaml:"sources"`
	Exclude    []string        `yaml:"exclude"`

	// 双向同步配置
	Mode   string      `yaml:"mode"`   // "sync" (默认) | "bisync"
	Bisync *BisyncConfig `yaml:"bisync"`
}

// BisyncConfig 是双向同步配置。
type BisyncConfig struct {
	Left     string `yaml:"left"`
	Right    string `yaml:"right"`
	Conflict string `yaml:"conflict"` // keep-both / left-wins / right-wins / newer-wins
}

// Load 从 path 读取并校验配置。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// normalize 归一化配置字段。
func (c *Config) normalize() error {
	c.TargetRoot = filepath.Clean(c.TargetRoot)
	// 默认值
	if c.Workers == 0 {
		c.Workers = 8
	}
	if c.Verify == nil {
		t := true
		c.Verify = &t
	}
	for i := range c.Sources {
		s := &c.Sources[i]
		s.Src = filepath.Clean(s.Src)
		// dest 用正斜杠
		s.Dest = strings.ReplaceAll(s.Dest, `\`, "/")
		s.Dest = filepath.ToSlash(filepath.Clean(s.Dest))
	}
	return nil
}

// validate 校验配置合法性。
func (c *Config) validate() error {
	if c.TargetRoot == "" || c.TargetRoot == "." {
		return fmt.Errorf("target_root is required")
	}
	if !filepath.IsAbs(c.TargetRoot) {
		return fmt.Errorf("target_root must be absolute: %q", c.TargetRoot)
	}
	if len(c.Sources) == 0 {
		return fmt.Errorf("at least one source is required")
	}
	for i, s := range c.Sources {
		if s.Src == "" {
			return fmt.Errorf("sources[%d].src is empty", i)
		}
		if !filepath.IsAbs(s.Src) {
			return fmt.Errorf("sources[%d].src must be absolute: %q", i, s.Src)
		}
		if s.Dest == "" {
			return fmt.Errorf("sources[%d].dest is empty", i)
		}
		if filepath.IsAbs(s.Dest) {
			return fmt.Errorf("sources[%d].dest must be relative: %q", i, s.Dest)
		}
		if strings.Contains(s.Dest, "..") {
			return fmt.Errorf("sources[%d].dest must not contain '..': %q", i, s.Dest)
		}
	}
	if c.Workers < 1 {
		return fmt.Errorf("workers must be >= 1, got %d", c.Workers)
	}
	return nil
}

// ApplyVerifyOverride 应用 CLI verify 覆盖。
// verifyFlag=true 强制开启；noVerifyFlag=true 强制关闭；两者皆 false 用配置默认。
// 两者皆 true 视为 --no-verify 优先（保守，不校验）。
func (c *Config) ApplyVerifyOverride(verifyFlag, noVerifyFlag bool) {
	if noVerifyFlag {
		f := false
		c.Verify = &f
		return
	}
	if verifyFlag {
		t := true
		c.Verify = &t
	}
}

// FilesyncDir 是目标盘上的元数据目录名。
const FilesyncDir = ".filesync"

// ObjectsDir 是 CAS 对象存储目录（相对 FilesyncDir）。
const ObjectsDir = "objects"

// IndexFile 是索引文件名（相对 FilesyncDir）。
const IndexFile = "index.db"
