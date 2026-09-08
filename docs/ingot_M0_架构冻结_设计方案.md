# ingot M0 架构冻结：Image / Runtime / Runtime Home / Plugin Configuration / Collection 设计方案 v0.1

> 状态：Frozen（M0 已定稿，决策记录见第 11 节）
> 范围：仅冻结 Contract，不交付功能
> 上游：`../../Ingot 下一阶段 Roadmap：Runtime、Composition 与 Plugin Ecosystem.md`（M0）
> 关联：`./ingot_架构设计_v0.3.md`、`./ingot.plugin.toml_设计方案_v0.1.md`、`./ingot_plugins.toml_v0.1_设计方案.md`、`./ingot_plugins.lock_v0.1_设计方案.md`、`./ingot_ABI_v0.1_设计提案.md`、`../../sdk/`、`../../ingot-abi/`
> 现状差异与迁移影响见：`./ingot_M0_现状差异与迁移影响.md`

## 1. 定位与范围

M0 不以功能为目标。它的唯一产出是把接下来会影响大量代码的基础 Contract 固化下来，使 M1（Standalone Runtime 与 Plugin-Owned State）与 M2（Image / Runtime / Multi-Process）可以按既定模型直接实现，而不是边写边改模型。

本文件冻结五个对象：

| 对象 | 一句话定义 | 首次落地里程碑 |
|---|---|---|
| Image | named + versioned + content-addressed 的不可变构建产物 | M2 |
| Runtime | 持久化执行环境，= Image + 隔离的 Runtime Home | M1（standalone）/ M2（registry） |
| Runtime Home | Runtime 拥有的持久化目录，Plugin state 的唯一定位来源 | M1 |
| Plugin Configuration | Plugin 自有的持久化状态 + Operation 形式的管理面 | M1 |
| Collection | 对 Direct Plugin Set 的声明式 Composition Recipe | M4 |

不在本文件范围：

- Manager、Marketplace、Registry（M5–M7）；
- Plugin 拆仓与发布基础设施（M3）；
- 现有 Application Web UI 的体验改进（持续支线）；
- 具体 Plugin 的 state schema 与 migration 实现（Plugin 自己负责，本文件只冻结边界）。

本文件是规范来源（normative）。与旧设计文档冲突时，以本文件为准；旧文档中尚未被本文件覆盖的部分继续有效。

### 1.1 Process 与三个对象的关系

```text
Image
  what to run

Runtime
  where it lives

Process
  one execution
```

Process 是 Runtime 的一次具体执行。M0 只冻结「Process 不是 Runtime」这一区分，Process 的完整记录模型（pid、started_at、health、endpoint、exit_status 等）属于 M2。

## 2. 核心模型

```text
Plugin
  最小能力与发行单元

Collection
  可复用的 Plugin Composition Recipe
  （不进 Runtime，不进 Component Graph）

Image
  named + versioned + content-addressed immutable artifact

Runtime
  persistent execution environment
  = Image + isolated Runtime Home
```

四条不变式：

1. **Composition 只发生在构建期。** Runtime 不做插件发现、依赖解析、动态加载。
2. **Image 不可变。** 一个 Image digest 对应唯一 artifact；version 一旦发布不可重指向不同 digest。
3. **Runtime 之间状态完全隔离。** 同一 Image 可以创建多个 Runtime，互不可见。
4. **Plugin 拥有自己的配置与状态。** Host 只提供位置与管理面约定，不解释内容。

## 3. Image

### 3.1 身份三要素

Image 必须具名并显式版本化：

```text
name
version
digest
```

示例：

```text
coding-agent:1.4.0
sha256:9f2c...
```

| 要素 | 表达 | 语义 |
|---|---|---|
| `name` | 产品身份 | 用户认知的产品名，跨版本稳定；同一 name 下可有多个 version |
| `version` | 用户可理解的发布版本 | 语义化版本，发布后不可重指向不同 digest |
| `digest` | 真实 immutable artifact identity | 内容寻址，是唯一可以校验 artifact 身份的标识 |

三者职责不可合并：

