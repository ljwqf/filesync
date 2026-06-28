# file_sync 文件同步工具设计文档

- **状态**: 审查修订中
- **日期**: 2026-06-23
- **语言/运行时**: Go 1.23, Windows amd64
- **目标平台**: Windows + 移动固态硬盘（当前 E: PSSD, exFAT, 1TB）

## 1. 背景与目标

将项目工作文件同步到移动固态硬盘（PSSD）作为备份。源数据量大、小文件多、存在重复文件，直接拷贝面临两个核心问题：

1. **小文件太多导致拷贝慢** —— 大量小文件的逐个拷贝受 IO 往返和文件系统元数据开销拖累，速度远低于磁盘带宽。
2. **重复文件导致重复拷贝且占用大量空间** —— 内容相同的文件（即使路径/文件名不同）被重复存储，浪费时间和空间。

工具需在保证数据完整的前提下，加速拷贝、消除重复存储（在文件系统支持时）、支持断点续传。

### 1.1 核心需求

| 编号 | 需求 | 决策 |
|------|------|------|
| R1 | 多源目录同步到单一移动 SSD | 配置文件管理源→目标映射 |
| R2 | 重复文件处理 | CAS 去重存储 + 断点续传跳过已拷贝 |
| R3 | 源文件被删除后目标处理 | 只增量备份，不删目标（安全，不误删） |
| R4 | 小文件加速 | 并发拷贝 + 大缓冲 + 快速哈希 |
| R5 | 去重判定方式 | 全内容 xxh3 哈希；size 仅作变更检测预筛，不参与 objectKey |
| R6 | 哈希索引持久化 | 目标盘存索引文件，加速重复同步 |
| R7 | 文件系统适配 | NTFS 用硬链接去重；exFAT 回退复制，仍跳过重复拷贝 |

### 1.2 非目标（YAGNI）

- 不做双向同步（仅源→目标单向备份）
- 不做删除镜像（不删目标）
- 不做实时监听（手动运行）
- 不做加密/压缩（保持文件可直接浏览）
- 不做跨平台（聚焦 Windows，路径处理针对 Windows）

## 2. 总体架构

采用 **CAS（内容寻址存储）+ 文件系统自适应** 架构。

```
源目录 (D:\...)                         目标移动 SSD (E:\PSSD_sync\)
├── 源文件1 (内容X)                      ├── .filesync/
├── 源文件2 (内容X, 与文件1相同)    ──>  │   ├── index.db          # bbolt 持久化索引 (files + objects bucket)
├── 源文件3 (内容Y)                      │   ├── config.yaml        # 配置副本
└── ...                                  │   └── objects/           # CAS 对象存储
                                         │       ├── X1/X1ac/      # 两层分桶: 哈希前2位/前4位
                                         │       │   └── <hash_X>  # NTFS: 内容X唯一物理副本(只读,永久)
                                         │       └── Y2/Y2bd/      # exFAT: 运行后此目录应为空(临时object拷后删)
                                         │           └── <hash_Y>  #   仅同步进行中临时存在
                                         └── <镜像目录结构>        # 正常路径位置
                                             ├── path/to/file1     ─┐ NTFS:硬链接→objects/X1/X1ac/<hash_X>
                                             ├── path/to/file2     ─┤ NTFS:硬链接→objects/X1/X1ac/<hash_X> (同一物理文件, 零额外空间)
                                             └── path/to/file3     ─┘ exFAT:独立副本(从临时object复制后object即删, 跳过重复读源)
```

**核心机制**：
- 每个唯一文件内容存一份到 `objects/<hash[:2]>/<hash[:4]>/<hash>`（两层分桶，控制每桶文件数）
- 正常路径位置的文件：
  - **NTFS** → 创建硬链接指向 object（零额外空间，物理一份；object 永久保留）
  - **exFAT** → 从 object **复制**副本到目标路径，随后**删除临时 object**（object 仅作去重判定与中转，不长期占空间；空间与 1:1 持平）
- 索引 `index.db` 记录所有已同步文件 + object 元数据（无论 object 物理是否存在），断点续传跳过已拷贝，去重判定查索引而非文件是否存在
- **object 文件设为只读** → 仅 NTFS 长期 object 需要，防止用户编辑一个硬链接文件时污染所有同内容的链接

