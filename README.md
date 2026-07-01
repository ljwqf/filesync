# filesync

将多个源目录**增量同步**到移动 SSD 的内容去重备份工具。基于 CAS(内容寻址存储)实现去重,支持断点续传、并发拷贝、文件系统自适应(NTFS 硬链接 / exFAT 复制回退)与原子索引。

跨平台支持: Windows、Linux、macOS。

---

## 核心特性

- **内容去重(CAS)**:相同内容的文件在目标盘只存一份。NTFS 用硬链接指向同一对象(零额外空间),exFAT/FAT32 自动回退为普通复制(空间与 1:1 持平)。
- **增量同步**:只拷贝新增或改动的文件。默认用 `size + mtime` 快速跳过未变化文件；需要严格内容扫描时可关闭。
- **断点续传**:中途 `Ctrl+C` 或断电不会损坏数据。已完成的文件下次运行直接跳过,重跑即续传。
- **原子索引**:用 bbolt 单文件 ACID 数据库记录文件与对象元数据,同步结果批量事务落库。
- **并发处理**:候选文件并发哈希,worker 池并行拷贝,同一对象的任务路由到固定 worker,避免竞态。
- **拷贝后校验**:可选哈希校验。小文件(≤ 1 MiB)默认强制校验,可在海量小文件场景关闭;大文件可配置。
- **冲突保护**:目标已存在同名但内容不同的文件时,旧文件自动移入 `.filesync/conflict/`,不会被静默覆盖丢失。
- **空间预检**:同步前预估所需空间,不足时提前报错而非中途失败。
- **增量去重**:独立 `dedup` 命令支持增量模式,首次全量扫描后自动缓存文件状态,后续运行只重算变化文件的哈希,大幅加速重复扫描。
- **重复文件去重**:扫描任意文件夹,对内容重复的文件用硬链接去重(NTFS),exFAT 仅报告。
- **双向同步**:新增 `bisync` 命令,支持两个相似文件夹的双向同步,自动检测两端变化并合并,支持 4 种冲突策略(keep-both/left-wins/right-wins/newer-wins)。

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
verify_small_files: true            # 小文件强制校验(默认 true;海量小文件可设 false)
metadata_fast_skip: true            # size+mtime 未变则跳过 hash(默认 true;严格内容扫描可设 false)

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
| `verify` | 否 | 拷贝后是否校验哈希,默认 `true`。设为 `false` 仅跳过大文件校验(小文件默认仍强制) |
| `verify_small_files` | 否 | 小文件(≤ 1 MiB)是否强制校验,默认 `true`;海量小文件追求速度时可设 `false` |
| `metadata_fast_skip` | 否 | 已同步文件 `size + mtime` 未变时是否直接跳过 hash,默认 `true`;需要严格内容扫描时设 `false` |
| `sources` | 是 | 源映射列表,至少一项 |
| `sources[].src` | 是 | 源目录绝对路径 |
| `sources[].dest` | 是 | 目标盘内相对路径(用正斜杠),不能含 `..` |
| `exclude` | 否 | 排除模式列表,`**` 递归 glob,匹配相对源根的路径,大小写不敏感 |

> **路径写法**:Windows 路径在 YAML 中反斜杠要转义(`D:\\Project`),或改用正斜杠。`dest` 建议统一用正斜杠。

---

## 命令

```
filesync sync     [--config FILE] [--workers N] [--dry-run] [--verify | --no-verify] [--no-small-verify] [--strict-hash-scan]
filesync status   [--config FILE]
filesync verify   [--config FILE]
filesync reindex  [--config FILE]
filesync prune    [--config FILE] [--dry-run]
filesync dedup    <目录> [--index PATH] [--dry-run] [--readonly] [--exclude PATTERN]...
filesync bisync   --left DIR --right DIR [--dry-run] [--workers N] [--conflict STRATEGY] [--exclude PATTERN]...
```

### `sync` — 同步
增量同步所有源目录到目标盘。可随时 `Ctrl+C` 中断,已完成的文件下次跳过。