- `name` 不是 digest 的别名；
- `version` 不是 mutable tag；
- `digest` 不承载产品语义。

### 3.2 命名与版本语法

冻结语法（严格，不接受隐式补齐）：

```text
name        = segment *( "." segment )
segment     = ( [a-z0-9] / [a-z0-9] *[a-z0-9-] [a-z0-9] )
version     = major "." minor "." patch [ "-" prerelease ]
major       = "0" / ( [1-9] *DIGIT )
minor       = "0" / ( [1-9] *DIGIT )
patch       = "0" / ( [1-9] *DIGIT )
prerelease  = 1*( [0-9A-Za-z] / "-" / "." )
digest      = "sha256:" 64HEXDIG
```

即 `segment` 长度为 1 时只允许 `[a-z0-9]`，长度大于 1 时首尾必须为 `[a-z0-9]`。

约束：

- `name` 只允许小写 ASCII 字母、数字、`-` 与 `.`；不允许以 `-` 开头或结尾；不允许空 segment；
- `name` 不包含 `:`、`/`、`@`；
- `version` 为严格三段语义化版本，`major`/`minor`/`patch` 不允许前导零，prerelease 只允许 `[0-9A-Za-z.-]+` 且非空；
- `digest` 固定 `sha256:` 前缀 + 64 位小写十六进制。

### 3.3 引用形式

Image Reference 冻结三种形式：

```text
<name>                    # 需要 alias 或「该 name 唯一版本」语义时使用，见 3.5
<name>:<version>          # 精确发布版本
<name>@<digest>           # 精确 artifact 身份
```

规则：

- 精确 version 与 digest 都可用于 Runtime 绑定、升级、回滚；
- digest 形式不要求 name 必须存在（用于完整性校验与离线分发）；
- 任何需要可复现的地方（lock、Runtime 记录、Receipt）都必须落 `name:version` 与 `digest` 两者，不能只落其一。

### 3.4 不可变性与发布规则

冻结以下规则：

1. 正式 version 发布后，不允许重新指向不同 digest。
2. 同一 `name:version` 在不同机器、不同时间必须解析到同一 digest。
3. digest 只由构建输入与产物内容决定，不由 `name`/`version` 决定。
4. 修改任何构建输入（Plugin set、Plugin 版本、本地源码内容、toolchain、target）都必须产生新 digest；因此也必须产生新 version，除非该 version 尚未发布。
5. 已发布的 `name:version` 只能被 yank（标记不可用），不能被复用或覆盖。

### 3.5 Alias 与 Version 的严格区分

未来可额外支持：

```text
latest
stable
dev
```

冻结边界：

| 概念 | 可变性 | 是否可进入 Image 身份 | 是否可用于可复现引用 |
|---|---|---|---|
| version | 不可变（发布后） | 是 | 是 |
| digest | 不可变 | 是 | 是 |
| alias | 可变 | 否 | 否 |

- alias 必须始终解析到某个具体 `name:version` / digest；
- alias 不参与 lock、Runtime 记录、Receipt 的权威字段；
- alias 只能出现在用户交互入口（CLI、UI、Marketplace），一旦越过边界必须立即解析为具体 version 或 digest；
- alias 解析结果必须可被记录与回放，不允许「隐式跟随最新」。

### 3.6 与现有模型的关系

现状中 Image 身份是 `sha256:...`（内容寻址，由 `CanonicalBuildManifest` 派生），没有 name 与 version，也没有 catalog。M0 冻结的方向是：

- 保留 digest 作为 artifact identity（现有内容寻址语义不推翻）；
- 新增 `name` 与 `version` 作为 artifact 元数据与产品身份；
- `name`/`version` 不参与 artifact digest 计算，避免「改名换镜像」；
- 引入 Image catalog：`name → { version → digest }`，这是 M2 的交付物。

具体 catalog 文件格式、目录布局与 GC 引用关系属于 M2 设计范围，不在 M0 冻结。

## 4. Runtime

### 4.1 定义

Runtime 是持久化环境，不等于 Process：

```text
Runtime:
  name
  image reference
  runtime home
  persistent plugin state
```

