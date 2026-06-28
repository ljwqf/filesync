// Package copier 实现并发拷贝 worker 池。
// 同一 objectKey 的任务按 key 哈希路由到固定 worker，由该 worker 全权负责
// EnsureObject → PlaceFile → RemoveTempObject 的 object 生命周期，消除跨 worker 竞态。
package copier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ljw/filesync/internal/cas"
	"github.com/ljw/filesync/internal/hasher"
	"github.com/ljw/filesync/internal/index"
	"github.com/ljw/filesync/internal/paths"
)

// Task 是一个待执行的同步任务。
type Task struct {
	SrcAbs    string
	DestAbs   string
	RelPath   string // 目标盘内相对路径（含 dest 前缀），用作 index files key
	ObjectKey string
	Size      int64
	Mtime     time.Time
}

// verifyThreshold 是小文件强制校验的阈值（1 MiB）。
// 低于此值的文件无论 verify 开关均强制校验（设计 §7）。
const verifyThreshold int64 = 1 << 20

// shouldVerify 判断是否需要校验：小文件强制校验，大文件按开关。
func shouldVerify(verify bool, size int64) bool {
	return verify || size <= verifyThreshold
}

// Result 是单次 Run 的汇总报告。
type Result struct {
	Copied     int64
	Skipped    int64
	Failed     int64
	Bytes      int64
	DedupSaved int64 // 跳过重复拷贝节省的字节
	Errors     []FileError
	Locked     []string // 被占用/锁定而跳过的文件
}

// FileError 记录单个失败文件。
type FileError struct {
	RelPath string
	Err     error
}

// ProgressEvent 描述单个文件处理完成的事件。
type ProgressEvent struct {
	RelPath string
	Copied  bool   // true=成功拷贝, false=失败/跳过/锁定
	Bytes   int64
}

// ProgressFunc 是进度回调签名。
type ProgressFunc func(e ProgressEvent)

// Copier 是并发拷贝器。
type Copier struct {
	cas        cas.CAS
	index      index.Index
	hasher     hasher.Hasher
	workers    int
	verify     *bool
	targetRoot string // 目标盘根，用于 .filesync/conflict 冲突文件存放
	progress   ProgressFunc
}

// New 创建 Copier。
func New(c cas.CAS, idx index.Index, h hasher.Hasher, workers int) *Copier {
	if workers < 1 {
		workers = 1
	}
	return &Copier{cas: c, index: idx, hasher: h, workers: workers}
}

// SetVerify 设置是否拷贝后校验。
func (c *Copier) SetVerify(v *bool) { c.verify = v }

// SetTargetRoot 设置目标盘根（冲突文件存放用）。
func (c *Copier) SetTargetRoot(root string) { c.targetRoot = root }

// SetProgress 设置进度回调，每个文件处理完成时调用。
func (c *Copier) SetProgress(fn ProgressFunc) { c.progress = fn }

// Run 执行所有任务，返回汇总结果。
func (c *Copier) Run(tasks []Task) Result {
	return c.RunWithContext(context.Background(), tasks)
}

