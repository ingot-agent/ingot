# `session.jsonl` Plugin v0.1 设计方案

> 状态：Implemented v0.1  
> Component：`default`  
> Exports：`session.MutableStore`
> Persistent State schema：1

## 1. 定位

`session.jsonl` 使用 Plugin-scoped 本地目录持久化 Agent Session。它实现 append-oriented `session.MutableStore`，以每个 Session 独立 JSONL 文件提供 same-session total order，并允许不同 Session 并发访问；Title 通过原子 metadata replacement 更新，不进入消息序列。

典型连接关系：

```text
session.jsonl --session.Store--------> agent.default
session.jsonl --session.MutableStore-> app.cli/app
```

## 2. 目标与非目标

目标：

- 成功 Append 将一个完整 Entry 原子加入 committed sequence；
- 同一 Session 的成功 Append 形成 total order；
- `Load` 按 committed order 返回；
- 不同 Session 可并行；
- 使用 `config.StateDir(ctx)`，不读写 workspace 或任意全局路径；
- 磁盘格式带独立版本并按 reader window fail-closed；
- 检测中间损坏，并安全处理未提交的尾部残片；
- `Create`、`Append`、`Load`、`List`、`Rename` 均支持 Context cancellation；
- Rename只更新展示Title，不改变Session identity、消息顺序或会话时间；
- 返回值与输入值遵守 aggregate ownership 规则。

非目标：

- 分布式 Session Store；
- Entry 更新、删除或随机位置插入；
- Session 删除、任意metadata patch、全文检索；
- 对 Entry payload 的领域 schema 做解释；
- 跨多个 Session 的事务。

v0.1 通过 StateDir owner lock 保证同一时刻只有一个 Runtime process writer；跨进程共享仍不是本地 Store 的数据协作协议。

## 3. Component Contract

```go
package sessionjsonl

import (
    "context"

    "github.com/ingot-agent/sdk"
    "github.com/ingot-agent/sdk/session"
)

type Dependencies struct{}

type Exports struct {
    Store session.MutableStore
}

func New(
    ctx context.Context,
    cfg Config,
    deps Dependencies,
) (Exports, sdk.Cleanup, error)
```

## 4. Config

```go
type Config struct {
    Durability string `toml:"durability"`
}
```

| Value | 行为 |
|---|---|
| empty / `sync` | 每次成功 Append 在返回前执行 file `Sync`；Create/metadata commit 同步必要的 directory；默认 |
| `flush` | 写入并 close，依赖操作系统后续 flush；不承诺 power-loss durability |

其他值在 `New` 阶段返回 Config Error。

无论 durability policy 如何，成功 Append 都必须对本进程后续 `Load` 可见，并且不得暴露部分 JSON record。

## 5. State directory 与版本

`New` 使用传入的 Component Context：

```go
stateDir, err := config.StateDir(ctx)
```

缺少 scope 时保留 `config.ErrStateDirUnavailable`。不得回退到 working directory、用户 home 或临时目录。

目录布局：

```text
<StateDir>/
├── .ingot-owner.lock
├── state.json
└── sessions/
    └── <session-id>/
        ├── metadata.json
        └── entries.jsonl
```

`state.json` exact shape：

```json
{"schema_version":1}
```

规则：

- 空目录由 v0.1 实现原子初始化为 schema 1；
- 已有 `state.json` 必须 strict decode；
- version 不在 Manifest reader window `[1,1]` 时 `New` fail-closed；
- `--ingot-check` 获得隔离空 StateDir，因此只验证初始化、构造和 Cleanup，不接触 production State；
- unknown State metadata field 视为不兼容，不静默忽略。

## 6. Session ID 与目录映射

`Create` 使用 `crypto/rand` 生成 16 bytes，并编码为 32 个 lowercase hex 字符：

```text
[0-9a-f]{32}
```

目录名精确等于 ID bytes。所有接收 `session.ID` 的操作先验证该 grammar；外部构造的非法 ID 返回 `ErrInvalidSessionID`，不得进入路径拼接。

随机碰撞时重试有限次数；超过上限返回带上下文的错误。不得使用 Title、时间戳或自增数字作为 ID。

建议 Plugin sentinel：

```go
var (
    ErrInvalidSessionID = errors.New("invalid session id")
    ErrInvalidEntry     = errors.New("invalid session entry")
    ErrCorruptState     = errors.New("corrupt session state")
    ErrUnsupportedState = errors.New("unsupported session state version")
    ErrInvalidQuery     = errors.New("invalid session query")
    ErrStateDirLocked   = errors.New("session state directory is locked")
    ErrOwnerLockUnsupported = errors.New("session state owner lock unsupported")
    ErrCommitUnknown    = errors.New("session append commit status unknown")
)
```

Session 不存在时必须保留 SDK 的 `session.ErrNotFound`。

## 7. 磁盘格式

### 7.1 `metadata.json`

Exact shape：

```json
{
  "record_version": 1,
  "id": "0123456789abcdef0123456789abcdef",
  "title": "Example",
  "created_at": "2026-08-22T12:00:00Z"
}
```

