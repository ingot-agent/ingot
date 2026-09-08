# ingot M0 现状差异与迁移影响

> 状态：Frozen（对应 M0 决策 D1–D9）
> 配套：`./ingot_M0_架构冻结_设计方案.md`
> 用途：把 M0 冻结的 Contract 与当前代码实现逐条对齐，作为 M1 / M2 的实施输入。
> 说明：本文件只描述差异与影响，不提出未定稿的新设计。所有「现状」均来自当前代码，已标注位置。

## 1. 结论摘要

M0 冻结的 5 个对象中，**没有任何一个在现有代码中完整存在**：

| 对象 | 现状 | 差距等级 |
|---|---|---|
| Image name/version | 不存在，Image 只有 `sha256:` digest | 大 |
| Runtime | 不存在，只有单 `current` 指针 | 大 |
| Runtime Home | 不存在，Runtime 依赖 `INGOT_HOME` + `config.toml` + `INGOT_STATE_ROOT` | 大 |
| Plugin-owned Configuration | 与目标相反：Host 统一 decode 所有 Plugin Config | 大（方向性反转） |
| Collection | 不存在 | 全新 |

其中 **Plugin Configuration 是唯一方向性反转**：目标要求 Plugin 自己加载状态，现状要求 Runtime 启动时先统一 decode 所有 Plugin Config，且**缺失 config table 直接报错**，与「Unconfigured Plugin 必须能创建」直接冲突。这是 M1 的第一优先项。

## 2. Image

### 2.1 现状

- Image 身份只有内容寻址 digest：`internal/builder/lock.go:625` 的 `Lock.ImageID()` 由 `CanonicalBuildManifest()` 派生，格式 `sha256:<64 hex>`。
- `internal/builder/build.go:38` 的 `ImageManifest` 字段为 `schema_version` / `image_id` / `artifact_digest` / `build_manifest` / `direct_plugins` / `component_creation_order` / `many_order` / `host_dependencies`；**没有 name，没有 version**。
- 镜像目录名由 `internal/layout/layout.go` 的 `ImageDirectoryName(imageID, goos)` 决定，即 `<home>/images/sha256:<hex>/`（Windows 用 `sha256-<hex>`）。
- 没有 Image catalog，没有 `name → version → digest` 映射，也没有 alias 概念。
- 可用的「版本感」只存在于 `plugins.lock` 的 Plugin module version，以及 manifest 的 `name` 短名（如 `app.cli`），二者都不是 Image 身份。

### 2.2 与 M0 的差异

| M0 要求 | 现状 | 差异 |
|---|---|---|
| Image 有 `name` | 无 | 需新增字段与 catalog |
| Image 有 `version` | 无 | 需新增字段与语义 |
| Image 有 `digest` | 有 | 一致，保留 |
| `name:version` 不可重指向不同 digest | 无该概念 | 需新增发布规则与校验 |
| alias 与 version 严格区分 | 无 | 需新增，且必须保证 alias 不进权威字段 |
| Image Reference 三种形式 | 只有 digest | 需新增解析层 |

### 2.3 迁移影响

- `ImageManifest` 需要新增 `name` / `version` 字段（以及 schema version 提升）。**关键约束：name/version 不得参与 artifact digest 计算**，否则改名会换镜像，破坏「同一 `name:version` 跨机器同 digest」。
- `home.Current()` / `switchCurrent()` / `GC()` 都以 digest 为单位工作，M2 需要在它们之上增加 catalog 层，而不是替换 digest 语义。
- `internal/layout.ImageDirectoryName` 的目录命名只接受 `sha256:` 前缀（`validImageID` 在 `internal/home/home.go` 中强制该前缀）。引入 name/version 后需要区分「catalog 记录」与「artifact 目录」两类命名。
- 现有的 `desired digest` / `locked digest` / `artifact digest` 三个 digest 概念已经容易混淆（`home.Status`），引入 Image digest 后必须在文档与 CLI 输出中显式区分：**Plugin set digest ≠ Image ID ≠ artifact digest**。