- `name`：Runtime 的身份，在 Ingot Home 内唯一；
- `image reference`：当前绑定的 Image（`name:version` + digest）；
- `runtime home`：该 Runtime 的持久化根目录（见第 5 节）；
- `persistent plugin state`：位于 runtime home 内、按 Plugin scope 划分的状态。

### 4.2 一个 Image，多个 Runtime

同一个 Image 可以创建多个 Runtime：

```text
coding-agent:1.4.0

Runtime work
Runtime personal
Runtime test
```

冻结语义：

- 三个 Runtime 共享同一 Image 的 artifact（二进制不复制语义）；
- 三个 Runtime 的 state 完全隔离，互相不可见；
- 一个 Runtime 的 Plugin state 迁移、损坏或删除不影响其它 Runtime；
- Runtime 不继承任何隐式全局状态。

### 4.3 Runtime 与 Process

| 对象 | 生命周期 | 是否持久 |
|---|---|---|
| Runtime | 直到显式删除 | 是 |
| Process | 一次执行 | 否 |

冻结：

- Runtime 可以在没有 Process 运行时存在（已创建、已停止）；
- Process 消失不改变 Runtime 的 Image 绑定与 state；
- v1 一个 Runtime 同时只允许一个 writer Process（避免 Plugin state 并发访问问题）；同 Runtime 多实例留待 Storage Contract 明确后再考虑。

### 4.4 Runtime 记录（草案）

M0 只冻结「Runtime 必须有可持久化的身份与 Image 绑定」，记录字段本身由 M2 定稿。方向性草案：

```text
name
image:
  name
  version
  digest
runtime_home
created_at
```

`Process` 记录单独维护，不写入 Runtime 记录。

### 4.5 Upgrade 与 Rollback 边界

冻结（细节在 M2）：

- Upgrade = 只切换 Image 引用，不复制 state；Plugin 自己负责 state/config migration；
- Rollback = 只切换 Image 引用，Runtime Home 保持不变；
- 因此 Plugin 的 state migration policy 必须声明兼容边界（reader window），这是 Plugin 的责任，Host 只负责把最旧/最新 schema 信息传递下去。

## 5. Runtime Home

### 5.1 解析规则

Standalone Runtime 默认规则：

```text
<executable>
<executable>.home/
```

例如：

```text
coding-agent
coding-agent.home/
```

解析顺序冻结为：

```text
if INGOT_RUNTIME_HOME is set and non-empty:
    use INGOT_RUNTIME_HOME
else:
    use <absolute executable path>.home
```

约束：

- 路径必须解析为绝对路径；
- `<absolute executable path>` 指实际执行文件的绝对路径，不是当前工作目录，不是 PATH 查找结果；
- 两个不同文件名的相同 binary 默认产生两个隔离的 Runtime Home；
- 同一 binary 的多次启动必须复用同一个 Runtime Home；
- 首次运行自动创建所需目录。

### 5.2 目录布局

最低 contract 只需要：

```text
<runtime home>/
  state/
```

推荐布局：

```text
<runtime home>/
  state/      # Plugin-owned persistent storage（必需）
  run/        # IPC / process information（按需生成）
  logs/       # optional logs（按需生成）
```

Runtime Home 的定位：

```text
Plugin-owned persistent storage
Runtime-local metadata
IPC / process information
optional logs
```

冻结：

- `state/` 是唯一必需目录，必须可自动创建；
- 其余目录按需要生成，缺失不等于错误；
- Host 不解释 `state/` 内的内容。

### 5.3 Plugin State Scope

每个 Plugin 获得独立 Scope：

```text
<runtime home>/
  state/
    <plugin-scope>/
```

冻结：

- `<plugin-scope>` 与 Plugin identity 一一对应；
- Host 不理解 Plugin 在其中存什么；允许 config、sqlite、cache、credentials、indexes 等任意内容；
- Plugin 不得读写其它 Plugin 的 scope；
- Plugin 不得假设 scope 目录之外的任何路径存在。

#### 5.3.1 `<plugin-scope>` 拼写（M0 已定）

采用 **manifest 短名**：