// RunWithContext 执行所有任务，支持 context 取消（SIGINT 优雅停止）。
// ctx 取消后不再分发新任务，等待进行中 worker 完成当前任务后返回。
func (c *Copier) RunWithContext(ctx context.Context, tasks []Task) Result {
	// 按 objectKey 路由：同 key 任务进同一队列
	queues := make([][]Task, c.workers)
	for _, t := range tasks {
		idx := c.routeKey(t.ObjectKey)
		queues[idx] = append(queues[idx], t)
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		result Result
	)
	v := true
	if c.verify != nil {
		v = *c.verify
	}

	for i := 0; i < c.workers; i++ {
		if len(queues[i]) == 0 {
			continue
		}
		wg.Add(1)
		go func(q []Task) {
			defer wg.Done()
			// 预计算每个 objectKey 在本队列中的最后任务索引，
			// exFAT 下该 key 的最后任务完成后删除临时 object。
			isHardlink := c.cas.Mode() == cas.ModeHardlink
			lastIdx := map[string]int{}
			for j, t := range q {
				lastIdx[t.ObjectKey] = j
			}
			for j, t := range q {
				// ctx 取消：停止分发新任务，进行中的任务已完成
				if ctx.Err() != nil {
					func() {
						mu.Lock()
						defer mu.Unlock()
						result.Skipped += int64(len(q) - j)
					}()
					return
				}
				saved, er := c.processTask(t, v)
				func() {
					mu.Lock()
					defer mu.Unlock()
					if er != nil {
						if isLockedError(er) {
							// 文件被占用/锁定：跳过并单独记录，不计入 failed
							result.Locked = append(result.Locked, t.RelPath)
						} else {
							result.Failed++
							result.Errors = append(result.Errors, FileError{RelPath: t.RelPath, Err: er})
						}
					} else {
						result.Copied++
						result.Bytes += t.Size
						result.DedupSaved += saved
					}
				}()
				// 进度回调（在 mu 外调用，避免回调内阻塞持有锁）
				if c.progress != nil {
					c.progress(ProgressEvent{RelPath: t.RelPath, Copied: er == nil, Bytes: t.Size})
				}
				// exFAT: 该 key 的最后任务完成 → 删除临时 object
				if !isHardlink && lastIdx[t.ObjectKey] == j {
					if err := c.cas.RemoveTempObject(t.ObjectKey); err != nil {
						func() {
							mu.Lock()
							defer mu.Unlock()
							result.Errors = append(result.Errors, FileError{RelPath: t.RelPath, Err: fmt.Errorf("remove temp object: %w", err)})
						}()
					}
				}
			}
		}(queues[i])
	}
	wg.Wait()
	return result
}

// routeKey 将 objectKey 路由到固定 worker。
func (c *Copier) routeKey(key string) int {
	if c.workers == 1 {
		return 0
	}
	var h uint32
	for _, ch := range key {
		h = h*31 + uint32(ch)
	}
	return int(h % uint32(c.workers))
}

// processTask 处理单个任务：重 stat → 去重 → EnsureObject → 冲突检测 → PlaceFile → 索引。
// 返回去重节省字节数（object 已存在时为文件 size，否则 0）与错误。
func (c *Copier) processTask(t Task, verify bool) (int64, error) {
	var dedupSaved int64
	// 1. 重新 stat 源文件，size/mtime 变化则重算哈希
	objectKey := t.ObjectKey
	srcLong := paths.Long(t.SrcAbs)
	fi, err := os.Stat(srcLong)
	if err != nil {
		return 0, fmt.Errorf("stat src: %w", err)
	}
	if fi.Size() != t.Size || !mtimeClose(fi.ModTime(), t.Mtime) {
		newKey, err := c.hasher.HashFile(srcLong)
		if err != nil {
			return 0, fmt.Errorf("rehash: %w", err)
		}
		objectKey = newKey
		t.Size = fi.Size()
		t.Mtime = fi.ModTime()
	}

	// 2. 去重判定：object 物理是否已存在（本轮已拷过或历史遗留）
	//    exFAT 下同 key 首个任务拷入，后续任务复用（EnsureObject 发现存在不重拷），
	//    统计去重节省字节。
	if _, err := os.Stat(paths.Long(c.cas.ObjectPath(objectKey))); err == nil {
		dedupSaved = t.Size
	}

	// 3. EnsureObject（不存在则从源拷入）
	if _, err := c.cas.EnsureObject(t.SrcAbs, objectKey); err != nil {
		return 0, fmt.Errorf("ensure object: %w", err)
	}

	// 3.5 冲突检测：若目标已存在且内容（哈希）与源 objectKey 不符，
	//     移至 .filesync/conflict/<时间戳>/<sanitized(源相对路径)>/<文件名> 再放置。
	if err := c.handleConflict(t, objectKey); err != nil {
		return 0, fmt.Errorf("conflict: %w", err)
	}

	// 4. PlaceFile
	if c.cas.Mode() == cas.ModeHardlink {
		if err := c.cas.PlaceFileHardlink(objectKey, t.DestAbs); err != nil {
			return 0, fmt.Errorf("place hardlink: %w", err)
		}
	} else {
		if err := c.cas.PlaceFileCopy(objectKey, t.DestAbs); err != nil {
			return 0, fmt.Errorf("place copy: %w", err)
		}
		// exFAT 临时 object 的删除不在本任务内执行：
		// 由 Run 的 worker 层按 key 剩余计数决定（该 key 最后任务完成后删除），
		// 保证同内容多文件共享同一临时 object，避免重复读源。
	}

	// 5. 保留源 mtime
	destLong := paths.Long(t.DestAbs)
	os.Chtimes(destLong, time.Now(), t.Mtime)

	// 6. verify（设计 §7：小文件强制校验；大文件按 verify 开关）
	//    小文件校验开销极小，强制执行确保完整性；大文件重算哈希 doubles IO，
	//    仅在 verify 开关开启时校验（设计 §15.1 待确认项）。
	if shouldVerify(verify, t.Size) {
		vk, err := c.hasher.HashFile(destLong)
		if err != nil {
			return 0, fmt.Errorf("verify hash: %w", err)
		}
		if vk != objectKey {
			return 0, fmt.Errorf("verify mismatch: dest %s != object %s", vk, objectKey)
		}
	}

	// 7. 索引：查询旧记录，原子更新
	oldKey := ""
	if old, ok, _ := c.index.GetFile(t.RelPath); ok {
		oldKey = old.ObjectKey
	}
	op := index.SyncOp{
		RelPath:      t.RelPath,
		NewRecord:    index.FileRecord{Size: t.Size, Mtime: t.Mtime, ObjectKey: objectKey},
		OldObjectKey: oldKey,
	}
	if err := c.index.ApplySyncResult(op); err != nil {
		return 0, fmt.Errorf("index update: %w", err)
	}
	return dedupSaved, nil
}