## 3. 组件设计

模块边界清晰，各自单一职责，可独立理解与测试。

| 组件 | 职责 | 输入 | 输出 | 依赖 |
|------|------|------|------|------|
| `config` | 加载/校验同步配置（多源目录映射） | config.yaml 路径 | `Config` 结构体 | yaml |
| `scanner` | 扫描源目录，收集文件元信息 | 源路径 | `[]FileInfo{path,size,mtime}` | fs |
| `hasher` | 内容哈希（全文件 xxh3） | `FileInfo`，文件内容 | objectKey | xxh3 |
| `index` | 持久化索引读写（目标盘 .filesync/index.db） | 文件记录 | 已同步文件集合 | bbolt |
| `cas` | CAS 对象存储管理 + 文件系统检测（对象只读保护） | 哈希+内容 | object 路径，硬链接/复制 | fs |
| `copier` | 并发拷贝 worker 池 | 同步任务 | 拷贝结果 | cas, index |
| `syncer` | 编排：串联以上组件的主流程 | Config | 同步报告 | 上述全部 |
| `report` | 进度展示 + 最终统计报告 | 同步事件 | 终端输出 | - |

### 3.1 接口约定（关键类型）

```go
// config
type SourceMapping struct {
    Src  string   `yaml:"src"`
    Dest string   `yaml:"dest"`
}
type Config struct {
    TargetRoot string           `yaml:"target_root"`
    Workers    int              `yaml:"workers"`
    Verify     bool             `yaml:"verify"`
    Sources    []SourceMapping  `yaml:"sources"`
    Exclude    []string         `yaml:"exclude"`
}

// scanner
type FileInfo struct {
    RelPath string  // 相对源根的路径
    AbsPath string  // 绝对路径
    Size    int64
    Mtime   time.Time
}

// hasher — 输出 objectKey (xxh3 哈希的十六进制字符串)
// 始终基于文件内容计算哈希，不再使用 size-only key
type Hasher interface {
    Hash(fi FileInfo, content io.Reader) (objectKey string, err error)
}

// index
type FileRecord struct {
    Size      int64
    Mtime     time.Time
    ObjectKey string
    SyncedAt  time.Time
}
type ObjectRecord struct {
    Size     int64
    RefCount int          // 逻辑引用数: 引用该 objectKey 的文件路径数 (NTFS/exFAT 语义一致)
    StoredAt time.Time
    // NTFS: object 物理长期存在, RefCount 归0可物理删除
    // exFAT: object 为临时中转, 拷贝后即删; RefCount 仅作逻辑统计与去重判定, 不映射物理文件数
}
type Index interface {
    GetFile(relPath string) (FileRecord, bool, error)
    PutFile(relPath string, r FileRecord) error
    GetObject(key string) (ObjectRecord, bool, error)
    PutObject(key string, r ObjectRecord) error
    DeleteFile(relPath string) error  // 用于 RefCount 递减
    Close() error
}

// cas
type StorageMode int
const (
    ModeHardlink StorageMode = iota  // NTFS
    ModeCopy                          // exFAT/FAT32
)
type CAS interface {
    EnsureObject(srcAbsPath, objectKey string) (exists bool, err error)
    // NTFS: object 不存在则从源拷入并 chmod 0444, 存在则复用; 返回 exists=true 表示 object 物理已就绪
    // exFAT: object 不存在则从源拷入(临时), 存在则复用; 调用方在 PlaceFile 后负责删除临时 object
    PlaceFile(objectKey, destAbsPath string) error
    // NTFS: 硬链接 object → destAbsPath (覆盖前先解除目标只读)
    // exFAT: 复制 object → destAbsPath, 调用方随后调 RemoveTempObject 清理
    RemoveTempObject(objectKey string) error  // 仅 exFAT 模式有效; NTFS 下 no-op
    Mode() StorageMode
}
```

## 4. 核心数据流