```text
<runtime home>/state/tool.shell/
<runtime home>/state/model.openai-compatible/
<runtime home>/state/app.backend/
```

冻结：

- `<plugin-scope>` = `ingot.plugin.toml` 的 `name`（已由 `validateShortName` 约束为 `[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*`，1–64 字节，见 `internal/builder/manifest.go`）；
- 短名在同一个 Ingot Home 内唯一（lock 校验保证，`internal/builder/lock.go` 的 `INGOT-LOCK-DUPLICATE-PLUGIN`），因此不会撞名；
- 不使用 module path：module path 会引入嵌套目录（`state/github.com/ingot-agent/tool-shell/`），可读性差且在某些平台上更长；
- 该拼写一旦写入用户磁盘即为持久化格式，后续不得随意更改。

注意：现状 generated wiring 用的是 Plugin ID（module path，`internal/builder/generate.go:407`），与本决策不同，M1 需要改动并处理迁移（见《现状差异与迁移影响》）。

### 5.4 首次初始化输出

首次初始化时允许输出一次（stderr/stdout 二者择一，不重复输出）：

```text
Runtime home initialized at ...
Set INGOT_RUNTIME_HOME to use another location.
Add the executable directory to PATH to run it from anywhere.
```

冻结：

- 只在首次创建时输出，后续启动不重复；
- 输出不得包含 secret 或 state 内容；
- 初始化失败必须是显式错误，不得静默降级到临时目录。

### 5.5 Standalone 与 Managed 使用同一 contract

冻结：

- Ingot managed Runtime 的目录本身**就是** Runtime Home（M2 中 `~/.ingot/runtimes/<name>/`）；
- standalone binary 与 managed Runtime 使用同一套 Runtime Home 解析与布局 contract；
- Runtime binary 不再依赖完整 Ingot Home。

### 5.6 与现有环境变量的关系（M0 已定）

现状：generated main 读取 `INGOT_HOME`、`INGOT_CONFIG`、`INGOT_STATE_ROOT`。M0 定稿：

- `INGOT_RUNTIME_HOME` 是唯一的 Runtime Home 覆盖入口；
- `INGOT_STATE_ROOT` **直接移除**，不保留废弃别名（项目尚未发布，无存量用户）；
- `INGOT_CONFIG` 随统一 Runtime 配置一并移除；
- `INGOT_HOME` 不再属于 Runtime contract：Ingot Home 只服务 Builder 与 CLI，Runtime 不读取它。

因此 Runtime 侧的环境变量集合收敛为：

```text
INGOT_RUNTIME_HOME   # 可选，覆盖 Runtime Home 位置
```

移除是**破坏性**的，实施时必须同步处理三处内部注入点：

| 位置 | 现状 | M1 处理 |
|---|---|---|
| `internal/builder/generate.go:307-319` | 读取三个变量 | 只读 `INGOT_RUNTIME_HOME` |
| `internal/home/home.go:RunCurrent()` | 注入 `INGOT_HOME` | 不再注入（或改为注入 `INGOT_RUNTIME_HOME`） |
| `internal/builder/build.go:211-217` | 用 `INGOT_STATE_ROOT` 指向临时目录做 pre-switch check | 改为注入临时 Runtime Home |

## 6. Plugin Configuration

### 6.1 定义

正式确认：

```text
Plugin Configuration
  = Plugin-owned persistent state
  + Operation-based management surface
```

Plugin 自己负责：

```text
schema / input definition
validation
persistence
migration
secret handling
live reload semantics
```

Ingot 不再维护统一 Runtime `config.toml`。

### 6.2 移除统一 Runtime Config

Generated Runtime 不再执行：

```text
读取统一 config.toml
↓
decode 所有 Plugin Config
↓
Plugin.New(Config)
```

新的启动模型冻结为：

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

#### 6.2.1 Config 加载来源（M0 决策）

`New` 保留 Config 参数，但参数类型不再是「Host 解码好的 TOML 结构」：

```text
New(ctx, config Source, deps Dependencies) (Exports, ingotabi.Cleanup, error)
```

冻结语义：

