# filesync

将多个源目录**增量同步**到移动 SSD 的内容去重备份工具。基于 CAS(内容寻址存储)实现去重,支持断点续传、并发拷贝、文件系统自适应(NTFS 硬链接 / exFAT 复制回退)与原子索引。

面向 Windows amd64。

---

## 核心特性

- **内容去重(CAS)**:相同内容的文件在目标盘只存一份。NTFS 用硬链接指向同一对象(零额外空间),exFAT/FAT32 自动回退为普通复制(空间与 1:1 持平)。
- **增量同步**:只拷贝新增或改动的文件。已同步且未变化的文件自动跳过。
- **断点续传**:中途 `Ctrl+C` 或断电不会损坏数据。已完成的文件下次运行直接跳过,重跑即续传。
- **原子索引**:用 bbolt 单文件 ACID 数据库记录文件与对象元数据,单个文件的同步结果在一个事务内原子落库。
- **并发拷贝**:worker 池并行拷贝,同一对象的任务路由到固定 worker,避免竞态。
- **拷贝后校验**:可选哈希校验。小文件(≤ 1 MiB)始终强制校验,大文件可配置。
- **冲突保护**:目标已存在同名但内容不同的文件时,旧文件自动移入 `.filesync/conflict/`,不会被静默覆盖丢失。
- **空间预检**:同步前预估所需空间,不足时提前报错而非中途失败。
- **重复文件去重**:独立 `dedup` 命令扫描任意文件夹,将内容重复的文件用硬链接去重(NTFS),exFAT 仅报告。

---

## 安装 / 编译

需要 Go 1.23+。

```bash
cd D:\Project\Go_project\file_sync
go build -o filesync.exe .
```

生成的 `filesync.exe` 可放到任意位置使用。

---

## 配置

工具读取一个 YAML 配置文件(默认查找当前目录的 `config.yaml`,也可用 `--config` 指定)。

```yaml
# filesync 配置示例
target_root: "E:\\PSSD_sync"      # 目标盘根(绝对路径,你的移动 SSD)
workers: 8                          # 并发拷贝数(默认 8)
verify: true                        # 拷贝后校验哈希(默认 true)

sources:                            # 要备份的源目录,可配置多个
  - src: "D:\\Project\\Go_project"  # 源目录(绝对路径)
    dest: "Project/Go_project"      # 在目标盘内的相对路径(正斜杠)
  - src: "D:\\Documents\\Work"
    dest: "Documents/Work"

exclude:                            # 排除规则(** 递归 glob,相对源根,大小写不敏感)
  - "**/.git/**"
  - "**/node_modules/**"
  - "**/*.tmp"
  - "**/*.log"
  - "**/Thumbs.db"
```

### 配置字段说明

| 字段 | 必填 | 说明 |
|------|------|------|
| `target_root` | 是 | 目标盘根目录,必须是绝对路径 |
| `workers` | 否 | 并发拷贝数,默认 `8`,最小 `1` |
| `verify` | 否 | 拷贝后是否校验哈希,默认 `true`。设为 `false` 仅跳过大文件校验(小文件仍强制) |
| `sources` | 是 | 源映射列表,至少一项 |
| `sources[].src` | 是 | 源目录绝对路径 |
| `sources[].dest` | 是 | 目标盘内相对路径(用正斜杠),不能含 `..` |
| `exclude` | 否 | 排除模式列表,`**` 递归 glob,匹配相对源根的路径,大小写不敏感 |

> **路径写法**:Windows 路径在 YAML 中反斜杠要转义(`D:\\Project`),或改用正斜杠。`dest` 建议统一用正斜杠。

---

## 命令

```
filesync sync     [--config FILE] [--workers N] [--dry-run] [--verify | --no-verify]
filesync status   [--config FILE]
filesync verify   [--config FILE]
filesync reindex  [--config FILE]
filesync prune    [--config FILE] [--dry-run]
filesync dedup    <目录> [--dry-run] [--readonly] [--exclude PATTERN]...
```

### `sync` — 同步
增量同步所有源目录到目标盘。可随时 `Ctrl+C` 中断,已完成的文件下次跳过。