## 3. Runtime

### 3.1 现状

- 没有 Runtime 概念。`internal/home/home.go` 用单文件指针表达「当前镜像」：`<home>/current`（`CurrentPath()`，第 98 行），回滚依赖 `<home>/current.previous`（第 429、463、600 行）。
- 一个 Ingot Home 在同一时刻只有一个可运行的 Image；`RunCurrent()` 直接执行 `current` 指向的二进制。
- 没有 Runtime 名，没有 Runtime registry，没有 Process registry；`ingot` 没有 `ps` / `stop` / `runtime` 子命令（`internal/cli/cli.go` 的命令集合为 init/resolve/build/apply/status/inspect/rollback/gc/plugin/bundle/check）。
- 并发控制只有 Ingot Home 级别的锁（`home.acquire`），没有 Runtime 级 writer 语义。

### 3.2 与 M0 的差异

| M0 要求 | 现状 | 差异 |
|---|---|---|
| Runtime 有 name | 无 | 需新增 registry |
| Runtime 绑定 image reference | 只有全局 `current` | 需从全局单例改为每 Runtime 绑定 |
| 同一 Image 多 Runtime 且 state 隔离 | 不支持 | 需新增 |
| Runtime ≠ Process | 无 Process 概念 | 需新增 |
| v1 单 writer Process | 无 | 需新增 |

### 3.3 迁移影响

- `current` / `current.previous` 是全局单例模型，M2 必须把它替换为 Runtime registry + 每 Runtime 的 image 绑定。这是**破坏性变更**，需要 CLI 兼容期（`ingot run` 语义、`rollback` 语义都会变化）。
- `home.GC()` 的引用集合当前只包含 `current` 与 `current.previous` 加「保留最近 N 个」，M2 必须改为按 Runtime 引用 + running Process + rollback refs + pinned 计算。
- `RunCurrent()` 目前只注入 `INGOT_HOME`（`replaceEnv(os.Environ(), "INGOT_HOME", home.Root)`），M1 需改为注入 Runtime Home 解析所需信息。

## 4. Runtime Home

### 4.1 现状

- 没有 Runtime Home。Runtime 的环境输入全部来自环境变量，由 generated main 解析（`internal/builder/generate.go:307–319`）：
  - `INGOT_HOME`，默认 `~/.ingot`；
  - `INGOT_CONFIG`，默认 `<home>/config.toml`；
  - `INGOT_STATE_ROOT`，默认 `<home>/state`。
- state scope 已经按 Plugin 分目录：`generate.go:407` 生成 `stateDirN := filepath.Join(stateRoot, "<plugin.ID>")`，并 `os.MkdirAll(stateDirN, 0o700)`；注入的是 `ingotabi/state.Scope`。
- 构建期 pre-switch check 会用临时 state 目录覆盖：`internal/builder/build.go:211–217`（`os.MkdirTemp(staging, "check-state-")` + `INGOT_STATE_ROOT`）。
- 没有任何 `INGOT_RUNTIME_HOME` 或 `<exe>.home` 逻辑（全仓库 grep 无结果）。
- Runtime 二进制无法 standalone：不设置 `INGOT_HOME` / `INGOT_CONFIG` 时，它仍然要求 `~/.ingot/config.toml` 存在（否则启动即失败），因此「下载 binary、chmod +x、直接跑」目前不成立。

### 4.2 与 M0 的差异

| M0 要求 | 现状 | 差异 |
|---|---|---|
| `INGOT_RUNTIME_HOME` 覆盖 | 无 | 需新增 |
| 默认 `<absolute exe path>.home` | 无（依赖 `INGOT_HOME`） | 需新增 |
| `state/` 必需并自动创建 | 有（但根目录来自 `INGOT_STATE_ROOT`/`INGOT_HOME`） | 语义对齐、来源改变 |
| `run/` `logs/` 按需生成 | 无 | 需新增 |
| 不依赖完整 Ingot Home | 不满足 | 需新增 |
| 首次初始化输出 | 无 | 需新增 |

