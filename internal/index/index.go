// Package index 提供 bbolt 持久化索引：files 与 objects 两个 bucket。
// 一个文件的同步结果（PutFile + PutObject RefCount 变更）在单事务内原子完成。
package index

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	filesBucket   = []byte("files")
	objectsBucket = []byte("objects")
)

// FileRecord 记录一个已同步文件。
type FileRecord struct {
	Size      int64
	Mtime     time.Time
	ObjectKey string
	SyncedAt  time.Time
}

// ObjectRecord 记录一个 CAS object 的元数据。
type ObjectRecord struct {
	Size     int64
	RefCount int
	StoredAt time.Time
	// Orphaned 标记 RefCount=0 待物理删除（仅用于状态查询/prune 列举）。
	Orphaned bool
}

// SyncOp 描述一次同步对索引的写操作，保证 PutFile + PutObject 原子。
type SyncOp struct {
	RelPath      string
	NewRecord    FileRecord
	OldObjectKey string // 旧文件记录的 objectKey；为空表示全新文件
}

// Index 是持久化索引接口。
type Index interface {
	GetFile(relPath string) (FileRecord, bool, error)
	PutFile(relPath string, r FileRecord) error
	DeleteFile(relPath string) error
	GetObject(key string) (ObjectRecord, bool, error)
	PutObject(key string, r ObjectRecord) error
	DeleteObject(key string) error
	// ApplySyncResult 在单事务内原子应用一次同步结果。
	ApplySyncResult(op SyncOp) error
	// ApplySyncResults 在单事务内原子应用一批同步结果。
	ApplySyncResults(ops []SyncOp) error
	// ApplyReindexBatch 在单事务内原子应用一批 reindex 写操作。
	// 确保崩溃后索引不会出现 file 记录已写但 object RefCount 未更新的不一致状态。
	ApplyReindexBatch(fileRecs map[string]FileRecord, objectRecs map[string]ObjectRecord) error
	IterateFiles(fn func(relPath string, r FileRecord) bool) error
	IterateObjects(fn func(key string, r ObjectRecord) bool) error
	Close() error
}

// boltIndex 是 bbolt 实现。
type boltIndex struct {
	db *bolt.DB
}

// Open 打开（或创建）bbolt 索引文件。
func Open(path string) (Index, error) {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("open bbolt %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{filesBucket, objectsBucket} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return fmt.Errorf("create bucket %s: %w", b, err)
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &boltIndex{db: db}, nil
}

func (b *boltIndex) GetFile(relPath string) (FileRecord, bool, error) {
	var rec FileRecord
	found := false
	err := b.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(filesBucket).Get([]byte(relPath))
		if v == nil {
			return nil
		}
		found = true
		return json.Unmarshal(v, &rec)
	})
	return rec, found, err
}

func (b *boltIndex) PutFile(relPath string, r FileRecord) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return tx.Bucket(filesBucket).Put([]byte(relPath), data)
	})
}

func (b *boltIndex) DeleteFile(relPath string) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(filesBucket).Delete([]byte(relPath))
	})
}

func (b *boltIndex) GetObject(key string) (ObjectRecord, bool, error) {
	var rec ObjectRecord
	found := false
	err := b.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(objectsBucket).Get([]byte(key))
		if v == nil {
			return nil
		}
		found = true
		return json.Unmarshal(v, &rec)
	})
	return rec, found, err
}

func (b *boltIndex) PutObject(key string, r ObjectRecord) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return tx.Bucket(objectsBucket).Put([]byte(key), data)
	})
}

func (b *boltIndex) DeleteObject(key string) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(objectsBucket).Delete([]byte(key))
	})
}

// ApplySyncResult 在单个 bbolt Update 事务内原子完成：
//  1. 读取旧文件记录的 objectKey（op.OldObjectKey 已由调用方提供，校验用）
//  2. 写入新文件记录
//  3. 旧 objectKey（若与新不同）RefCount--，归 0 标记 orphaned
//  4. 新 objectKey RefCount++（若已存在则累加）
func (b *boltIndex) ApplySyncResult(op SyncOp) error {
	return b.ApplySyncResults([]SyncOp{op})
}