```bash
filesync.exe sync --dry-run    # 只扫描不拷贝,预览将同步哪些文件(强烈建议首次使用)
filesync.exe sync              # 正式同步
filesync.exe sync --workers 4  # 临时用 4 个并发
```

### `status` — 查看状态
显示已同步文件数、对象数、引用总大小与去重节省的空间。

```bash
filesync.exe status
```

### `verify` — 全盘校验
重新计算目标盘上所有文件的哈希,与索引比对,检测损坏或缺失。

```bash
filesync.exe verify
```

### `reindex` — 重建索引
当索引文件丢失或损坏时,从目标盘现有文件重建索引(NTFS 用 `os.SameFile` 判定硬链接,exFAT 重算哈希)。

```bash
filesync.exe reindex
```

### `prune` — 清理孤立对象
删除无人引用(RefCount=0)的对象与残留临时对象,回收空间。

```bash
filesync.exe prune --dry-run   # 先预览将删除什么
filesync.exe prune             # 执行清理
```

### `dedup` — 重复文件去重
扫描任意文件夹,对内容完全相同的文件用硬链接去重。NTFS 下重复文件替换为指向同一物理副本的硬链接(所有原始路径仍可正常访问,磁盘只存一份);exFAT/FAT32 不支持硬链接时仅报告重复组不做修改。该命令独立于同步配置,无需 `config.yaml`。

```bash
filesync.exe dedup D:\Photos --dry-run            # 先预览重复文件
filesync.exe dedup D:\Photos                       # 执行去重（保持可写，适合工作目录）
filesync.exe dedup D:\Photos --readonly            # 去重后设只读（归档场景，防误编辑污染硬链接副本）
filesync.exe dedup D:\Photos --exclude "**/*.tmp"  # 排除临时文件
```

> **硬链接特性**:去重后各路径名地位平等,删除任意一个不影响其他副本(只要还有引用,内容不释放)。但**修改任意一个会同步影响所有副本**——因为它们指向同一物理内容。
>
> **两种场景**:
> - `--readonly`(归档):去重后将整组文件设为只读 0444,防止误编辑污染所有硬链接副本。适合照片、文档归档等只读数据。需修改时用户自行 `chmod` 可写。
> - 默认(工作目录):去重后保持可写。适合明确知晓"改一个全变"风险、需要原地修改的场景。
>
> **安全说明**:已是硬链接的文件(同一物理文件)自动跳过;被替换文件的 mtime 保留。建议先用 `--dry-run` 预览。

### 全局选项

| 选项 | 适用命令 | 说明 |
|------|----------|------|
| `--config FILE` | `sync`/`status`/`verify`/`reindex`/`prune` | 配置文件路径(默认 `config.yaml`) |
| `--workers N` | `sync` | 临时覆盖并发数(`0` = 用配置默认) |
| `--dry-run` | `sync` / `prune` / `dedup` | 只扫描/预览,不实际改动文件 |
| `--verify` | `sync` | 强制开启拷贝后校验 |
| `--no-verify` | `sync` | 禁用大文件校验(小文件 ≤ 1 MiB 仍强制校验)。与 `--verify` 同时给出时以 `--no-verify` 为准 |
| `--exclude PATTERN` | `dedup` | 排除模式(`**` 递归 glob,可重复) |
| `--readonly` | `dedup` | 去重后将文件设为只读(归档场景,防误编辑污染硬链接副本) |

---

## 典型使用流程

1. 编译 `filesync.exe`,准备好 `config.yaml`。
2. 首次预览,确认同步范围正确:
   ```bash
   filesync.exe sync --dry-run
   ```
3. 正式备份:
   ```bash
   filesync.exe sync
   ```
4. 之后每次插上 SSD,直接再跑 `filesync.exe sync` 即可——只会拷贝新增或改动的文件。
5. 定期 `filesync.exe verify` 检查数据完好;`filesync.exe prune` 回收旧版本占用的空间。

---

## 目标盘目录结构

工具在目标盘根创建一个 `.filesync/` 元数据目录:

