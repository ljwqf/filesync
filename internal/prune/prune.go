// Package prune 清理孤立 object：
//   - NTFS: RefCount=0（orphaned）的 object 物理删除 + 清索引记录
//   - exFAT: 索引无记录的残留临时 object 物理删除
package prune

import (
	"fmt"

	"github.com/ljw/filesync/internal/cas"
	"github.com/ljw/filesync/internal/index"
)

// Stats 是 prune 统计。
type Stats struct {
	Scanned int64
	Deleted int64
	Failed  int64
}

// Pruner 执行孤立对象清理。
type Pruner struct {
	cas   cas.CAS
	index index.Index
}

// New 创建 Pruner。
func New(c cas.CAS, idx index.Index) *Pruner {
	return &Pruner{cas: c, index: idx}
}

// Run 执行清理。dryRun=true 时只统计不删除。
//
// 幂等性说明：物理删除（cas.DeleteObject）与索引删除（index.DeleteObject）分属
// 文件系统与 bbolt 两个系统，无法跨事务原子化。若物理删除成功后、索引删除前崩溃，
// 重启后 prune 会再次发现该 key（索引 RefCount=0）但物理文件已不存在：
// cas.DeleteObject 对不存在文件返回 nil（容错），随后 index.DeleteObject 清理记录。
// 因此 prune 是幂等的——崩溃后重跑即可自愈，不会留下永久不一致状态。
func (p *Pruner) Run(dryRun bool) (Stats, error) {
	var stats Stats

	// 1. 索引中 RefCount=0 的 object
	orphanKeys := []string{}
	p.index.IterateObjects(func(key string, r index.ObjectRecord) bool {
		stats.Scanned++
		if r.RefCount == 0 {
			orphanKeys = append(orphanKeys, key)
		}
		return true
	})

	for _, key := range orphanKeys {
		if dryRun {
			stats.Deleted++
			continue
		}
		if err := p.cas.DeleteObject(key); err != nil {
			stats.Failed++
			continue
		}
		p.index.DeleteObject(key)
		stats.Deleted++
	}

	// 2. 物理存在但索引无记录的残留临时 object（exFAT 崩溃残留）
	physicalKeys, err := p.cas.ListObjects()
	if err != nil {
		return stats, fmt.Errorf("list objects: %w", err)
	}
	for _, key := range physicalKeys {
		_, ok, _ := p.index.GetObject(key)
		if !ok {
			if dryRun {
				stats.Deleted++
				continue
			}
			if err := p.cas.DeleteObject(key); err != nil {
				stats.Failed++
				continue
			}
			stats.Deleted++
		}
	}

	return stats, nil
}