```bash
filesync.exe sync --dry-run    # 只扫描不拷贝,预览将同步哪些文件(强烈建议首次使用)
filesync.exe sync              # 正式同步
filesync.exe sync --workers 4  # 临时用 4 个并发
filesync.exe sync --no-small-verify   # 海量小文件时关闭小文件强制校验
filesync.exe sync --strict-hash-scan  # 禁用 size+mtime 快跳过,逐候选文件计算内容哈希
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
filesync.exe dedup D:\Photos --index my-index.db   # 指定增量索引路径
```

> **增量模式**:默认在扫描目录下创建 `.dedup-index.db` 索引文件。首次运行全量扫描,后续运行仅重算变化文件的哈希,大幅加速。可用 `--index` 指定自定义路径。

> **硬链接特性**:去重后各路径名地位平等,删除任意一个不影响其他副本(只要还有引用,内容不释放)。但**修改任意一个会同步影响所有副本**——因为它们指向同一物理内容。
>
> **两种场景**:
> - `--readonly`(归档):去重后将整组文件设为只读 0444,防止误编辑污染所有硬链接副本。适合照片、文档归档等只读数据。需修改时用户自行 `chmod` 可写。
> - 默认(工作目录):去重后保持可写。适合明确知晓"改一个全变"风险、需要原地修改的场景。
>
> **安全说明**:已是硬链接的文件(同一物理文件)自动跳过;被替换文件的 mtime 保留。建议先用 `--dry-run` 预览。

### `bisync` — 双向同步
扫描两个目录,检测两端的变化(新增/修改/删除),并将变化同步到另一端。两端各存一份索引(冗余容灾),支持 4 种冲突策略。该命令独立于同步配置,无需 `config.yaml`。

```bash
filesync.exe bisync --left D:\Project --right E:\Backup --dry-run        # 先预览
filesync.exe bisync --left D:\Project --right E:\Backup                  # 执行同步
filesync.exe bisync --left D:\Project --right E:\Backup --conflict newer-wins  # mtime 新的覆盖旧的
filesync.exe bisync --left D:\Project --right E:\Backup --exclude "**/*.log"   # 排除日志文件
```

> **冲突策略**:
> - `keep-both`(默认):两端都保留,冲突文件重命名为 `*.conflict-left` / `*.conflict-right`
> - `left-wins`:左端覆盖右端
> - `right-wins`:右端覆盖左端
> - `newer-wins`:mtime 更新的一端覆盖另一端

> **索引机制**:两端各存 `.bisync-index.db`,记录各自目录在上次同步后的文件状态。通过对比当前状态与上次状态检测变化。即使交换 left/right 角色,索引仍能正确工作(索引跟随目录,不跟随角色)。

### 全局选项

| 选项 | 适用命令 | 说明 |
|------|----------|------|
| `--config FILE` | `sync`/`status`/`verify`/`reindex`/`prune` | 配置文件路径(默认 `config.yaml`) |
| `--workers N` | `sync`/`bisync` | 临时覆盖并发数(`0` = 用配置默认) |
| `--dry-run` | `sync`/`prune`/`dedup`/`bisync` | 只扫描/预览,不实际改动文件 |
| `--verify` | `sync` | 强制开启拷贝后校验 |
| `--no-verify` | `sync` | 禁用大文件校验(小文件 ≤ 1 MiB 默认仍强制校验)。与 `--verify` 同时给出时以 `--no-verify` 为准 |
| `--no-small-verify` | `sync` | 禁用小文件强制校验,适合海量小文件且可接受少一次目标 hash 的场景 |
| `--strict-hash-scan` | `sync` | 禁用 `size + mtime` 快速跳过,所有候选文件均计算内容哈希 |
| `--index PATH` | `dedup` | 增量索引路径(默认 `.dedup-index.db`) |
| `--exclude PATTERN` | `dedup`/`bisync` | 排除模式(`**` 递归 glob,可重复) |
| `--readonly` | `dedup` | 去重后将文件设为只读(归档场景,防误编辑污染硬链接副本) |
| `--left DIR` | `bisync` | 左端目录 |
| `--right DIR` | `bisync` | 右端目录 |
| `--conflict STRATEGY` | `bisync` | 冲突策略: `keep-both`/`left-wins`/`right-wins`/`newer-wins` |

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
2. **快跳过**:默认先查索引,若 `size + mtime` 与已同步记录一致,直接跳过,避免未变化文件重复读盘。
3. **并发哈希**:对剩余候选文件用 xxh3 并发计算内容哈希,得到 `objectKey`(格式 `h3:<hex>`)。
4. **生成任务**:与索引中已记录的 `size/mtime/objectKey` 比对,仅 mtime 变化则批量更新索引,内容变化或新增则生成同步任务。
5. **并发拷贝**:worker 池执行。`EnsureObject`(对象不存在则从源拷入 CAS)→ `PlaceFile`(NTFS 硬链接 / exFAT 复制)→ 可选校验 → 批量原子更新索引。
6. **报告**:输出拷贝数、跳过数、去重节省、失败列表。