```
E:\PSSD_sync\
├── Project\Go_project\        # 按 dest 还原的可直接浏览的文件树
├── Documents\Work\
└── .filesync\
    ├── index.db               # bbolt 索引(文件记录 + 对象记录)
    ├── objects\               # CAS 对象存储,按哈希两层分桶
    │   └── <2c>\<4c>\<hex>    # 物理文件名为纯 hex(无 h3: 前缀,Windows 冒号非法)
    └── conflict\              # 内容冲突时旧文件的备份
        └── <时间戳>\...
```

> **objectKey 与物理文件名**:索引中 objectKey 格式为 `h3:<hex>`(`h3:` 前缀标识 xxh3 算法),但物理文件名用去掉前缀的**纯 hex**——因为 Windows 文件名不允许冒号。两者通过 `ObjectPath`/`ListObjects` 自动转换。
>
> 目标盘上 `dest` 路径下是**正常可浏览的文件**(NTFS 下它们是指向 `objects/` 的硬链接,看起来和普通文件无异)。`.filesync/` 是工具内部数据,请勿手动修改。

---

## 工作原理简述

1. **扫描**:遍历每个源目录,应用 `exclude` 规则,收集文件元信息。
2. **哈希**:用 xxh3 计算内容哈希,得到 `objectKey`(格式 `h3:<hex>`)。
3. **查索引生成任务**:与索引中已记录的 `size/mtime/objectKey` 比对,未变化则跳过,变化或新增则生成同步任务。
4. **并发拷贝**:worker 池执行。`EnsureObject`(对象不存在则从源拷入 CAS)→ `PlaceFile`(NTFS 硬链接 / exFAT 复制)→ 可选校验 → 原子更新索引。
5. **报告**:输出拷贝数、跳过数、去重节省、失败列表。

---

## 测试

```bash
go test ./...            # 全部测试
go test -race ./...      # 含竞态检测
go test -cover ./...     # 覆盖率
```

当前共 **95 个测试**,覆盖 16 个包,`go vet ./...` 无问题。

### CI/CD

每次 push / PR 自动在 **Ubuntu + Windows** 上跑 `go vet` + `go test -race` + `go build`。

打 `v*` 标签自动触发 Release，交叉编译 4 个平台并附带 SHA256 checksums：

| 平台 | 产物 |
|------|------|
| Windows amd64 | `filesync-windows-amd64.exe` |
| Linux amd64 | `filesync-linux-amd64` |
| macOS amd64 | `filesync-darwin-amd64` |
| macOS arm64 | `filesync-darwin-arm64` |

---

## 更新记录(CHANGELOG)

### 2026-06-24 新增 dedup 重复文件去重命令

新增独立子命令 `filesync dedup <目录>`,扫描任意文件夹对内容重复的文件用硬链接去重:

- **NTFS**:重复文件替换为指向同一物理副本的硬链接,所有原始路径仍可访问,磁盘只存一份
- **exFAT/FAT32**:不支持硬链接时仅报告重复组不做修改
- 算法:按 size 分组 → 同 size 组并发算 xxh3 哈希 → 同 (size,hash) 归为重复组 → 每组保留代表文件,其余用 `os.Link` 替换
- 安全:已是硬链接的文件跳过;被替换文件 mtime 保留;`--dry-run` 仅报告;替换前校验代表文件 size 未变
- 独立于同步配置,无需 `config.yaml`;支持 `--exclude` 排除模式
- `--readonly` flag 区分两种场景:归档场景去重后设只读 0444(防误编辑污染硬链接副本),工作目录场景默认保持可写
- 新增 `internal/dedup` 包(11 个测试),`cas.DetectMode` 导出函数

### 2026-06-24 健壮性补齐

在对照设计文档逐项核对后,补齐了 10 项缺失或简化的功能:

