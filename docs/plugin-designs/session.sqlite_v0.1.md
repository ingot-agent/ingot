# `session.sqlite` Plugin v0.1 设计方案

> 状态：Implemented v0.1（M5）
> Exports：`session.Store`、`session.Manager`、`session.Query`
> State：Plugin-scoped SQLite database，schema version 1

## 1. 边界

`session.sqlite` 是正式的本地 Session persistence implementation。它把三类
能力作为独立 capability 导出，但由同一个事务型 store 实现：

```text
session.sqlite --session.Store----> agent.default / context.compact / app.cli
session.sqlite --session.Manager--> app.cli
session.sqlite --session.Query----> app.cli
```

Store 只持久化 opaque `session.Entry`，不解释 Agent message、tool call、asset
reference 或其他 payload schema。Session Delete 与 Fork 都不管理 Asset 生命周期。

## 2. Component Contract

```go
type Config struct{}

type Dependencies struct {
    State state.Scope
}

type Exports struct {
    Store   session.Store
    Manager session.Manager
    Query   session.Query
}
```

M5 没有可配置 policy。`New` 要求绝对、非空的 plugin State directory，创建或
打开 `sessions.sqlite3`，启用 foreign key enforcement，并返回负责关闭数据库的
Cleanup。

## 3. Schema

```sql
CREATE TABLE sessions (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    archived_at INTEGER
);

CREATE TABLE entries (
    session_id TEXT NOT NULL,
    sequence   INTEGER NOT NULL,
    kind       TEXT NOT NULL,
    version    INTEGER NOT NULL,
    payload    BLOB NOT NULL,
    PRIMARY KEY (session_id, sequence),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);
```

`PRAGMA user_version=1` 标识数据库 application schema。时间以 UTC Unix
nanoseconds 存储；这是 implementation detail，SDK 对外仍返回 `time.Time`。

## 4. Operation semantics

- `Create` 生成 128-bit random ID；创建 active、零 Entry 的 Session，且
  `CreatedAt == UpdatedAt`。
- `Append` 在同一 transaction 中检查 archived state、分配下一个 sequence、
  插入完整 Entry 并推进 `UpdatedAt`。Archived Session 返回
  `session.ErrArchived`。
- `Load` 对 active/archived Session 都可用，严格按 sequence 返回 caller-owned
  payload。
- `Rename` 只修改 Title；`Archive`/`Restore` 是 desired-state idempotent
  operation；三者都不修改 `UpdatedAt`。重复 Archive 保留原 `ArchivedAt`。
- `Delete` 删除 active/archived Session，并由 foreign-key cascade 在同一
  transaction 删除 Entries；不存在返回 `session.ErrNotFound`。
- `Fork` 在一个 transaction 中建立新 active Session，并以 SQL logical copy
  保留 source Entry count/order/kind/version/payload。Archived source 允许 Fork；
  target 不继承 source lifecycle state。
- `List` 返回 active 与 archived Session，固定排序为
  `UpdatedAt DESC, CreatedAt DESC, ID ASC`。

## 5. Concurrency and durability

实例对所有调用 concurrent-safe。v0.1 使用单 SQLite connection 串行 transaction，
因此同一数据库内 Append、Fork 与 Delete 具有明确的 transaction ordering；Fork
观察到 source 的完整稳定边界，不会复制 partial Entry。

SQLite commit 是 operation success boundary。Append error 仍遵循 SDK 的
conservative retry contract：caller 不得仅因返回 error 自动重试。

## 6. Manifest

```toml
manifest_version = 1
name = "session.sqlite"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."

[state]
schema_version = 1
min_reader_version = 1
```

旧实验性 `session.jsonl` 不提供 migration 或 compatibility；M5 直接移除该
Plugin、状态格式、测试和文档。