// ApplySyncResults 在单个 bbolt Update 事务内原子完成一批同步结果。
func (b *boltIndex) ApplySyncResults(ops []SyncOp) error {
	if len(ops) == 0 {
		return nil
	}
	return b.db.Update(func(tx *bolt.Tx) error {
		fb := tx.Bucket(filesBucket)
		ob := tx.Bucket(objectsBucket)

		for _, op := range ops {
			// 写新文件记录
			op.NewRecord.SyncedAt = time.Now()
			data, err := json.Marshal(op.NewRecord)
			if err != nil {
				return err
			}
			if err := fb.Put([]byte(op.RelPath), data); err != nil {
				return err
			}

			newKey := op.NewRecord.ObjectKey

			// 旧 object RefCount 递减（仅当旧 key 存在且与新 key 不同）
			if op.OldObjectKey != "" && op.OldObjectKey != newKey {
				if err := decRefCount(ob, op.OldObjectKey); err != nil {
					return err
				}
			}

			// 新 object RefCount 递增（仅当旧 key 不同，避免同 key 重复计数）。
			// 旧==新 表示文件内容未变，RefCount 不变。
			if op.OldObjectKey != newKey {
				if err := incRefCount(ob, newKey, op.NewRecord.Size); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func incRefCount(ob *bolt.Bucket, key string, size int64) error {
	var rec ObjectRecord
	if v := ob.Get([]byte(key)); v != nil {
		if err := json.Unmarshal(v, &rec); err != nil {
			return err
		}
	}
	rec.RefCount++
	if rec.Size == 0 {
		rec.Size = size
	}
	if rec.StoredAt.IsZero() {
		rec.StoredAt = time.Now()
	}
	rec.Orphaned = false
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return ob.Put([]byte(key), data)
}

func decRefCount(ob *bolt.Bucket, key string) error {
	var rec ObjectRecord
	if v := ob.Get([]byte(key)); v != nil {
		if err := json.Unmarshal(v, &rec); err != nil {
			return err
		}
	}
	if rec.RefCount > 0 {
		rec.RefCount--
	}
	if rec.RefCount == 0 {
		rec.Orphaned = true
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return ob.Put([]byte(key), data)
}

// ApplyReindexBatch 在单个 bbolt Update 事务内原子写入一批 file 与 object 记录。
// 用于 reindex 场景，确保崩溃后不会出现 file 已写但 object RefCount 未更新的不一致。
// fileRecs: relPath -> FileRecord；objectRecs: objectKey -> ObjectRecord（已含正确 RefCount）。
func (b *boltIndex) ApplyReindexBatch(fileRecs map[string]FileRecord, objectRecs map[string]ObjectRecord) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		fb := tx.Bucket(filesBucket)
		ob := tx.Bucket(objectsBucket)

		// 写入所有 file 记录
		for relPath, rec := range fileRecs {
			data, err := json.Marshal(rec)
			if err != nil {
				return fmt.Errorf("marshal file %s: %w", relPath, err)
			}
			if err := fb.Put([]byte(relPath), data); err != nil {
				return err
			}
		}

		// 写入所有 object 记录
		for key, rec := range objectRecs {
			data, err := json.Marshal(rec)
			if err != nil {
				return fmt.Errorf("marshal object %s: %w", key, err)
			}
			if err := ob.Put([]byte(key), data); err != nil {
				return err
			}
		}
		return nil
	})
}

func (b *boltIndex) IterateFiles(fn func(relPath string, r FileRecord) bool) error {
	return b.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(filesBucket).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec FileRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			if !fn(string(k), rec) {
				return nil
			}
		}
		return nil
	})
}

func (b *boltIndex) IterateObjects(fn func(key string, r ObjectRecord) bool) error {
	return b.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(objectsBucket).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec ObjectRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			if !fn(string(k), rec) {
				return nil
			}
		}
		return nil
	})
}

func (b *boltIndex) Close() error {
	return b.db.Close()
}