- 时间编码为 UTC RFC3339Nano；
- `Create` 原样保存 `Metadata.Title`；
- `Rename` 在持有session gate时以同目录临时文件原子替换metadata，只修改`title`；
- Rename不修改`created_at`，也不影响由entries计算的`UpdatedAt`；
- `Metadata.CreatedAt` 必须非 zero，Plugin 不自行替换调用者时间；
- unknown field、ID mismatch、invalid time 或 unsupported record version 视为 corrupt state；
- metadata 使用同目录临时文件写入并原子提交。

### 7.2 `entries.jsonl`

每行是一个完整 persistence record：

```json
{
  "record_version": 1,
  "appended_at": "2026-08-22T12:01:00Z",
  "entry": {
    "kind": "message",
    "version": 1,
    "payload": {}
  }
}
```

物理文件使用 compact JSON，每个 record 后恰好一个 `\n`。字段顺序不属于持久化语义，但 Writer 使用固定 struct 产生稳定、可读 diff。

层次区别：

- `state.json.schema_version`：整个 Plugin State format；
- `record_version`：metadata/JSONL record envelope；
- `session.Entry.Version`：某一 `Entry.Kind` 的 payload schema。

Store 不解释 payload schema，但 v0.1 写入前要求：

- `Entry.Kind` 非空；
- `Entry.Version > 0`；
- `Entry.Payload` 是有效 JSON value；
- 先复制/序列化 `json.RawMessage`，返回后不再引用 caller 数据。

## 8. Operation semantics

### 8.1 `Create`

1. 检查 Context；
2. 校验 `CreatedAt` 非 zero；
3. 生成 ID；
4. 在 `sessions/` 下创建隐藏 candidate directory；
5. 写入并原子提交 `metadata.json`；
6. 创建空 `entries.jsonl`；
7. durability=`sync` 时 Sync 文件和必要的 parent directory；
8. 完成 candidate 后通过同一文件系统内 rename 原子发布为 `<session-id>`，再返回 ID；List 忽略未发布 candidate。

失败时只清理本次尚未发布的 candidate directory，不影响其他 Session。成功 Create 后的空 Session：

```text
Load => empty []Entry
UpdatedAt => CreatedAt
```

### 8.2 `Append`

同一 Session 的流程：

1. 在获取 session gate 前检查 Context；
2. 以可取消方式等待 session gate；
3. 获取后再次检查 Context；
4. 验证 Session metadata 和 entries tail；
5. 复制并序列化完整 record；
6. append 全部 bytes 和 newline；
7. durability=`sync` 时执行 file `Sync`；
8. 成功后释放 gate 并返回。

同一 gate 内只允许一个 Append，因此成功顺序就是 file record 顺序。不得使用普通 `sync.Mutex` 直接等待，因为它无法在等待期间观察 Context；推荐使用容量为 1 的 channel gate，并通过 select 获取。

写操作需要处理 short write，只有全部 bytes 写完并满足 durability policy 才返回成功。

如果写入、`Sync` 或 `Close` 阶段返回错误，record 可能已经存在，错误包装 `ErrCommitUnknown`；只有在打开文件前失败或明确未写入时，调用方才可以按 definitely-uncommitted 处理。

### 8.3 `Load`

- 以同一个 session gate 与 Append 串行，避免观察到正在写入的尾部；
- 逐行 strict decode，验证 record version、timestamp 和 Entry；
- 按文件顺序返回新的 `[]session.Entry`；
- 每个 `Payload` 都是独立 copy；
- 不存在返回包装 `session.ErrNotFound`；
- 等待 gate、扫描和 decode 期间周期性检查 Context。

### 8.4 `List`

`Query` 规则：

- `Offset >= 0`；
- `Limit >= 0`；
- `Limit == 0` 表示不限制；
- negative value 返回 `ErrInvalidQuery`。

排序固定为：

```text
UpdatedAt descending
CreatedAt descending
ID UTF-8 bytes ascending
```

`UpdatedAt`：

- 无 Entry 时等于 `CreatedAt`；
- 有 Entry 时等于最后一个 committed record 的 `appended_at`。

List 扫描 Session metadata，并读取每个 entries 文件的最后一个完整 record。它不依赖 map iteration 或 filesystem enumeration order；排序完成后再应用 Offset 和 Limit。

若任一已发布 Session 存在中间 corruption，List 返回 `ErrCorruptState`，不静默隐藏该 Session。

### 8.5 `Rename`

1. 校验Session ID、Context和非空valid UTF-8 Title；
2. 以可取消方式获取与Append/Load相同的session gate；
3. strict读取并验证现有metadata；
4. 只替换Title，保留ID、record version和CreatedAt；
5. 将完整metadata写入同目录临时文件，按durability policy Sync；
6. 原子替换`metadata.json`并在sync模式同步目录；
7. 成功返回后后续List必须看到新Title。

Session不存在时保留`session.ErrNotFound`。Rename不追加`entries.jsonl`，因此不会改变对话`UpdatedAt`或Agent history。Windows使用replace-existing且write-through的原子文件替换，Unix使用同文件系统rename replacement。