### 4.3 迁移影响

- `INGOT_STATE_ROOT` 与 `INGOT_RUNTIME_HOME` 是**同一职责的两个名字**。M0 已定：**直接移除** `INGOT_STATE_ROOT`（项目尚未发布，无存量用户），不做废弃别名。
- `INGOT_HOME` 在 Runtime 侧的用途必须收缩。注意 `internal/home/home.go:RunCurrent()` 与 `internal/builder/build.go:217` 都在注入它；M1 需明确 Ingot Home 只服务 Builder/CLI，不进入 Runtime contract。
- `build.go` 的 pre-switch check 目前用 `INGOT_STATE_ROOT` 指向临时目录。改为 Runtime Home 后，check 需要等价的「临时 Runtime Home」注入方式，否则 check 会写进真实 state。
- `<exe>.home` 依赖 `os.Executable()`；注意符号链接与 PATH 查找语义（M0 冻结为「实际执行文件的绝对路径」）。`internal/layout` 已承担平台命名差异，适合放解析逻辑的公共部分。

## 5. Plugin Configuration

### 5.1 现状

- **方向与 M0 相反**：Runtime 启动时统一读取一个 TOML 文件并 decode 所有 Plugin Config。
  - `generate.go:340` 的 `writeRuntimeConfig` 生成 `decodedConfigs` 结构与 `decodeConfigs(path)`；
  - `generate.go:395` 的 `run()` 第一步就是 `decodeConfigs(configPath)`，失败即整个 Runtime 启动失败；
  - `generate.go:360` 的 `resolveConfigTable` 在 Plugin 缺少 config table 时返回 `plugin %s (%s): missing config table`；
  - `decodeConfigs` 使用 `DisallowUnknownFields`，未知 key 直接报错。
- Config 是 `New(ctx, cfg Config, deps Dependencies)` 的第二个参数（15 个官方 Plugin 共 16 个 Component 全部如此：`plugins/app-webui` 的 `host` 与 `app` 两个 Component 共享同一 root `Config`，见 `plugins/app-webui/host/host.go:33`、`plugins/app-webui/app/server.go:64`），因此 **Config 必须在构造前完全确定**。
- 所有官方 Plugin 的 `ingot.plugin.toml` 都是 `config_package = "."`，即 Config 类型来自 Plugin 根 package。
- `internal/home/init.go:192` 的 `renderConfigTOML` 会为 profile 中每个 Plugin 都写一个 `[plugins.<name>]` table（注释明确「Every plugin in the current image needs exactly one [plugins] table」），部分 Plugin 会写入带真实值的样例（如 `model.openai-compatible` 的 `base_url`/`api_key` 模板、`app.backend` 的 `backend.address`）。
- `home.ConfigPath()` 固定为 `<home>/config.toml`；`build.go` 与 `home` 的多处都传 `ConfigPath`。
- 现有 secret 处理只有 `plugins/model-openai-compatible` 的错误信息脱敏（`redactSecret`，`openaicompat.go:451`），没有统一的 secret 读取/存储约定；文档中出现的 `${secret:...}`（`docs/ingot_plugins.toml_v0.1_设计方案.md` 第 8 节）在代码中**没有实现**。
- Plugin 完全没有「Unconfigured」状态：`session.sqlite` 的 `Config struct{}` 是空结构，其它 Plugin 的 Config 都有默认值或必填值，缺失 table 会在 decode 阶段就失败。

### 5.2 与 M0 的差异

| M0 要求 | 现状 | 差异 |
|---|---|---|
| Plugin 自己加载 config/state | Host 统一 decode | 方向性反转 |
| 移除统一 `config.toml` | 强依赖 | 需删除并迁移 |
| Plugin 可处于 Unconfigured | 缺失 table 即启动失败 | 必须改造 |
| Config 参数移除（D2） | 参数是 Host 解码的 TOML 结构 | 需移除参数 + 改 16 个 Component |
| Configuration Operation convention | 无 | 全新 |
| secret 不经普通读 Operation 明文返回 | 无约定 | 全新 |
| `restart_required` | 无 | 全新 |