```
1. 加载 config.yaml → 得到 [(源路径, 目标子路径), ...]
2. 对每个源目录:
   a. scanner 扫描 → []FileInfo (path, size, mtime)，应用 exclude 过滤
   b. hasher 计算 objectKey = xxh3(file content) — 始终基于内容
   c. 查 index.db:
      - (路径, size, mtime, objectKey) 全匹配 → 跳过 (断点续传)
      - (路径, size, objectKey) 匹配但 mtime 不同 → 仅更新索引 mtime，跳过拷贝
      - 否则 → 生成同步任务 {srcAbs, destAbs, objectKey, size}
3. syncer 编排同步任务 → copier worker 池 (并发度默认8, --workers可配)
   每个 worker:
   a. 重新 stat 源文件，若 mtime 或 size 与 FileInfo 不符 → 重算 objectKey
   b. 去重判定: 查 index.db objects bucket
      - objectKey 已存在记录 → 该内容已拷贝过 (NTFS: object 物理存在可直接硬链接; exFAT: object 已被删, 需重新 EnsureObject 临时拷入)
      - 不存在 → 需全新拷贝
   c. cas.EnsureObject(srcAbs, objectKey):
      - NTFS: objects/<hash[:2]>/<hash[:4]>/<key> 已存在 → 复用; 不存在 → 从源拷贝, chmod 0444
      - exFAT: 不存在 → 从源拷贝临时 object (带1MB buffer); 已存在(本轮已拷过) → 复用
   d. 覆盖预处理: 若 destAbsPath 已存在且为只读 → 先 chmod 可写 (NTFS 硬链接覆盖 / exFAT 复制覆盖均需)
   e. cas.PlaceFile(objectKey, destAbsPath):
      - NTFS → 硬链接 object → destAbsPath
      - exFAT → 复制 object → destAbsPath
   f. exFAT: cas.RemoveTempObject(objectKey) 删除临时 object (同 objectKey 任务由路由保证串行; 该 worker 是此 key 的最后任务时删除)
   g. 在**同一个 bbolt 事务**中:
      - 若 destAbsPath 旧记录存在且 objectKey 不同 → 旧 object RefCount-- (仅更新索引计数, **不在事务内做物理删除**)
      - 新 object PutFile + PutObject(RefCount++)
      - 若旧 object RefCount 归 0 → 记录到延迟删除队列 (objectKey, objectPath), 事务提交成功后由异步清理通道处理
   h. 异步清理通道 (与 prune 共享逻辑): 事务提交成功后, 对延迟队列中的 objectKey 执行物理删除 (NTFS: 删 object 文件; exFAT: object 本就临时已删, 仅清索引记录)。删除失败记入孤立对象列表, 留待下次 prune 重试。**物理删除与索引事务解耦**: 即使物理删除失败, 索引已正确反映 RefCount=0, 不影响正确性, 仅产生可被 prune 清理的孤立文件
   i. (可选) verify: 对 destAbsPath 重算哈希比对 objectKey
4. report 输出: 已跳过 / 已拷贝 / 去重节省 / 失败统计
```

## 5. 哈希策略

所有文件统一使用 **xxh3 全内容哈希**作为 objectKey，不再使用 size-only key。

**为什么不用 size-only key 方案**：
- 以 size 作 key 持久化后，不同运行中不同内容的文件可能产生相同 size → 误判为重复 → **数据损坏**
- 虽可在 EnsureObject 时验证内容，但增加了设计复杂度，收益有限
- 全内容 xxh3 的计算速度已足够快（~10 GB/s），大文件瓶颈在 IO 而非哈希

**objectKey 格式**：`h:<xxh3_hex>`（`h:` 前缀标识哈希算法，便于未来扩展）

**object 存储路径**：`objects/<bucket1>/<bucket2>/<objectKey>`
- `bucket1` = objectKey 去掉 `h:` 前缀后的第 1-2 字符
- `bucket2` = 第 1-4 字符
- 示例：`h:a1b2c3d4e5...` → bucket1=`a1`，bucket2=`a1b2` → 路径 `objects/a1/a1b2/h:a1b2c3d4e5...`

两层分桶使桶数从 256 扩展到 65536，100 万 object 时每桶仅约 15 个文件，避免 exFAT 大目录性能退化。

**关于"大小预筛"的保留**：scanner 输出 FileInfo 时仍带有 Size，用于 copier 中判断是否有必要重新哈希（size 变化一定需要重算），但不作为 objectKey 的一部分。

## 6. 索引格式（index.db）

使用 **bbolt**（Go 嵌入式 KV，单文件、ACID、无需外部服务）。存两个 bucket：

