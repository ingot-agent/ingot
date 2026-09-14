# ADR 0007：Collection v1 文件、规划与应用语义

- 状态：Frozen
- 里程碑：M4
- 相关：`0005-collection.md`、`internal/collection`、`internal/home/collection.go`

## 背景

ADR 0005 已冻结 Collection 是 Direct Plugin Set 的输入变换，但没有冻结文件格式、
冲突处理、顺序合并或提交语义。Plugin 顺序会影响 MANY capability、Interceptor 与
Prompt Contributor 等运行行为，因此 Collection 不能静默重排已有 Plugin。

## 决策

### 1. Collection v1 是可从本地文件或 HTTPS 获取的严格 TOML 文件

```toml
collection_schema = 1
id = "github.com/example/ingot-collections/coding"
version = "v1.0.0"

[metadata]
name = "Coding Essentials"
description = "A curated coding stack"
homepage = "https://example.com/coding"

[[plugins]]
module = "github.com/ingot-agent/plugins/tool-shell"
version = "v0.1.0"
```

- `id` 使用 Go module path 语法；Collection 与 Plugin version 都是 exact canonical
  Go module version，并遵守 semantic import major。
- `[metadata]` 只包含必填 `name` 与可选 `description`、`homepage`。
- Plugin 只允许 Module source；Collection v1 不包含 Local Path 或嵌套 Collection。
- Collection semantic digest 覆盖 identity、metadata 与 ordered Plugin references。
- HTTPS 获取可使用 expected digest 固定语义内容；M4 不提供 registry 或缓存。

### 2. Planner 先分类 source/version，再处理顺序

每个 Plugin 分类为 Add、Satisfied、VersionConflict 或 SourceConflict。已有 Local Path
不会被 Collection 替换；已有 exact module version 不会被自动升级或降级。

Collection 顺序必须是结果 Direct Plugin Order 的子序列，不要求形成连续块。共同
Plugin 的现有顺序与 Collection 顺序不一致时产生 OrderConflict，并报告 current、
required 与反序对。

默认 Plan 不可应用。显式 `accept-order` 后：

1. 保持非 Collection Plugin 彼此顺序；
2. 在满足 Collection 共同 Plugin 顺序的排列中最小化相对当前顺序的 Kendall 反转数；
3. 同成本时优先保留当前靠前 Plugin；
4. 再稳定合并缺失 Plugin，使重排后的现有序列和完整 Collection 序列均为子序列，
   可选节点同时出现时优先现有 Plugin。

`accept-order` 不处理 VersionConflict 或 SourceConflict。

### 3. Apply 必须在项目锁内重新规划并事务提交

Apply 获取 Collection 后才获取项目 writer lock；在锁内重新读取 `plugins.toml`、规划、
构造 candidate、执行完整 Builder resolve preflight，然后复用项目事务原子提交
`plugins.toml` 与 `plugins.lock`。任何失败都不修改两者。

全部 Plugin 已满足且顺序合法时 Apply 是 no-op，不刷新 lock。M4 不记录 receipt；Apply
后唯一权威 desired state 仍是 `plugins.toml`。

## 理由

- exact Module reference 直接复用 M3 的统一 Plugin source 与 Go MVS 管道。
- 严格冲突与显式顺序授权避免 Collection 改变现有运行语义而用户不知情。
- 最小反转给显式授权后的自动重排一个确定、可测试且扰动最小的规则。
- 不记录 receipt 避免把 Collection 误建模为长期 dependency owner。

## 后果

- CLI 提供 `collection inspect`、`collection plan`、`collection apply`。
- Version/Source conflict 必须先通过普通 Plugin mutation 手工解决。
- Marketplace、签名、Collection update/remove、receipt 与其他 source kind 留给后续里程碑。