---

## 测试

```bash
go test ./internal/...            # 全部测试(业务包,不含 main)
go test -race ./internal/...      # 含竞态检测
go test -cover ./internal/...     # 覆盖率
```

当前共 **168 个测试**,覆盖 18 个内部包,整体覆盖率 **~81.8%**。

> **覆盖率口径**:用 `./internal/...` 而非 `./...`。`main.go` 是 CLI 入口胶水(命令分发、flag 解析),无单元测试,计入会拉低到 ~70%,不反映业务代码真实覆盖。`main.go` 编译由 CI 的 build 步保证。
>
> **`go vet`**:`go vet ./internal/...` 在各平台对本平台文件无问题。`go vet ./...` 在 Linux 上会因解析 `_windows.go` 的 import 链报错(Go 工具链跨平台解析限制),CI 不跑全量 vet。

### CI/CD

每次 push / PR 自动在 **Ubuntu + Windows** 上跑 `go test -race` + 覆盖率统计 + `go build`。Ubuntu 上额外执行 **80% 覆盖率门槛**(Windows 因 NTFS-only 分支跑不到 copy 模式,数字偏低,不作门槛)。各平台覆盖率产物上传为 artifact,可下载合并查看真实跨平台覆盖。

打 `v*` 标签自动触发 Release，交叉编译 4 个平台并附带 SHA256 checksums：

| 平台 | 产物 |
|------|------|
| Windows amd64 | `filesync-windows-amd64.exe` |
| Linux amd64 | `filesync-linux-amd64` |
| macOS amd64 | `filesync-darwin-amd64` |
| macOS arm64 | `filesync-darwin-arm64` |

---

## 更新记录(CHANGELOG)

### 2026-07-02 测试覆盖提升 + CAS 幂等修复 + CI 覆盖率门槛

- **CAS 幂等修复**:`RemoveTempObject` 与 `DeleteObject` 删除已不存在的 object 时,`Chmod` 先因 `ENOENT` 报错中断,导致 exFAT 崩溃恢复重试失败(prune 重跑会重复删除同一 object)。Chmod 前加 `os.Stat` 短路,不存在直接返回 nil。`DeleteObject` 此前与 `prune.go` 注释"对不存在文件返回 nil"的契约不符,一并修正。
- **覆盖率提升**:整体覆盖率 79.8% → 81.8%。补薄弱路径:`paths.MtimeClose`/`ObjectBuckets`(0%→100%)、`index.ApplyReindexBatch`(0%→75%)、`syncer.SetProgress`(0%→100%)、`lock` 损坏锁文件拒绝路径。幂等修复配套复现测试直接构造 `fileCAS{mode: ModeCopy}` 绕过本地 NTFS 检测,CI Linux 真实 exFAT 路径再验。
- **CI 覆盖率门槛**:`ci.yml` 测试步加 `-coverprofile`,新增 80% 覆盖率门槛(仅 ubuntu)与 coverage artifact 上传。Test 步显式 `shell: bash`,修复 Windows PowerShell 拆分 `-coverprofile=coverage.out` 的问题。
- **清理**:`makeUniqueTestDir` 移除未使用的 `path` 参数(`go vet` unusedparams)。

### 2026-07-01 小文件同步性能优化

