# ADR 0003：Plugin Configuration 所有权

- 状态：Frozen
- 里程碑：M0（定稿）/ M1（落地）
- 相关：`internal/builder/generate.go`、`internal/builder/graph.go`、`internal/home/init.go`、`ingot-abi/state`、`sdk/interaction`

## 背景

当前模型：

```text
runtime 读取统一 config.toml
  ↓
decodeConfigs 为每个 Plugin 解出一份 Config（generate.go:340-390）
  ↓
Plugin.New(ctx, Config, deps)（graph.go:395-410 强制签名）
```

后果：

- runtime 启动时**必须**为每个 Plugin 找到配置表，否则
  `resolveConfigTable` 报 `missing config table` 直接退出（`generate.go:360`）；
- 「Plugin 允许处于 Unconfigured 状态」在现状下**不可能**；
- `ingot init` 需要生成覆盖全部 Plugin 的 config.toml 模板（`init.go:189`）；
- 配置有两个潜在入口：`Config` 参数与 `state.Scope` 目录。

## 决策

### 1. 移除 Config 参数

Component 构造函数签名变为：

```go
func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error)
```

Plugin 通过 `deps.State`（`state.Scope`，已提供绝对目录）自己持久化配置。
不再有统一 Runtime `config.toml`，也不再由 Host decode 全部 Plugin Config。

新的启动模型：

```text
resolve Runtime Home
  ↓
create state scopes
  ↓
construct Plugins
  ↓
Plugin loads its own state/config
  ↓
Runtime starts
```

### 2. 未配置是合法状态

Plugin 必须允许处于 **Unconfigured**。未配置不能导致 Plugin 完全无法创建，
否则用户无法通过 Operation 完成初次 Setup。

### 3. 配置的交互模型：Operation + Interaction + State

配置变更**不是**用两个配对的 Operation（`config.get` / `config.set`）建模，
而是：

```text
Operation 触发变更
  ↓
Plugin 发 interaction.Request（结构化表单）
  ↓
Host 渲染并回传 interaction.Response
  ↓
Plugin 写入自己的 state.Scope
```

现有链路已经具备（`plugins/app-webui/app/operations.go:207` 提供
`operation.Request.Interaction`；`host/interaction.go:425-429` 渲染 `Sensitive`
且敏感字段的 Default 不下发），因此：

- **命名空间问题消失**：是 Plugin 主动向 Host 要什么，
  Host 不需要推断 Operation 归属；表单自描述（Label/Description 在 Request 内）；
- **get-before-set 消失**：不存在两个需要配对的 Operation；
  Plugin 自己读 state、把当前值放进 `Field.Default`、发一个 `Request`；
- **secret 天然安全**：`Sensitive` 字段的 Default 不下发，
  Plugin 不需要读取旧值，收到新值直接写入。

### 4. 扩展 interaction 支持嵌套

现有 `interaction.FieldKind` 只有
`String/Integer/Number/Boolean/Choice/MultiChoice`，`Value` 没有嵌套表示。
官方插件中相当一部分配置是嵌套/重复结构
（`model.openai-compatible` 的 `providers`、`interceptor.approval` 的 `rules`、
`usage.default` 的 `routes`、`tool.shell` 的 `environment` 等）。

决策：**扩展 interaction 的字段与值模型以支持嵌套/重复字段**，而不是让
Plugin 自行拆分多次请求，也不是退回「编辑配置文件」。

扩展必须向后兼容，涉及 `sdk/interaction/interaction.go` 的
`FieldKind`/`ValueKind`，以及 `plugins/app-webui/host/interaction.go` 中
`validateDefault`、`validateAnswer`、`projectField` 三处 switch 与前端渲染。

### 5. Operation 不区分「配置类」

Host 不区分某个 Operation 是否为配置入口，对所有 Operation 平等处理。
配置的可发现性由 Plugin 自己的 Operation name/description 承担。

**已知取舍**：M5/M6 的 Manager 无法自动把「配置」归类出来，
需要依赖插件命名约定或用户自行识别。这是为保持「消费方平等处理」付出的代价。

## 后果

- M1 需要修改：builder 的构造函数签名校验与 generated wiring（移除
  `decodeConfigs`、`configs.Plugin%d`）、`ingot init` 不再生成 config.toml、
  16 个官方 Plugin 的 `New` 签名。
- 现有 `Config` 结构体可以保留为 Plugin 内部的持久化表示，但不再是
  Host 注入的构造参数。
- interaction 扩展属于 SDK 协议变更，需要 SDK 与 app-webui 同步演进。

## 不冻结

- `state/` 内配置文件的格式（Plugin 自定，TOML/JSON/sqlite 均可）；
- schema 定义与校验细节（Plugin 自定）；
- state migration policy 与兼容边界（Plugin 负责，M2 需要明确）；
- 嵌套字段的具体类型设计（M1 在 SDK 内定稿）。