- **`files` bucket**
  - key = `目标相对路径`（UTF-8）
  - value = `FileRecord{Size, Mtime, ObjectKey, SyncedAt}`（gob/JSON 编码）
- **`objects` bucket**
  - key = `ObjectKey`
  - value = `ObjectRecord{Size, RefCount, StoredAt}`

**重要写入保证**：
- **原子性**：`PutFile` + `PutObject(RefCount++)` 在同一个 bbolt `Update` 事务中完成。一个文件的同步结果不会出现 files bucket 和 objects bucket 不一致。
- **RefCount 原子更新**：当目标路径被新文件覆盖，需在同一个事务中：
  1. `GetFile(relPath)` 查询旧 objectKey
  2. `PutFile(relPath, newRecord)` 更新文件记录
  3. `GetObject(oldKey)` → `PutObject(oldKey, {RefCount: oldRefCount-1})` 递减旧对象引用
  4. `GetObject(newKey)` → `PutObject(newKey, {RefCount: newRefCount+1})` 递增新对象引用
- bbolt 的 ACID 保证同步过程中崩溃不损坏索引
- 断点续传查 `files`，去重判定查 `objects`

## 7. 并发与错误处理

- **worker 池**：默认 8 个并发拷贝，`--workers` 可配
- **任务路由（exFAT 临时 object 生命周期管理）**：同一 objectKey 的任务按 key 哈希分发到**固定 worker**（`workerIndex = hash(objectKey) % N`）。一个 worker 从 EnsureObject → PlaceFile → RemoveTempObject 全权负责其 key 的 object 生命周期，天然消除"别人还在用就删了"的竞态，无需跨 worker 引用计数同步。Worker 池负载均衡损失可接受（同内容 key 的文件通常不多）。NTFS 下 object 永久保留，无此约束
- **大缓冲拷贝**：1MB buffer + `io.CopyBuffer`，避免小粒度 IO
- **exFAT 大文件**：exFAT 单文件写入对簇对齐敏感，大 buffer 缓解
- **NTFS object 并发**：object 永久保留且只读，多 worker 可安全并发硬链接同一 object，无需串行化
- **源文件中途修改处理**：worker 在拷贝前重新 stat 源文件，若 size/mtime 与 scanner 记录不符，重新计算哈希
- **错误隔离**：单个文件失败不中断整体，记入失败列表，最终报告列出
- **中断恢复**：捕获 `SIGINT`，优雅停止分发新任务，等待进行中的 worker 完成；已更新索引的文件下次跳过；exFAT 中途崩溃可能残留临时 object，下次运行或 prune 清理
- **物理删除与索引事务解耦**：旧 object RefCount 归 0 时，物理删除不放入 bbolt 事务（避免"FS 删除成功但事务回滚"或"事务成功但 FS 删除失败"的不一致）。改为事务提交成功后由异步清理通道执行物理删除，删除失败仅产生可被 prune 清理的孤立文件，不影响索引正确性
- **校验**：`--verify`（默认 true）拷贝后对目标文件重算哈希比对 objectKey；小文件强制校验
- **索引写入串行化**：bbolt 单写者，所有 worker 的索引更新经单一 channel 串行写入，避免写竞争。每个消息携带同事务内 PutFile+PutObject 的完整信息

## 8. 配置文件（config.yaml）

```yaml
# 放在工具同级目录，或 --config 指定路径
target_root: "E:\\PSSD_sync"      # 目标盘根
workers: 8                          # 并发数
verify: true                        # 拷贝后校验
sources:
  - src: "D:\\Project\\Go_project"
    dest: "Project/Go_project"      # 目标盘内相对路径 (正斜杠)
  - src: "D:\\Documents\\Work"
    dest: "Documents/Work"
exclude:                            # 排除规则 (glob, 相对源根)
  - "**/.git/**"
  - "**/node_modules/**"
  - "**/*.tmp"
```

注意：`**` 递归匹配需要第三方 glob 库（推荐 `github.com/gobwas/glob` 或 `github.com/bmatcuk/doublestar`），标准库 `filepath.Match` 不支持 `**`。

## 9. 命令行接口