| 变更 | 说明 |
|------|------|
| `\\?\` 长路径应用 | cas/copier/syncer/verify/reindex 所有文件操作处用 `paths.Long()` 包裹路径,绕过 Windows 260 字符限制 |
| SIGINT 中断恢复 | copier 新增 `RunWithContext(ctx)`,ctx 取消后停止分发新任务、等待进行中 worker 完成;syncer 透传 `SyncWithContext`;main 用 `signal.NotifyContext` 捕获 SIGINT/SIGTERM |
| 目标盘空间预估 | 新增 `internal/disk` 包;syncer 拷贝前按 NTFS(新增 object 总量)/exFAT(单 worker 最大文件峰值)预估,不足时提前报错 |
| 文件锁定处理 | copier 识别 `ERROR_SHARING_VIOLATION`/`ERROR_LOCK_VIOLATION`,锁定文件计入 `Locked` 而非 Failed,单独列出 |
| 小文件强制校验 | verify 改为:文件 ≤1 MiB 强制校验,大文件按 verify 开关(设计 §7 + §15.1) |
| CLI `--verify` flag | 新增 `--verify`/`--no-verify` 互斥 flag,覆盖配置默认;新增 `config.ApplyVerifyOverride` 方法 |
| 进度回调 | copier 新增 `ProgressFunc` 回调,每文件完成时通知;syncer 透传;main 输出实时进度 |
| 示例 config.yaml | 创建根目录 `config.yaml` 示例 |
| errgroup 评估 | 评估后保留 WaitGroup——errgroup 的"首个错误即取消"语义与设计 §7"错误隔离"冲突 |
| 新增测试 12 个 | 覆盖中断、进度回调、强制校验、锁定识别、空间预估、verify flag、锁定报告 |

### 2026-06-23 初始实现

按设计文档完成 15 个 Task 的 TDD 实现,涵盖全部核心组件(config/paths/scanner/hasher/index/cas/copier/report/syncer/prune/verify/reindex/main/e2e)。

实现中发现并修正的关键问题:

| 问题 | 修正 |
|------|------|
| Windows 文件名冒号非法 | objectKey 含 `h3:` 作为物理文件名失败(被解释为备用数据流)。物理文件名改用纯 hex,`h3:` 前缀仅用于索引 key |
| 跨卷硬链接失败 | Windows 硬链接要求同卷,测试中 dest 改为与 object 同 root |
| 空文件哈希常量错误 | 原用占位 `0000...`,实测 xxh3 空内容哈希为固定值 `99aa06d3...`,修正常量 |
| RefCount 同 key 双变 | `ApplySyncResult` 当旧=新 objectKey 时会重复递增,修正为仅当旧≠新时递增 |
| bbolt 文件锁阻塞 | reindex 重新打开 index.db 前需显式关闭前一句柄,否则被 flock 阻塞 |
| mtime 容差漏检 | 同秒内修改源文件因 2s 容差未触发重算,测试改为显式设置超 2s 的 mtime |
| exFAT 临时 object 生命周期 | 原每任务后删除导致同内容多文件重复读源(去重失效),改为按 key 在队列中最后任务索引控制删除 |

### 设计文档偏差记录

以下实现决策与设计文档原文存在偏差(均已记录,设计文档为权威参考,实现为实际行为):

1. **objectKey 物理文件名**:设计 §5 写 `objects/<bucket1>/<bucket2>/<objectKey>`(含 `h3:` 前缀)。实现中物理文件名为纯 hex(去掉 `h3:`),因 Windows 文件名不允许冒号。索引 key 仍为 `h3:<hex>`。
2. **空文件哈希**:设计 §10 写"哈希固定为常量"未给具体值。实现中实测 xxh3 空内容 128 位哈希为 `99aa06d3014798d86001c324468d497f`,objectKey 为 `h3:99aa06d3014798d86001c324468d497f`。
3. **RefCount=0 异步删除**:设计 §4.g/h/§10 描述"事务内仅置 0 标记 orphaned,事务提交后经异步清理通道再校验删除"。实现中简化为 prune 命令显式清理(RefCount=0 的 object 在 prune 时物理删除),未实现独立异步清理通道。功能等价,但需手动运行 prune。
4. **errgroup**:设计 §12 选型理由提到 errgroup。实现中保留 WaitGroup,因 errgroup 的取消语义与设计 §7 的错误隔离需求冲突。
