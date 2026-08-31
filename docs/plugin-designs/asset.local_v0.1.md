# `asset.local` Plugin v0.1 设计方案

## 1. 目标与边界

`asset.local` 在 Plugin-scoped State 目录中保存不可变二进制对象，向 Component
Graph 提供 SDK `asset.Store`。由于 `asset.Store` 内嵌 `asset.Resolver`，同一导出
值同时满足写入方和只读消费方，避免为同一实例导出两个可赋值字段而造成 Builder
候选歧义。

插件只保存 bytes。MIME type、文件名和内容 Kind 仍由 `content.Media` 维护；Session
只持久化 opaque `asset.Reference`，不会复制媒体 bytes。

## 2. Component Contract

```go
type Config struct {
    MaxObjectBytes int64 `toml:"max_object_bytes"`
    MaxTotalBytes  int64 `toml:"max_total_bytes"`
    IOConcurrency  int   `toml:"io_concurrency"`
}

type Dependencies struct {
    State state.Scope
}

type Exports struct {
    Store asset.Store
}
```

默认值分别为 64 MiB、10 GiB 和 8 个并发 I/O。限制值必须为正，单对象上限不得
大于总容量。`state.Scope.Dir()` 必须是非空绝对路径。

## 3. Identity 与布局

实现使用 `sha256:<lowercase hex>` content address 作为本地 Reference ID，并按
digest 前两位分片保存到 `blobs/`。该格式是实现细节，调用方只能将 ID 视为 opaque
value，不得自行推导路径。

写入先进入 `staging/` 的 0600 临时文件，同时计算 digest 并验证实际读取字节数与
`PutRequest.Size` 完全一致。临时文件 `fsync` 后，以 hard link 原子发布到最终路径，
再同步 shard 目录。相同 bytes 去重；已有目标必须具有相同大小和 digest，否则按
损坏或碰撞失败。

启动时创建 0700 目录、清理所有未完成 staging entry、扫描 durable blob 的总字节
数，并在已存内容超过配置容量时拒绝启动。Manifest State schema version 为 1。

## 4. 生命周期、并发与限制

- `Put` 在读取 Body 前检查声明大小；Body 由调用方管理，插件不关闭它。
- `Put`、`Open` 共享实例级 I/O semaphore；等待过程响应 Context。
- 每次 `Open` 返回独立 reader；reader 关闭时释放 semaphore slot。
- `Stat` 和 `Open` 对非法、缺失或非普通文件的 Reference 返回明确错误。
- 成功发布后没有 Update、Overwrite、Delete 或 ID 复用路径。
- 总容量按唯一 durable blob 计算，重复 Put 不重复计费。

v0.1 采用保守的永久保留策略，不执行引用扫描或自动 GC。这避免在共享引用和跨
Session 引用尚无统一枚举 Contract 时误删对象；后续保留期/扫描实现仍属于 Asset
插件配置演进，不进入 SDK。

## 5. 验收

- 精确大小、短读、长读、零字节、单对象与总容量边界；
- 相同内容去重、不同内容 identity、损坏目标拒绝；
- 原子发布、启动清理 staging、重启后 Reference 稳定；
- `Stat`/`Open` 原始 bytes 一致，多个 reader 相互独立；
- 并发 `Open`/`Put` race test 与 Context 等待；
- 非法和未知 Reference、无效 State 与配置显式失败。