- `syncer` 默认启用 `metadata_fast_skip`: 已同步文件 `size + mtime` 未变时跳过内容 hash;需要严格内容扫描可设 `metadata_fast_skip: false` 或使用 `--strict-hash-scan`
- 候选文件 hash 改为按 `workers` 并发,避免同步前串行 hash 成为小文件瓶颈
- `index` 新增 `ApplySyncResults` 批量事务,`copier` 成功拷贝后批量落库,减少海量小文件的 bbolt 写事务次数
- 小文件强制校验改为可配置: `verify_small_files: false` 或 `--no-small-verify`
- CLI 进度输出节流,避免 PowerShell/终端逐文件刷新拖慢同步
- 新增配置、批量索引、严格扫描和校验策略测试

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

实现与设计文档的差异已记录于设计文档 [§16 实现记录与设计偏差](docs/superpowers/specs/2026-06-23-file-sync-design.md#16-%E5%AE%9E%E7%8E%B0%E8%AE%B0%E5%BD%95%E4%B8%8E%E8%AE%BE%E8%AE%A1%E5%81%8F%E5%B7%AE)。主要偏差：

| 偏差 | 实现决策 |
|------|----------|
| objectKey 物理文件名 | 纯 hex（去掉 `h3:` 前缀），因 Windows 不允许文件名含冒号 |
| 空文件哈希 | 实测值 `99aa06d3...`，非占位符 |
| RefCount=0 清理 | `prune` 命令显式清理，未实现异步清理通道 |
| errgroup | 保留 WaitGroup，与错误隔离语义冲突 |


### 2026-06-30 增量去重 + 双向同步

新增三项核心功能：

**增量去重索引 (`internal/fileindex`)**
- 新增轻量级 bbolt 包，存储文件状态 `{path → {size, mtime, hash}}`
- 被 dedup 和 bisync 共用，避免重复实现

**增量去重 (`internal/dedup` 改造)**
- `Run()` 新增可选 `idx` 参数，传入时启用增量模式
- 首次全量扫描并缓存哈希，后续仅重算 (size/mtime) 变化的文件
- 新增 `--index` CLI 参数指定索引路径（默认 `.dedup-index.db`）
- 新增 9 个增量场景测试

**双向同步 (`internal/bisync`)**
- 新增 `bisync` 命令：`filesync bisync --left DIR --right DIR`
- 扫描两端目录，对比索引检测变化（新增/修改/删除）
- 4 种冲突策略：`keep-both`（默认）/ `left-wins` / `right-wins` / `newer-wins`
- 两端各存 `.bisync-index.db`，记录各自上次同步后的状态，冗余容灾
- 索引跟随目录不跟随角色，交换 left/right 仍正确工作
- 新增 18 个测试

**测试总计**: 131 个（原 95 + fileindex 9 + dedup 增量 9 + bisync 18）

### 2026-06-29 跨平台重构 + CI/CD 配置

完成 Windows-only 代码的跨平台拆分，配置 GitHub Actions CI/CD：

- **跨平台重构**：4 个包的 Windows API 用 build-tag 拆分 + cgo 替代 `golang.org/x/sys`：
  - `lock`：`lock_windows.go`（cgo OpenProcess）/ `lock_unix.go`（syscall.Kill）
  - `disk`：`disk_windows.go`（cgo GetDiskFreeSpaceExW）/ `disk_unix.go`（syscall.Statfs）
  - `copier`：`locked_windows.go`（errno 32/33）/ `locked_unix.go`（EBUSY/EACCES/EAGAIN）
  - `paths`：`paths_windows.go`/`paths_unix.go`（Long/IsLong）；`sanitize_windows.go`/`sanitize_unix.go`
- **模块路径**：`github.com/ljw/filesync` → `github.com/ljwqf/filesync`
- **CI/CD**：`.github/workflows/` 下 CI（Ubuntu + Windows 矩阵）+ Release（4 平台交叉编译）
- **测试跨平台**：config/copier/paths 测试加 `runtime.GOOS` 平台判断
- **YAML 转义修复**：`yaml.v3` 双引号内 `\P` 被解释为 U+2029，含反斜杠路径改用单引号包裹
- **sanitize 行为**：Unix 只 strip `/` 和 ` `，反斜杠和 `..` 为合法 Unix 文件名字符