- `Source` 是一个 **Config 加载来源 Contract**（候选名：`ingotabi/config.Source`），由 generated runtime 注入；
- Host 不解析 Plugin Config 内容，只提供「读取 + 定位」能力；
- Plugin 自己完成 schema、validation、persistence、migration、secret handling；
- Plugin Config 的持久化位置**就是自己的 Plugin State Scope**（`<runtime home>/state/<plugin-scope>/`），不额外引入第二个位置；
- `Source` 至少需要能表达：读取当前持久化配置、判断 `Unconfigured`、写入/更新（供 Configuration Operation 使用）；
- `Source` 的具体接口形状在 M1 定稿，M0 只冻结「参数保留 + 类型换成来源 + Host 不解释内容」三点。

理由与约束：

- 保留参数可以避免 `Dependencies` 变成「什么都塞进去」的杂物袋，Config 与 capability 依赖在类型上保持分离；
- Config 与 State 同址，因此 Plugin 不需要同时处理两个持久化位置；
- `Source` 必须允许在 `Unconfigured` 下返回零值而不报错，否则与 6.3 冲突。

实现约束（来自当前 Builder 语义）：generated wiring 只能为 `Dependencies`/`Exports` 中的**精确 host interface** 注入 host 值（`internal/builder/graph.go:292-295`），且 host interface 不允许出现在集合类型里（`graph.go:297-300`）。因此 `Source` 必须是顶层精确接口类型，不能是 `Optional[Source]` 或 `[]Source`。

#### 6.2.2 不再需要 `config_package`

`ingot.plugin.toml` 的 `config_package` 原本用于定位「Host 要解码的 Config 类型所在 package」（`internal/builder/manifest.go:24`、`internal/builder/resolve.go:283/292`）。Config 改为来源注入后，Host 不再需要该类型，因此：

- `config_package` 标记为 **deprecated**，不再参与 `New` 的生成；
- 为保持 manifest 文件稳定，M0 不立即删除该字段，也不因此提升 `manifest_version`；
- 真正删除字段与提升版本留到后续 manifest 演进（M3 拆仓时一并处理）。

### 6.3 Unconfigured 状态

冻结：

- Plugin 必须允许处于 `Unconfigured` 状态；
- 未配置不能导致 Plugin 完全无法创建，否则用户无法通过 Operation 完成初次 Setup；
- `Unconfigured` 不等于「使用了错误的默认值」：Plugin 必须能区分「未配置」与「已配置且值为默认」；
- Plugin 在 `Unconfigured` 状态下的行为必须可预期（例如 Operation 返回明确错误、或按文档化的只读降级），不得 panic、不得静默使用空凭据发起外部请求。

#### 6.3.1 pre-switch check 在 Unconfigured 下通过（M0 已定）

Builder 的 pre-switch check（`--ingot-check`，见 `internal/builder/build.go` 与 `internal/builder/generate.go`）在 Plugin 未配置时**必须通过**。

冻结语义：

- check 判定标准是「构造成功」：`New` 在无持久化配置的情况下不得返回错误、不得 panic、不得占用外部资源；
- check 不要求 Plugin 已完成配置，也不校验配置内容；
- Plugin 不得为了通过 check 而伪造默认配置并持久化（check 期间写入的 state 属于临时 Runtime Home，不得污染真实 state）；
- 若 Plugin 在未配置时无法构造，那是 Plugin 契约缺陷，不是 check 的问题。

### 6.4 Configuration Operation convention

需要定义统一的 Configuration / Setup Operation convention，避免每个插件完全自由发挥。M0 冻结以下**契约要素**：

| 要素 | 冻结内容 |
|---|---|
| 命名空间 | 使用稳定小写 ASCII 身份，符合现有 `operation.Definition.Name` 语法 `^[a-z][a-z0-9]*([._-][a-z0-9]+)*$` |
| 推荐名称 | `config.get` / `config.status` / `config.update`，最终命名见 6.5 决策点 |
| 输入定义 | 通过 `operation.Definition.InputSchema`（JSON Schema Draft 2020-12，描述 JSON object）为 Generic UI 提供结构 |
| 输出定义 | 通过 `OutputSchema` 描述；成功输出必须是符合 schema 的 JSON object |
| 持久化 | 由 Plugin 自己完成，写入自己的 state scope |
| 生效语义 | Plugin 自己决定修改是否立即生效 |
| 重启需求 | Plugin 通过输出中的 `restart_required: bool`（可选附带原因文本）表达需要重启才能生效 |
| secret | secret 不应通过普通读取 Operation 明文返回；读取只返回「已设置/未设置」，写入照常 |