### 5.3 迁移影响（M1 核心）

1. **generated wiring 必须去掉统一 decode。** `writeRuntimeConfig` / `decodeConfigs` / `resolveConfigTable` / `strictDecodeConfig` 全部需要移除（M0 决策 D2）。
2. **`New` 签名改造是最大破坏点。** M0 已定移除 Config 参数（D2）：`New(ctx, cfg Config, deps Dependencies)` → `New(ctx, deps Dependencies)`。影响 15 个官方 Plugin / 16 个 Component，是 M1 的主要工作量。不新增 ABI 类型：配置读写走已有的 `deps.State state.Scope`。
3. **Host 侧 Config 机制要一并移除。** `ConfigImport`/`ConfigType` 与「根 package 必须有 `Config` struct」校验（`graph.go:37-39, 157-164`）、`RootPackage`（`lock.go`、`resolve.go:292`、`build.go:328`）、manifest `config_package`（`manifest.go:24,78`）都不再被 Runtime 使用；字段删除与版本提升由 M1 按格式兼容性决定。
3. **存量 `config.toml` 不迁移（D4）。** 不读取、不解析、不转换，也不提供迁移命令；用户在新模型下通过 Configuration Operation 重新配置。`ingot init` 不再生成 config 模板。
4. **`ingot init` 的 config 模板需要删除。** `renderConfigTOML`（`internal/home/init.go:192`）生成的模板（含 `api_key` 等）不再适用，取而代之应是「Runtime Home 初始化 + Plugin setup 指引」。
5. **pre-switch check 在未配置下必须通过（D5）。** 现在 check 依赖 `config.toml` 可 decode；去掉后，check 判定标准变为「`New` 在无持久化配置下构造成功」，要求 Plugin 的 `New` 在无 state 时安全。
6. **Operation 基础设施已存在但未用于 config。** `sdk/operation/operation.go` 已定义 `Definition`/`Request`/`Result` 与 JSON Schema 约定，`plugins/app-webui/app/server.go` 的 `Dependencies.Operations []operation.Operation` 已能收集并暴露 Operation。Configuration Operation 可以复用这套 contract；缺口是「不依赖 app.backend 的调用入口」（即 M1 的 Runtime Management Channel）。
7. **`state.Scope` 覆盖面不足。** 当前只有 `asset.local` 与 `session.sqlite` 声明了 `State state.Scope` 依赖。移除 Config 参数后（D2），**其余 13 个 Plugin 需要补上 `State state.Scope`** 才能持久化自己的配置，这是 M1 中除签名改造外的第二项必做工作。

## 6. Collection

### 6.1 现状

- 完全没有 Collection：没有 parser、没有 planner、没有文件格式、CLI 无 `collection` 子命令。
- 最接近的现有概念是 `internal/bundle` 的 **Profile**（`Profile` 结构 + `profiles` 表 + `LookupProfile`，`internal/bundle/bundle.go:32–106`），它也是「一组官方 Plugin」，但：
  - Profile 编译进 binary、只在 `ingot init` 时使用；
  - Profile 与 bundled plugin 分发耦合（`BundledDirectory = "bundled-plugins"`）；
  - Profile 不是用户可发布的 artifact，没有 identity/version/digest；
  - Profile 不参与 merge/plan，只是初始写入。
- `plugins.toml` 的顺序语义已存在且是权威的（`builder.DesiredPlugins` 有序，`DesiredPlugin` 只有 `module`/`version`/`path`）。

### 6.2 与 M0 的差异