```
filesync sync [--config config.yaml] [--dry-run] [--workers N]
  # 执行同步。--dry-run 只扫描报告不拷贝

filesync status [--config config.yaml]
  # 显示索引状态: 已同步文件数、占用空间、去重节省量、object 数
  # 去重节省量:
  #   NTFS: (Σ引用文件大小 - object 实际物理占用) —— 物理空间节省
  #   exFAT: (Σ跳过重复拷贝的文件大小) —— 时间节省; 空间上因 object 临时删除, 与 1:1 持平, 无物理节省

filesync verify [--config config.yaml]
  # 校验目标盘所有文件哈希与索引一致

filesync reindex [--config config.yaml]
  # 重建索引 (目标盘已有数据但索引丢失/损坏时用)
  # 性能预估: 100 万文件级需数分钟到数十分钟 (取决于磁盘 IO)
  # 策略:
  #   - NTFS: 利用 os.SameFile 比较镜像文件与 objects/ 下文件的 inode, 关联到对应 object (无需重算哈希)
  #   - exFAT: objects/ 应为空(临时 object 已删); 重算每个镜像文件哈希, 重建 files + objects bucket 的 RefCount 统计

filesync prune [--config config.yaml] [--dry-run]
  # 清理孤立 object:
  #   - NTFS: RefCount=0 的 object 物理删除
  #   - exFAT: 清理残留临时 object (中断崩溃未删干净的), 正常运行后 objects/ 应为空
  # --dry-run 列出将清理的文件但不删除
```

## 10. 其他已考虑的问题