冻结的语义要求：

1. `get` 与 `status` 必须是只读、幂等、可并发调用；
2. `update` 必须是原子的：失败时 Plugin 的持久化状态不变；
3. 每个 Configuration Operation 的 Definition 必须在 Plugin 创建后即可用（不依赖已配置）；
4. Operation 的调用入口不属于本文件（由 M1 Runtime Management Channel 提供）；
5. secret 的判定由 Plugin 自己负责，Host 不解释字段语义。

### 6.5 Configuration Operation 命名（M0 已定）

Roadmap 明确「具体 naming 可以单独设计」。M0 定稿如下：

| 决策点 | 结论 |
|---|---|
| 命名空间 | `config.*`；`setup.*` 保留给未来多步引导流程 |
| 读取 | `config.get`（返回当前值，secret 脱敏为「已设置/未设置」）与 `config.status`（返回 `configured` / `restart_required` / `schema_version`）都要 |
| 写入 | 单一 `config.update`；整块或部分更新由 InputSchema 表达 |
| 重启标记 | 输出字段 `restart_required: bool`，可附带原因文本 |

### 6.6 存量统一 config 不迁移（M0 已定）

移除统一 `config.toml` 后，**不提供自动迁移**，也不提供迁移命令。用户需要在新模型下重新配置 Plugin。

冻结：

- 不读取、不解析、不转换旧的 `<ingot home>/config.toml`；
- 不提供 `config migrate` 之类的迁移入口（避免 Host 重新理解各 Plugin 的旧格式，那正是本次要移除的耦合）；
- 旧的 `config.toml` 文件可以留在磁盘上不被读取，也可以由用户自行删除；Host 不对其做任何处理；
- `ingot init` 不再生成 `config.toml` 模板（现状的 `renderConfigTOML`，`internal/home/init.go:192`），改为输出「Runtime Home 已就绪 + 如何配置 Plugin」的指引；
- Plugin 首次配置的入口是 Configuration Operation（6.4），不是编辑文件。

采用该决策的前提是项目尚未发布、无存量用户（见 5.6 同一前提）。

## 7. Collection

### 7.1 定义

> Collection 是对 Direct Plugin Set 的声明式 Composition Recipe。

它描述「一组经过验证的 Plugin 应该如何进入 `plugins.toml`」，不描述 Runtime 行为。

### 7.2 边界（冻结）

```text
Collection 不进入 Runtime
Collection 不进入 Component Graph
Plugin 不依赖 Collection
Collection v1 不嵌套 Collection
Collection 不负责 Runtime Config
```

推论：

- Apply 后真正的 desired state 仍然只是 Plugin Set；
- Collection 自身不会永久留在 Runtime graph 中；
- Collection 不引入新的运行时概念，不改变 Capability 解析。

### 7.3 Collection 文件 v1

声明内容冻结为四类：

```text
collection identity
collection version
metadata
ordered exact plugin references
```

冻结规则：

- `identity` 与 `version` 必填，语法同 Image 的 `name`/`version`；
- Plugin reference 必须是 **exact**（精确 module + 精确 version，或明确的 local dev 引用），不接受版本区间；
- reference 的**顺序是语义**：它表达期望的 Direct Plugin Order；
- v1 不支持 `Collection -> Collection`；
- `metadata` 只服务展示与策展，不参与解析语义。

具体文件格式（TOML schema、字段名、校验规则）在 M4 定稿；M0 只冻结上述语义。

### 7.4 Planner

真正核心不是 Parser，而是 Collection Planner：

```text
输入：
  Collection
  +
  Current Direct Plugin Set

输出：
  CollectionPlan
```

