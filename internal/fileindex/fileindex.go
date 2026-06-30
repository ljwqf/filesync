// Package fileindex 提供轻量级文件状态持久化索引，用于增量检测。
// 与 index 包不同，fileindex 不涉及 CAS object/RefCount，纯粹追踪文件状态。
// 被 dedup（增量去重）和 bisync（双向同步）共用。
package fileindex

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var statesBucket = []byte("filestates")

// FileState 记录单个文件的元信息快照。
type FileState struct {
	Size   int64
	Mtime  time.Time
	Hash   string // xxh3 objectKey；空表示未计算哈希（仅需增量检测时可省略）
}

// FileIndex 是持久化文件状态索引接口。
type FileIndex interface {
	Get(path string) (FileState, bool, error)
	Put(path string, s FileState) error
	Delete(path string) error
	Iterate(fn func(path string, s FileState) bool) error
	ApplyBatch(puts map[string]FileState, deletes []string) error
	Close() error
}

type boltFileIndex struct {
	db *bolt.DB
}

// Open 打开或创建文件状态索引文件。
func Open(path string) (FileIndex, error) {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("open fileindex %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(statesBucket)
		return err
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &boltFileIndex{db: db}, nil
}

func (b *boltFileIndex) Get(path string) (FileState, bool, error) {
	var s FileState
	found := false
	err := b.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(statesBucket).Get([]byte(path))
		if v == nil {
			return nil
		}
		found = true
		return json.Unmarshal(v, &s)
	})
	return s, found, err
}

func (b *boltFileIndex) Put(path string, s FileState) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(s)
		if err != nil {
			return err
		}
		return tx.Bucket(statesBucket).Put([]byte(path), data)
	})
}

func (b *boltFileIndex) Delete(path string) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(statesBucket).Delete([]byte(path))
	})
}

func (b *boltFileIndex) Iterate(fn func(path string, s FileState) bool) error {
	return b.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(statesBucket).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var s FileState
			if err := json.Unmarshal(v, &s); err != nil {
				return err
			}
			if !fn(string(k), s) {
				return nil
			}
		}
		return nil
	})
}

// ApplyBatch 在单个事务内原子写入一批状态更新并删除一批条目。
func (b *boltFileIndex) ApplyBatch(puts map[string]FileState, deletes []string) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(statesBucket)
		for path, s := range puts {
			data, err := json.Marshal(s)
			if err != nil {
				return fmt.Errorf("marshal %s: %w", path, err)
			}
			if err := bucket.Put([]byte(path), data); err != nil {
				return err
			}
		}
		for _, path := range deletes {
			if err := bucket.Delete([]byte(path)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (b *boltFileIndex) Close() error {
	return b.db.Close()
}

// IsUnchanged 判断文件当前元信息是否与索引记录一致。
// size 必须精确匹配；mtime 允许 2 秒容差（与 paths.MtimeClose 一致）。
func IsUnchanged(indexed FileState, currentSize int64, currentMtime time.Time) bool {
	return indexed.Size == currentSize && mtimeClose(indexed.Mtime, currentMtime)
}

// mtimeClose 判断两个时间是否在 2 秒容差内。
func mtimeClose(a, b time.Time) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d <= 2*time.Second
}