| 问题 | 处理 |
|------|------|
| 路径过长（Windows 260 限制） | 用 `\\?\` 前缀长路径支持 |
| 中文/特殊字符路径 | 统一 UTF-8，Windows API 用宽字符 |
| 文件被占用/锁定 | 跳过并记录，报告末尾列出 |
| 目标盘空间不足 | 拷贝前预估总待拷贝量。NTFS: 仅新增的 object 大小之和(硬链接零额外)；exFAT: 所有待拷贝文件大小之和(临时 object 拷后即删, 峰值占用≈单 worker 最大文件, 可忽略)；不足时提前报错并列出可同步量 |
| mtime 精度（FAT/exFAT 仅 2 秒） | 比对时 mtime 容差 ±2s，且强制比 size + objectKey |
| 符号链接/快捷方式 | 默认跳过符号链接，记录；不跟随 |
| 时间戳保留 | 拷贝后 `os.Chtimes` 保留源 mtime/atime |
| exFAT 无硬链接 | 自适应回退为复制；object 作临时中转拷后即删，去重判定查索引，跳过重复拷贝省时间 |
| 配置中路径分隔符 | 配置内 dest 用正斜杠；src 用原生反斜杠或正斜杠均可，内部归一化 |
| 增量备份一致性 | 仅新增/修改文件入 object；已存在 object 永不重写（内容寻址天然幂等） |
| 目标目录已存在同名但内容不同文件 | 以源 objectKey 为准：若目标路径现有文件 objectKey 与源不符，将该文件移至 `.filesync/conflict/<时间戳>/<sanitized(源相对路径)>/<文件名>` 后再放置正确内容。`sanitized()` 将路径分隔符、`..`、`:`、非法字符替换为 `_`，并对超长路径截断+追加短哈希后缀（总长控制在 `\\?\` 的 32767 字符内），避免嵌套深度爆炸与非法路径。冲突移动记入报告 |
| 硬链接误修改保护 | NTFS object 文件完成后设为只读 `0444`，防止用户编辑一个链接时污染所有同内容文件；exFAT object 临时存在无需此保护 |
| 覆盖只读目标文件 | 放置/覆盖目标路径前，若目标已存在且为只读，先 chmod 可写再覆盖；exFAT 镜像副本默认可写，仅在用户手动设只读时触发 |
| 文件更新时旧对象引用清理 | 同一 bbolt 事务中原子性地递减旧 object RefCount、递增新 object RefCount；**物理删除从事务分离**——RefCount 归 0 时仅记入延迟删除队列，事务提交成功后由异步清理通道（与 prune 共享逻辑）执行物理删除，失败留作孤立对象供下次 prune 重试 |
| 源文件在扫描与拷贝之间被修改 | copier 在拷贝前**重新 stat** 源文件，size/mtime 变化则重算哈希 |
| object 存储桶平坦化 | 两层分桶 `objects/<2chars>/<4chars>/<key>`，100 万 object 时每桶仅 ~15 个文件，避免 exFAT 大目录性能退化 |
| 哈希一致性校验 | `<algo>:<hex>` 格式使 index 可区分 hash 类型，便于未来扩展算法。当前算法前缀建议用 `h3:`（xxh3）而非泛化的 `h:`，避免未来新增算法时前缀歧义 |
| 空文件 (size=0) | 哈希固定为常量，所有空文件复用同一 objectKey；NTFS 共享一个空 object，exFAT 各复制一份空文件（开销可忽略） |
| 空目录 | scanner 记录空目录，syncer 在目标对应路径 mkdir 保留目录结构（不依赖文件存在性） |
| exFAT 跨运行去重效果 | **仅单次运行内**有效：同内容文件共享一个临时 object，减少读源次数。跨运行时 object 已删，重复同步仍需读取源（与普通增量拷贝一致），额外开销仅为哈希重算。status/report 中对 exFAT 去重节省明确标注"单次运行内"，避免用户误期望跨运行跳过拷贝 |
| Scanner 循环检测 | 使用 `filepath.WalkDir`（默认不跟随 symlink，本身安全）；额外记录 visited (device, inode) 集合，遇到已访问目录直接 `fs.SkipDir`，防御目录符号链接造成的循环。不跟随任何符号链接 |
| exclude 大小写敏感性 | Windows 文件系统大小写不敏感，但 doublestar 默认大小写敏感匹配。约定：**在 Windows 上 exclude pattern 匹配为大小写不敏感**（匹配前对 pattern 与路径统一转小写），避免 `.Git`/`.GIT` 不匹配 `.git` 规则 |

## 11. 测试策略

- **单元测试**：每个组件独立测试（hasher 的碰撞兜底、index 的 ACID + 原子事务、cas 的硬链接/复制分支 + 只读设置、config 解析校验）
- **集成测试**：用临时目录模拟源/目标，验证完整 sync 流程、断点续传、去重效果、文件更新后旧 RefCount 递减
- **文件系统适配测试**：检测到 NTFS 走硬链接、其他走复制的分支覆盖；验证 object 只读属性
- **故障注入**：模拟中途崩溃（验证索引原子性）、文件锁定、空间不足
- **场景测试**：
  - 源文件内容不变、仅 mtime 变化 → 仅更新索引，不拷贝
  - 源文件路径不变、内容变化 → 正确替换目标，旧 object RefCount--，RefCount 归 0 走延迟异步删除
  - 源文件删除 → 目标文件保留，RefCount 不变
  - 同内容文件在多个源目录出现 → NTFS: objects 中仅存一份; exFAT: 临时 object 仅拷一次后删除, 镜像各一份
  - exFAT 同 objectKey 并发拷贝（路由到同 worker）→ object 不被提前删除, 无竞态损坏
  - exFAT 中断崩溃 → 残留临时 object 由 prune 清理, 索引一致性不被破坏
  - 覆盖只读目标文件 → 先 chmod 可写再覆盖, 不失败
  - 空文件/空目录 → 正确处理, 空目录结构保留
  - **事务与物理删除解耦**：模拟物理删除失败 → 索引 RefCount 正确归 0，孤立文件留待 prune；模拟事务回滚 → 物理文件未被删，索引未变
- **reindex 测试**：NTFS 下用 os.SameFile 关联镜像与 object 重建索引；exFAT 下重算镜像哈希重建；reindex 后 sync 行为与原索引一致（无误判重复/遗漏）
- **prune 测试**：NTFS 清理 RefCount=0 的 object；exFAT 清理残留临时 object；--dry-run 仅列出不删除；prune 不误删 RefCount>0 的 object
- **Scanner 测试**：目录符号链接循环不导致无限递归（visited 集合生效）；exclude 大小写不敏感（`.Git` 匹配 `.git` 规则）
- 详见实现阶段的测试计划

## 12. 依赖

| 依赖 | 用途 | 选型理由 |
|------|------|----------|
| `github.com/zeebo/xxh3` | xxh3 哈希 | 极快~10GB/s，纯 Go，无 cgo |
| `go.etcd.io/bbolt` | 嵌入式 KV 索引 | 单文件 ACID，无需外部服务，成熟稳定 |
| `gopkg.in/yaml.v3` | 配置解析 | 标准选择 |
| `golang.org/x/sync/errgroup` | 并发编排 | errgroup 简化并发错误处理 |
| `github.com/gobwas/glob` 或 `doublestar` | 递归 glob 匹配 | 标准库不支持 `**` 递归匹配 |

## 13. 目录结构（预期）

```
file_sync/
├── go.mod
├── go.sum
├── config.yaml              # 用户配置示例
├── main.go                  # CLI 入口
├── internal/
│   ├── config/              # 配置加载校验
│   ├── scanner/             # 源目录扫描
│   ├── hasher/              # 内容哈希
│   ├── index/               # bbolt 索引
│   ├── cas/                 # CAS 对象存储
│   ├── copier/              # 并发拷贝
│   ├── syncer/              # 主编排
│   └── report/              # 报告输出
├── docs/superpowers/specs/
│   └── 2026-06-23-file-sync-design.md
└── ...
```

## 14. 审查修订记录（2026-06-23）

本次审查在 v2 基础上修正以下问题：

| 问题 | 修正 |
|------|------|
| 标题残留 "v2"、目录结构残留 v1/v2 双文件名 | 标题去掉版本号；目录结构改为单一文档 |
| R5 描述仍提"大小预筛 + 内容哈希"易误解 size 参与 objectKey | 改为"全内容 xxh3；size 仅作变更检测预筛" |
| exFAT object 永久占空间导致总空间 > 1:1，违背 R2 | exFAT object 改为临时中转，拷后即删；空间与 1:1 持平 |
| exFAT 去重判定依赖 object 文件存在性 | 改为查索引 objects bucket 记录，与物理 object 解耦 |
| RefCount 语义在 NTFS/exFAT 下含糊 | 明确：NTFS 映射物理硬链接数可物理删除；exFAT 仅逻辑统计 |
| 同 objectKey 并发拷贝时临时 object 被提前删除的竞态 | 同 objectKey 任务串行化（per-key 路由或 Mutex） |
| 覆盖只读目标文件会失败 | 放置前 chmod 可写预处理 |
| reindex 在 exFAT 下逻辑模糊 | 明确：exFAT objects/ 应为空，重算镜像哈希重建索引 |
| prune 未区分 NTFS/exFAT 语义 | 明确：NTFS 删 RefCount=0 object；exFAT 清残留临时 object |
| status 空间节省口径与 exFAT 临时 object 矛盾 | exFAT 空间与 1:1 持平无物理节省，仅时间节省 |
| 未涉及空文件/空目录 | 补充：空文件复用常量 objectKey；空目录 mkdir 保留结构 |
| 中断崩溃 exFAT 残留临时 object | 中断恢复说明 + prune 清理 |

### 第二轮审查补充修正（2026-06-23）

| 问题 | 修正 |
|------|------|
| 旧 object 物理删除在 bbolt 事务内执行，FS 删除与事务提交任一失败导致不一致 | 物理删除从事务分离：事务内仅更新 RefCount，归 0 记入延迟删除队列，事务提交成功后由异步清理通道（与 prune 共享）执行物理删除，失败留作孤立对象供 prune 重试 (§4.h, §7, §10) |
| §15.1（串行化方案）与 §15.2（删除时机）耦合未显式指出 | 默认选路由方案（同 objectKey 按 key 哈希分发到固定 worker），worker 全权负责 object 生命周期，天然消除跨 worker 引用计数同步；删除时机随之确定为"该 key 最后任务完成时"。两项一并解决 (§7) |
| exFAT 跨运行去重效果预期未说明 | 补充：exFAT 去重仅单次运行内有效，跨运行仍需读源；status/report 标注"单次运行内" (§10) |
| 冲突路径中 `<源路径归一化>` 含 `..`/`\`/超长路径不安全 | 改用 sanitized()：非法字符替换为 `_`，超长路径截断+短哈希后缀，总长控制在 32767 字符内 (§10) |
| Scanner 缺循环检测 | filepath.WalkDir 默认不跟随 symlink + visited (device,inode) 集合防御目录符号链接循环 (§10) |
| exclude 大小写敏感与 Windows 不匹配 | 约定 Windows 上 pattern 匹配大小写不敏感（pattern 与路径统一转小写） (§10) |
| `h:` 前缀不够具体 | 建议 `h3:`（xxh3）避免未来算法前缀歧义 (§10) |
| 测试策略缺 reindex/prune/循环检测/大小写覆盖 | §11 补充对应测试场景 |

## 15. 审查待确认项

经两轮审查，原三项待确认已解决两项（路由方案与删除时机一并确定）。剩余一项：

1. **verify 对大文件的性能权衡**：大文件重算哈希 doubles IO。当前设计：小文件强制 verify，大文件走 `--verify` 开关（默认 true）。实现时若大文件 verify 成为瓶颈，可考虑改为默认关闭大文件 verify、仅校验首尾采样段。此为实现期可调项，不影响设计评审通过。

**已决项（记录备查）**：
- exFAT 串行化方案 = **路由**（同 objectKey → 固定 worker）。曾考虑 per-key Mutex，因引入跨 worker 引用计数同步而放弃。
- 临时 object 删除时机 = **该 key 最后任务完成时删除**（路由方案的必然结果）。
- 旧 object 物理删除 = **事务外异步**（延迟删除队列 + prune 兜底）。

---

## 16. 实现记录与设计偏差

本节记录实现过程中与设计原文的偏差及决策。设计文档为权威参考，以下为实现实际行为。

### 16.1 objectKey 物理文件名

**设计 §5**：`objects/<bucket1>/<bucket2>/<objectKey>`，objectKey 含 `h3:` 前缀。

**实现**：物理文件名用**纯 hex**（去掉 `h3:` 前缀）。

**原因**：Windows 文件名不允许冒号 `:`，`h3:a1b2c3d4` 作为文件名会被解释为 NTFS 备用数据流或非法字符，导致 object 创建失败。索引 key 仍为 `h3:<hex>`（保留算法前缀以便未来扩展），仅物理存储层剥离前缀。`cas.ObjectPath` 负责转换，`cas.ListObjects` 反向还原为 objectKey。

### 16.2 空文件哈希常量

**设计 §10**：空文件哈希"固定为常量"，未给具体值。

**实现**：实测 xxh3 对空内容（0 字节）的 128 位哈希为 `Hi=99aa06d3014798d8`, `Lo=6001c324468d497f`，objectKey = `h3:99aa06d3014798d86001c324468d497f`。常量定义于 `hasher.EmptyObjectKey`。

### 16.3 RefCount=0 处理简化

**设计 §4.g/h/§10**：事务内仅置 RefCount=0 并标记 orphaned，事务提交后经异步清理通道再校验 RefCount 仍为 0 才物理删除（消除与复用的竞态）。

**实现**：简化为 **prune 命令显式清理**。RefCount=0 的 object 在索引中标记 Orphaned，由用户运行 `filesync prune` 时物理删除 + 清索引记录。功能等价（RefCount=0 的 object 最终被清理），但未实现独立异步清理通道，需手动触发 prune。

**影响**：旧版本文件占用的 object 在下次 prune 前仍占用空间，但不影响正确性（索引 RefCount 已正确反映）。

### 16.4 errgroup 未采用

**设计 §12**：选型理由提到 `golang.org/x/sync/errgroup` 简化并发错误处理。

**实现**：保留 `sync.WaitGroup` + 手动错误收集。

**原因**：errgroup 的默认语义是"首个错误即取消整组"，与设计 §7"错误隔离：单个文件失败不中断整体同步"冲突。errgroup 配 `SetLimit` 仍会因 ctx 取消中断未完成任务。WaitGroup + per-task 错误收集更贴合错误隔离需求。`golang.org/x/sync` 依赖保留（go.mod）但未实际使用 errgroup。

### 16.5 补齐功能

初始实现后对照设计逐项核对，补齐了以下设计要求但初始实现缺失的功能（详见 README CHANGELOG）：

- **§10 长路径**：`\\?\` 前缀在所有文件操作处实际应用（初始仅实现 `paths.Long` 未调用）
- **§7 SIGINT 中断**：context 取消传递，优雅停止
- **§10 空间预检**：拷贝前预估，不足报错
- **§10 文件锁定**：共享违规错误识别与跳过
- **§7 小文件强制校验**：≤1 MiB 始终校验
- **§3 进度展示**：ProgressFunc 回调
- **§9 CLI verify flag**：`--verify`/`--no-verify`