Plan 至少区分：

```text
Add
Satisfied
VersionConflict
SourceConflict
OrderConflict
```

冻结：

- Planner 是纯函数式语义（同样输入产生同样 Plan）；
- Planner 不修改任何状态；
- Plan 必须能完整解释每个 Plugin 的去向与每个冲突的原因。

### 7.5 Merge Semantics（冻结）

原则：

- 已有 Plugin 不自动升级；
- 已有 Plugin 不自动降级；
- Local Dev 不被 Collection 静默替换；
- 不静默改变现有 Direct Plugin Order；
- Collection 内 order 尽量通过插入新 Plugin 满足；
- 如果现有顺序与 Collection 要求冲突，则显式报 `OrderConflict`。

补充：`Satisfied` 表示已存在 Plugin 满足 Collection 的 exact 要求（module + version + source 一致），不表示「版本兼容即可」。

### 7.6 Apply

```text
collection parse
↓
plan
↓
candidate plugins.toml
↓
preflight resolve
↓
success
↓
atomic commit plugins.toml + plugins.lock
```

失败语义冻结：

```text
nothing changed
```

即 parse、plan、preflight 任一步失败，`plugins.toml` 与 `plugins.lock` 都不变。

### 7.7 Receipt

可以保存非权威 provenance：

```text
collection
version
digest
applied plugin snapshot
resulting plugins digest
```

冻结：

- Builder 不读取 receipt；
- receipt 只用于未来 `Modified` / `Diff` / `Reapply` 等 UX；
- receipt 缺失或被修改不得影响构建正确性。

### 7.8 CLI v1

只做：

```text
ingot collection inspect
ingot collection plan
ingot collection apply
```

第一版不做：

```text
collection remove
collection update
```

理由：避免用户误认为 Collection 是长期 dependency owner。

## 8. 术语表（统一用词）

| 术语 | 含义 | 不要混用 |
|---|---|---|
| Plugin | 最小能力与发行单元 | 不是 Component |
| Component | 构造与生命周期节点 | 不是 Plugin |
| Capability | 有类型的组件间 Contract | 不是 Operation |
| Operation | 对外可调用的 request-response 能力 | 不是 Capability |
| Image | named + versioned + content-addressed 不可变产物 | 不是 Runtime |
| Image Reference | `name` / `name:version` / `name@digest` | 不是 alias |
| Runtime | 持久化执行环境 | 不是 Process |
| Process | Runtime 的一次执行 | 不是 Runtime |
| Runtime Home | Runtime 拥有的持久化根目录 | 不是 Ingot Home |
| Plugin Scope | `state/<plugin-scope>/` | 不是 Ingot Home state |
| Collection | Direct Plugin Set 的声明式 Recipe | 不是 Image |
| Direct Plugin Set | `plugins.toml` 表达的有序 Plugin 集合 | 不是 Component Graph |
| Receipt | 非权威 provenance 记录 | 不是 lock |

## 9. M0 Done 检查清单

Contract 层面，以下条目全部冻结才算 M0 完成。状态列反映本次 M0 定稿结果。