## 9. Tail recovery 与 corruption

Writer 总是以 newline 结束 committed record。打开 Session 时：

- 文件以 newline 结束：解析所有 record；
- 最后存在无 newline 的残片：视为未提交 tail，在持有 session gate 时截断到最后一个 newline；
- 空文件合法；
- 任意完整行 JSON 无效、record version 不支持或字段不合法：返回 `ErrCorruptState`；
- 中间损坏永不跳过、重排或自动重写。

截断 tail 前，在 durability=`sync` 模式下记录诊断并在截断后 Sync。自动修复仅限最后一个物理残片；完整但语义非法的最后一行仍视为 corruption。

## 10. 并发与内部状态

Store 持有 keyed session gate registry：

- key 为已验证的 Session ID；
- gate 支持 Context-aware acquire；
- registry 使用短期 mutex 保护创建和引用计数；
- operation 完成后释放引用，无活动 operation 的 gate 可以回收，避免无限增长；
- Create 和 State 初始化使用独立的短期 coordination；
- 不同 Session 的 Append/Load/Rename 不共用全局 I/O 锁。

本实现使用 StateDir owner lock 检测第二个 writer 并 fail-fast；锁实现按平台提供，无法提供可靠锁的平台在 `New` 阶段返回 `ErrOwnerLockUnsupported`。锁由 Cleanup 释放。进程内仍使用 keyed session gate 保证同一 Session 的顺序。

## 11. Cleanup

推荐 v0.1 每次 operation 短暂打开文件，不缓存 session file handle，不启动后台 flush goroutine。

- `New` 成功返回非 nil Cleanup，Cleanup 释放本实例 owner lock handle；
- Cleanup 不修改或 compact Session 数据；
- Cleanup 观察 Context，在释放本地句柄后及时返回。

## 12. Manifest

```toml
manifest_version = 1
name = "session.jsonl"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."

[state]
schema_version = 1
min_reader_version = 1
```

## 13. 测试方案

### 13.1 State initialization

- empty StateDir 初始化；
- missing Context scope；
- strict `state.json`；
- supported、older、newer和非法 schema version；
- check-mode 空目录不触碰 production fixture。

### 13.2 CRUD Contract

- Create/Load empty Session；
- Append/Load round trip；
- Title、CreatedAt、UpdatedAt；
- Rename持久化、非法/空Title、missing ID、Context取消；
- Rename不改变CreatedAt、UpdatedAt和Entry sequence；
- missing和非法 ID；
- invalid Entry kind/version/payload；
- caller 在 Append 返回后修改 RawMessage 不影响结果；
- caller 修改 Load 返回 Payload 不影响后续 Load。

### 13.3 Ordering 与 Context

- 同一 Session 并发 Append，确认每个成功调用对应一个完整 record，Load 顺序与提交顺序一致；
- 不同 Session 并发；
- 等待 same-session gate 时 Context cancellation；
- 大文件 Load/List 过程中 cancellation；
- `go test -race ./...`。

### 13.4 Recovery

- empty file；
- partial final JSON；
- partial UTF-8；
- invalid middle line；
- unsupported record version；
- metadata/ID mismatch；
- durability sync/flush；
- short-write 与 Sync failure 注入。

### 13.5 Pagination

- UpdatedAt/CreatedAt/ID tie-break；
- stable ordering independent of directory enumeration；
- Offset、Limit、zero limit、out-of-range offset；
- negative query。

测试使用 `t.TempDir()` 和 `config.WithStateDir`，通过可注入 clock、random reader、file operation adapter 制造确定性 ID、时间和失败，不在公共 SDK Contract 中暴露这些测试依赖。

## 14. 验收标准

1. 完整实现 `session.MutableStore` 并通过外部 package compile assertion；
2. same-session total order 和 cross-session concurrency 有确定性测试；
3. 等待内部 serialization 时 Context 可取消；
4. State schema、record envelope 和 Entry schema 三层版本明确；
5. partial tail 只按规定恢复，中间 corruption fail-closed；
6. `List` 顺序和 pagination 固定；
7. input/output ownership 测试覆盖 `json.RawMessage`；
8. 所有文件路径只来自 `StateDir` 和已验证 ID；
9. 普通测试、`go vet` 和支持环境中的 race test 通过。

## 15. v0.1 实现决策

- module path 为 `github.com/ingot-agent/session-jsonl`；
- 提供跨进程 StateDir owner lock；第二个 writer 以 `ErrStateDirLocked` fail-fast；
- Append 在写入、Sync 或 close 阶段发生错误时包装 `ErrCommitUnknown`，调用方不得把该错误当成 definitely-uncommitted；
- `durability` 对用户公开，支持默认 `sync` 与显式 `flush`；
- `Metadata.CreatedAt == zero` 拒绝，不由 Store 隐式补值；
- `List` 遇到单个 corrupt Session 时整体 fail-closed；
- 首版使用目录扫描，不维护独立 index。
- `Rename`复用same-session gate并原子替换metadata，不写Entry sequence。