| M0 要求 | 现状 | 差异 |
|---|---|---|
| Collection 文件（identity/version/metadata/exact refs） | 无 | 全新 |
| Planner（5 类结果） | 无 | 全新 |
| Merge 语义 | 无 | 全新 |
| Apply 原子提交 | 无 | 全新（但 `plugins.toml`+`plugins.lock` 已有成对事务机制可复用） |
| Receipt | 无 | 全新 |
| `ingot collection inspect/plan/apply` | 无 | 全新 |

### 6.3 迁移影响

- Profile 与 Collection 的职责必须明确区分：Profile 是「官方默认初始集」，Collection 是「用户可应用/可发布的 Recipe」。M4 不应把 Profile 直接改造成 Collection，否则会把「编译进 binary 的默认值」与「可发布的社区策展」混在一起。
- Apply 的原子性可以复用现有事务机制：`home.commitPair(desired, lock)`（`internal/home/home.go:331`）与 `recoverTransaction`（第 356 行）已经是 `plugins.toml` + `plugins.lock` 的成对原子写入，并带崩溃恢复。
- Planner 需要读取「Current Direct Plugin Set」，其权威来源是 `plugins.toml`（`builder.DesiredPlugins`），不需要读 `plugins.lock`；但判断 source 冲突需要 lock 中的 `SourceKind`（`remote`/`dev`）信息。

## 7. 跨仓库影响

| 仓库 | M0 冻结带来的影响 |
|---|---|
| `ingot-core` | 最大：generated wiring、home 模型、CLI、builder manifest 都要改 |
| `ingot-abi` | **无需改动**：`state.Scope` 已足够承载 Plugin 配置持久化（M0 决策 D2 不新增 ABI 类型） |
| `sdk` | `operation` 已可用于 Configuration Operation；可能需要新增 config/setup 相关通用类型 |
| `plugins/*`（在 core 内） | `New` 签名改造（移除 Config 参数）+ 补 `state.Scope` 依赖，影响全部 15 个官方 Plugin |

`ingot-abi` 的 `state.Scope` 注释已经写明「Scope carries only the persistent location: file access, schema migration and any business persistence remain the Plugin's responsibility」，与 M0 第 5、6 节一致，无需反转，也无需新增 Config 相关 Contract。

## 8. M1 建议实施顺序（差异驱动）

按依赖关系，建议 M1 内部顺序为：

```text
1. Runtime Home 解析（INGOT_RUNTIME_HOME / <exe>.home）+ 目录创建 + 首次输出
      ↓
2. generated wiring 移除统一 config.toml decode 与 Config 注入
      ↓
3. Builder 移除 ConfigImport / ConfigType / RootPackage 依赖
      ↓
4. Plugin 改造（移除 New 的 Config 参数 + 补 state.Scope 依赖 + 自行读写配置）
      ↓
5. Unconfigured 语义（含 pre-switch check 在未配置下可构造）
      ↓
6. Configuration Operation convention 落地（复用 sdk/operation）
      ↓
7. Runtime Management Channel（list/invoke/status/shutdown）
      ↓
8. standalone 验收（download → chmod +x → ./agent）
```

第 2–4 步是破坏性最强的，建议先出迁移方案文档再动代码；第 7 步虽然列在 M1，但它是「Operation 可被调用」的前提，若延期则 M1 的验收项「通过 Operation 配置 Plugin」无法闭环。

## 9. M0 决策（已确认）

| 编号 | 问题 | 决策 |
|---|---|---|
| D1 | 旧 Runtime 环境变量退场方式 | 直接移除 `INGOT_STATE_ROOT` / `INGOT_CONFIG`，只保留 `INGOT_RUNTIME_HOME` |
| D2 | Plugin 如何获得配置 | 移除 `New` 的 Config 参数，配置持久化走 `deps.State`（`state.Scope`），不新增 ABI 类型 |
| D3 | `<plugin-scope>` 拼写 | manifest 短名 |
| D4 | 存量统一 `config.toml` | 不迁移、不提供迁移命令 |
| D5 | 未配置时 pre-switch check | 通过 |

详见《M0 架构冻结设计方案》第 11 节决策记录。
