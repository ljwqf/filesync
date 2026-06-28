// Package scanner 扫描源目录，收集文件元信息。
package scanner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

// FileInfo 描述一个待同步文件。
type FileInfo struct {
	RelPath string    // 相对源根的路径（正斜杠）
	AbsPath string    // 绝对路径
	Size    int64
	Mtime   time.Time
}

// Scan 扫描 srcRoot，返回所有未排除的文件与其所在目录（含空目录，绝对路径）。
// exclude 是 ** glob 模式列表（相对源根，大小写不敏感匹配）。
func Scan(srcRoot string, exclude []string) (files []FileInfo, dirs []string, err error) {
	srcRoot, err = filepath.Abs(srcRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("abs src root: %w", err)
	}
	lowerExclude := make([]string, len(exclude))
	for i, p := range exclude {
		lowerExclude[i] = strings.ToLower(p)
	}

	type visitedEntry struct {
		path string
		fi   os.FileInfo
	}
	var visitedDirs []visitedEntry

	err = filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, rerr := filepath.Rel(srcRoot, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		relLower := strings.ToLower(rel)

		if d.IsDir() {
			// 跳过符号链接目录（不跟随），防御循环
			if d.Type()&fs.ModeSymlink != 0 {
				return fs.SkipDir
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			// 循环检测：与已访问目录比较 inode
			for _, v := range visitedDirs {
				if os.SameFile(fi, v.fi) {
					return fs.SkipDir
				}
			}
			visitedDirs = append(visitedDirs, visitedEntry{path, fi})
			if rel == "." {
				return nil
			}
			// 排除目录
			if matchAny(lowerExclude, relLower+"/") || matchAny(lowerExclude, relLower) {
				return fs.SkipDir
			}
			dirs = append(dirs, path)
			return nil
		}

		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if matchAny(lowerExclude, relLower) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, FileInfo{
			RelPath: rel,
			AbsPath: path,
			Size:    info.Size(),
			Mtime:   info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk %s: %w", srcRoot, err)
	}
	return files, dirs, nil
}

// matchAny 判断 path 是否匹配任意 pattern（均已小写）。
func matchAny(patterns []string, path string) bool {
	for _, p := range patterns {
		ok, _ := doublestar.Match(p, path)
		if ok {
			return true
		}
	}
	return false
}