// handleConflict 检测目标路径是否已存在且内容与源 objectKey 不符。
// 若不符（内容冲突），将目标现有文件移至 .filesync/conflict/<ts>/<sanitized>/<name>。
// 目标不存在或内容一致时不动。
func (c *Copier) handleConflict(t Task, objectKey string) error {
	destLong := paths.Long(t.DestAbs)
	destInfo, err := os.Stat(destLong)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 目标不存在，无冲突
		}
		return fmt.Errorf("stat dest: %w", err)
	}
	if !destInfo.Mode().IsRegular() {
		return nil // 非普通文件，交由 PlaceFile 覆盖
	}
	// 计算目标现有文件哈希，与源 objectKey 比对
	existingKey, err := c.hasher.HashFile(destLong)
	if err != nil {
		return fmt.Errorf("hash existing dest: %w", err)
	}
	if existingKey == objectKey {
		return nil // 内容一致，无冲突
	}
	// 内容冲突：移至 conflict 目录
	conflictDir := filepath.Join(c.targetRoot, ".filesync", "conflict",
		nowTimestamp(), paths.Sanitized(t.RelPath))
	if err := os.MkdirAll(paths.Long(conflictDir), 0755); err != nil {
		return fmt.Errorf("mkdir conflict dir: %w", err)
	}
	conflictPath := filepath.Join(conflictDir, filepath.Base(t.DestAbs))
	// 冲突文件可能是只读，先解除
	os.Chmod(destLong, destInfo.Mode()|0200)
	if err := os.Rename(destLong, paths.Long(conflictPath)); err != nil {
		return fmt.Errorf("move conflict file to %s: %w", conflictPath, err)
	}
	return nil
}

// nowTimestamp 返回用于冲突目录名的时间戳（秒级，文件系统友好）。
func nowTimestamp() string {
	return time.Now().Format("20060102-150405")
}

// mtimeClose 比较 mtime，容差 2 秒（FAT/exFAT 精度）。
func mtimeClose(a, b time.Time) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d <= 2*time.Second
}

// Windows 共享/锁定违规错误码。
const (
	errSharingViolation = 32 // ERROR_SHARING_VIOLATION
	errLockViolation    = 33 // ERROR_LOCK_VIOLATION
)

// isLockedError 判断错误是否为文件被占用/锁定（设计 §10：跳过并记录）。
func isLockedError(err error) bool {
	var sysErr syscall.Errno
	if errors.As(err, &sysErr) {
		return sysErr == errSharingViolation || sysErr == errLockViolation
	}
	return false
}