| # | 条目 | 状态 |
|---|---|---|
| 1 | Image 三要素（name / version / digest）与各自语义冻结 | 已冻结 |
| 2 | Image 命名与版本语法冻结（严格解析，无隐式补齐） | 已冻结 |
| 3 | Image Reference 三种形式与可复现引用规则冻结 | 已冻结 |
| 4 | 「正式 version 不可重指向不同 digest」规则冻结 | 已冻结 |
| 5 | alias 与 version 的严格区分冻结（alias 不进入权威字段） | 已冻结 |
| 6 | Runtime 定义、字段边界与身份规则冻结 | 已冻结 |
| 7 | 同一 Image 多 Runtime 的隔离语义冻结 | 已冻结 |
| 8 | Runtime ≠ Process、v1 单 writer 规则冻结 | 已冻结 |
| 9 | Runtime Home 解析优先级（`INGOT_RUNTIME_HOME` → `<exe>.home`）冻结 | 已冻结 |
| 10 | Runtime Home 最低目录 contract（`state/` 必需，`run/` `logs/` 按需）冻结 | 已冻结 |
| 11 | Plugin State Scope 的一一对应与所有权边界冻结 | 已冻结 |
| 12 | `<plugin-scope>` 拼写定稿（manifest 短名） | 已定（5.3.1） |
| 13 | 首次初始化输出语义冻结 | 已冻结 |
| 14 | standalone 与 managed 共用同一 Runtime Home contract 冻结 | 已冻结 |
| 15 | 旧环境变量处置定稿（直接移除 `INGOT_STATE_ROOT` / `INGOT_CONFIG`） | 已定（5.6） |
| 16 | Plugin Configuration 定义（state + Operation management surface）冻结 | 已冻结 |
| 17 | 统一 Runtime `config.toml` 移除方向冻结 | 已冻结 |
| 18 | Config 参数保留 + 类型换成来源 Contract | 已定（6.2.1） |
| 19 | `config_package` deprecated 处置 | 已定（6.2.2） |
| 20 | Unconfigured 状态语义冻结 | 已冻结 |
| 21 | pre-switch check 在 Unconfigured 下通过 | 已定（6.3.1） |
| 22 | Configuration Operation convention 要素冻结（含 secret 与 restart_required） | 已冻结 |
| 23 | Configuration Operation 命名定稿（6.5） | 已定 |
| 24 | 存量统一 config 不迁移 | 已定（6.6） |
| 25 | Collection 定义与 5 条边界冻结 | 已冻结 |
| 26 | Collection 文件 v1 语义冻结（exact reference + 顺序语义） | 已冻结 |
| 27 | Planner 输入输出与 5 类结果冻结 | 已冻结 |
| 28 | Merge 语义 6 条原则冻结 | 已冻结 |
| 29 | Apply 原子性与失败「nothing changed」冻结 | 已冻结 |
| 30 | Receipt 非权威性冻结 | 已冻结 |
| 31 | CLI v1 命令集合冻结 | 已冻结 |
| 32 | 术语表统一 | 已冻结 |

M0 无未决项，可以进入 M1。

## 10. 开放问题（明确不在 M0 冻结）

| 问题 | 归属 |
|---|---|
| Image catalog 文件格式与目录布局 | M2 |
| Runtime registry 布局与 Runtime 记录字段 | M2 |
| Process registry 字段与生命周期 API | M2 |
| Image GC 引用关系计算 | M2 |
| Runtime Management Channel 的传输与协议 | M1（建议同时落地） |
| Config `Source` Contract 的具体接口形状 | M1（M0 已冻结语义，见 6.2.1） |
| Plugin state migration policy 的兼容边界表达 | M2/M8 |
| Collection 文件 TOML schema | M4 |
| Source Resolver 接口与分发形态 | M3/M7 |
| Plugin trust 等级与 Registry 字段 | M7 |
| Plugin state 的 Storage Contract（并发访问） | M2 之后 |

## 11. M0 决策记录（按时间顺序）

| 编号 | 决策 | 结论 | 位置 |
|---|---|---|---|
| D1 | 旧 Runtime 环境变量退场方式 | 直接移除 `INGOT_STATE_ROOT` / `INGOT_CONFIG`，只保留 `INGOT_RUNTIME_HOME` | 5.6 |
| D2 | Plugin 如何获得配置 | 保留 `New` 的 Config 参数，类型换成注入的 Config 来源 Contract | 6.2.1 |
| D3 | `<plugin-scope>` 拼写 | manifest 短名 | 5.3.1 |
| D4 | 存量统一 `config.toml` | 不迁移、不提供迁移命令 | 6.6 |
| D5 | 未配置时 pre-switch check | 通过 | 6.3.1 |
| D6 | Configuration Operation 命名 | `config.get` / `config.status` / `config.update` | 6.5 |
| D7 | secret 读取语义 | 只返回「已设置/未设置」 | 6.4 |
| D8 | `restart_required` 表达 | 输出布尔字段 | 6.4 |
| D9 | `config_package` 去留 | 标记 deprecated，不立即删除、不升 manifest_version | 6.2.2 |
