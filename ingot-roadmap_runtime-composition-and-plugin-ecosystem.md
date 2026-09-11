# Ingot 下一阶段 Roadmap

## 总体方向

下一阶段不再继续扩展 Agent Core 本身，而是把 Ingot 从“可组合 Agent Runtime Builder”进一步推进成：

**Runtime Platform + Composition Manager + Plugin Ecosystem**

主线围绕四个核心对象展开：

```text
Plugin
  最小能力与发行单元

Collection
  可复用的 Plugin Composition Recipe

Image
  named + versioned + content-addressed immutable artifact

Runtime
  persistent execution environment
  = Image + isolated Runtime Home
```

Process 是 Runtime 的一次具体执行：

```text
Image
  what to run

Runtime
  where it lives

Process
  one execution
```

后续 Manager、Marketplace、插件配置和多镜像运行都建立在这几个基础对象之上。

---

# M0：Architecture Freeze / 新模型定稿

这一阶段不以功能为目标，而是先把接下来会影响大量代码的基础 Contract 固化下来。

需要形成正式设计文档 / ADR：

### Image

Image 必须具名并显式版本化：

```text
name
version
digest
```

例如：

```text
coding-agent:1.4.0
sha256:...
```

其中：

- `name` 表达产品身份；
- `version` 表达用户可理解的发布版本；
- `digest` 表达真实 immutable artifact identity。

正式版本不可重新指向不同 digest。

未来可以额外支持：

```text
latest
stable
dev
```

这类 mutable alias，但 alias 与 version 必须严格区分。

### Runtime

正式引入 Runtime 概念。

Runtime 是持久化环境，不等于 Process：

```text
Runtime:
  name
  image reference
  runtime home
  persistent plugin state
```

同一个 Image 可以创建多个 Runtime：

```text
coding-agent:1.4.0

Runtime work
Runtime personal
Runtime test
```

状态完全隔离。

### Runtime Home

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

覆盖方式：

```text
INGOT_RUNTIME_HOME
```

Runtime Home 的定位：

```text
Plugin-owned persistent storage
Runtime-local metadata
IPC / process information
optional logs
```

Runtime binary 不再依赖完整 Ingot Home。

### Plugin Configuration

正式确认：

```text
Plugin Configuration
=
Plugin-owned persistent state
+
Operation-based management surface
```

Ingot 不再维护统一 Runtime `config.toml`。

Plugin 自己负责：

```text
schema / input definition
validation
persistence
migration
secret handling
live reload semantics
```

但需要定义统一的 Configuration / Setup Operation convention，避免每个插件完全自由发挥。

### Collection

Collection 定义为：

> 对 Direct Plugin Set 的声明式 Composition Recipe。

明确：

```text
Collection 不进入 Runtime
Collection 不进入 Component Graph
Plugin 不依赖 Collection
Collection v1 不嵌套 Collection
Collection 不负责 Runtime Config
```

Apply 后真正的 desired state 仍然只是 Plugin Set。

---

# M1：Standalone Runtime 与 Plugin-Owned State

这是整个新架构最先应该落地的实际功能。

目标：

> 任意 Image binary 被单独拿出来后，可以直接启动，不需要用户预先构造特定目录结构。

### Runtime Home Resolution

Generated Runtime：

```text
if INGOT_RUNTIME_HOME:
    use override

else:
    use <absolute executable path>.home
```

首次运行自动创建所需目录。

例如：

```text
coding-agent.home/
  state/
  run/
  logs/
```

最低 contract 实际只需要：

```text
state/
```

其他目录按需要生成。

首次初始化时可以输出一次：

```text
Runtime home initialized at ...
Set INGOT_RUNTIME_HOME to use another location.
Add the executable directory to PATH to run it from anywhere.
```

### Plugin State

每个 Plugin 获得独立 Scope：

```text
runtime.home/
  state/
    <plugin-scope>/
```

Host 不理解 Plugin 在其中存什么。

允许：

```text
config
sqlite
cache
credentials
indexes
...
```

### 移除统一 Runtime Config

Generated Runtime 不再：

```text
读取统一 config.toml
↓
decode 所有 Plugin Config
↓
Plugin.New(Config)
```

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

Plugin 必须允许处于：

```text
Unconfigured
```

状态。

未配置不能导致 Plugin 完全无法创建，否则用户无法通过 Operation 完成初次 Setup。

### Configuration Operations

定义一套推荐或标准约定，例如：

```text
config.get
config.update
config.status
```

或者更广义：

```text
setup.*
```

具体 naming 可以单独设计。

关键 contract 是：

- Plugin 自己持久化；
- Operation InputSchema 为 Generic UI 提供结构；
- Plugin 自己决定修改是否立即生效；
- Plugin 可以返回 `restart_required`；
- secret 不应通过普通读取 Operation 明文返回。

### Runtime Management Channel

M1 不实现 Runtime management channel。Standalone Runtime 只处理系统 signal、Plugin
发起的 lifecycle shutdown 和 cleanup，保持 generated binary 轻量。

M2 由 Runtime 外部的 per-process supervisor 提供 metadata 与 graceful shutdown；M5 再在
独立协议中增加 Operation invocation 和 Application health。两者都不依赖 `app-webui`，也
不把管理协议注入 generated Runtime。

### M1 Done

必须能够：

```text
download binary
chmod +x
./agent
```

直接启动。

并且：

- 自动创建 `<binary>.home`；
- Plugin 可以在未配置状态启动；
- 通过 Operation 配置 Plugin；
- 重启后配置仍存在；
- 两个不同文件名的相同 binary 默认产生两个隔离 Runtime Home；
- `INGOT_RUNTIME_HOME` 可以显式覆盖目录。

---

# M2：Named Images、Runtime 与 Multi-Process

完成 M1 后，把现在的单 `current` 模型升级成正式 Image / Runtime 模型。

详细持久格式、CLI、breaking transition、supervisor 与 GC 契约见
[M2 Image / Runtime / Process 设计方案](./docs/ingot_M2_image_runtime_process_设计方案.md)。

### Build Recipe 与 Named Images

项目目录允许存在多套 recipe/lock：

```text
plugins.toml       + plugins.lock
coding-agent.toml  + coding-agent.lock
```

`ingot build` 默认读取当前目录的 `plugins.toml`，也可以通过 `--use` 选择其他 recipe。
Ingot Home 不提供隐式 recipe fallback。

Ingot Home 维护 immutable image store：

```text
~/.ingot/
  images/
    <digest>/
      ingot-runtime
      manifest.json
```

再增加 Docker-like mutable tag catalog：

```text
coding-agent
  1.3.0 -> sha256:A
  1.4.0 -> sha256:B
```

Runtime 创建时将 tag 解析为 concrete digest；后续移动 tag 不影响已有 Runtime 或 Process。

### Runtime Registry

例如：

```text
~/.ingot/
  runtimes/
    work/
      image
      state/
      run/
      logs/

    personal/
      image
      state/
      run/
      logs/
```

这里 Runtime directory 自身就是 Runtime Home。

例如：

```text
work desired image = sha256:A      # resolved from coding-agent:1.4.0
personal desired image = sha256:A
```

两个 Runtime 共享 Image binary，但 state 完全独立。

### Upgrade

Runtime upgrade：

```text
Runtime work
Image 1.4.0
State X
```

变成：

```text
Runtime work
Image 1.5.0
State X
```

不复制 state。

Plugin 自己负责 state/config migration。

### Rollback

Rollback 只切换 Image：

```text
1.5.0 -> 1.4.0
```

Runtime Home 保持不变。

这要求 Plugin state migration policy 后续需要明确兼容边界。

### Process Model

正式增加 supervisor-owned Process registry：

```text
runtime
image digest
process id
supervisor/runtime pid + birth identity
started_at
argv
exit_status
```

Generated Runtime 保持轻量，只负责 Runtime Home、writer lock、signal、Component Graph 和
cleanup。前台 CLI 或 detached `ingot supervise` 负责 process metadata、日志、shutdown
control 与 exit status；M2 不伪造 Application readiness/health。

支持：

```text
ingot runtime run work
ingot runtime run personal
ingot ps
ingot stop ...
```

不同 Runtime 可以同时运行。

v1 建议：

```text
一个 Runtime 同时只允许一个 writer Process
```

避免 Plugin state 并发访问问题。

未来再根据 Storage Contract 考虑同 Runtime 多实例。

### GC

Image GC 必须根据引用关系计算：

```text
all Runtime image refs
managed/orphaned Processes
rollback refs
tagged images
pinned images
```

不再只有全局 `current/current.previous`。

### M2 Done

必须能够：

- 同时运行两个不同 Image；
- 同时运行两个共享同一 Image 的 Runtime；
- Runtime state 完全隔离；
- Image upgrade 不丢 Runtime state；
- rollback 不需要复制 Runtime Home；
- standalone binary 与 managed Runtime 共享 Runtime Home/writer lock/lifecycle contract；
- generated Runtime 不包含 Process metadata、control server 或 supervisor protocol。

---

# M3：Official Plugin 拆仓与 Release Infrastructure

完成 Runtime 基础模型后，把 Plugin 从 Core repo 物理拆出去。

目标不是“一插件一仓库”，而是：

```text
Core development lifecycle
!=
Plugin development lifecycle
```

建议：

```text
ingot-agent/ingot
ingot-agent/sdk
ingot-agent/ingot-abi

ingot-agent/plugins
  tool-shell/
  tool-runtime/
  tool-ask/
  session-sqlite/
  model-openai-compatible/
  ...
```

官方 Plugin 使用 monorepo。

社区 Plugin 可以自由使用独立仓库。

### Official Plugin Release

每个 Plugin 仍然独立：

```text
version
release
compatibility
changelog
```

如果采用 Go nested module：

```text
plugins/tool-shell/go.mod
plugins/tool-runtime/go.mod
```

则 release tooling 自动处理对应 module tag。

### CI

统一 Plugin CI template：

```text
manifest validation
go test
SDK / ABI compatibility
Ingot compatibility
integration smoke build
release verification
```

避免十几个 Plugin 仓库重复维护基础设施。

### Source Resolver Boundary

这一阶段可以先只抽象接口：

```text
Plugin Reference
↓
Source Resolver
↓
Resolved Source
```

v1 仍然只有：

```text
GoModuleResolver
```

继续使用 Go module download / cache。

暂时不自研完整 Plugin downloader。

是否引入：

```text
archive
git snapshot
registry package
```

留到 Marketplace 阶段根据实际需求决定。

### M3 Done

官方 Plugin 可以：

- 不依赖 Core repo 发布；
- 独立升级；
- 独立测试；
- 被当前 Ingot resolver 正常安装；
- Core release 不需要携带所有 Plugin 源码。

---

# M4：Plugin Collections

这是 Plugin Ecosystem 的第一层 Composition abstraction。

### Collection File

声明：

```text
collection identity
collection version
metadata
ordered exact Plugin references
```

v1：

```text
Collection -> Plugins
```

不支持：

```text
Collection -> Collection
```

### Planner

真正核心不是 Parser，而是：

```text
Collection Planner
```

输入：

```text
Collection
+
Current Direct Plugin Set
```

输出：

```text
CollectionPlan
```

至少区分：

```text
Add
Satisfied
VersionConflict
SourceConflict
OrderConflict
```

### Merge Semantics

原则：

- 已有 Plugin 不自动升级；
- 已有 Plugin 不自动降级；
- Local Dev 不被 Collection 静默替换；
- 不静默改变现有 Direct Plugin Order；
- Collection 内 order 尽量通过插入新 Plugin 满足；
- 如果现有顺序与 Collection 要求冲突，则显式报 OrderConflict。

### Apply

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

失败：

```text
nothing changed
```

### Receipt

可以保存非权威 provenance：

```text
collection
version
digest
applied plugin snapshot
resulting plugins digest
```

Builder 不读取 receipt。

它只用于以后：

```text
Modified
Diff
Reapply
```

等 UX。

### CLI v1

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

避免用户误认为 Collection 是长期 dependency owner。

### M4 Done

社区可以发布一个：

```text
Coding Essentials
```

用户一键得到一套经过验证的 Direct Plugin Set，同时仍然可以自由删除、升级和替换其中任何 Plugin。

---

# M5：Ingot Control Plane / Manager Backend

前面几个阶段完成后，再开始 Ingot 自身 UI。

先做 Backend，不先做页面。

Manager 的定位：

> 管理 Images、Runtimes 和 Composition，而不是 Agent 对话。

它不属于任何 Runtime Image。

### Manager API

围绕：

```text
Images
Runtimes
Processes
Plugins
Collections
Composition Drafts
Builds
Runtime Operations
```

提供稳定 API。

### Draft Composition

正式引入：

```text
Current Composition
Draft Composition
```

所有 UI CRUD 都只修改 Draft：

```text
add plugin
remove plugin
update version
reorder
apply collection
```

每次 mutation 都可以重新：

```text
resolve / graph validate
```

但不会直接改变 Current Image。

### Long-running Tasks

以下操作全部任务化：

```text
resolve
download
build
apply
collection apply
image switch
```

提供：

```text
task id
progress
logs
cancel
result
```

Manager UI 不应依赖长时间 blocking HTTP request。

### Runtime Control

Manager 通过 M1 的 Runtime control channel：

```text
discover running Runtime
list Plugin Operations
invoke Setup / Config Operation
health
shutdown
```

### M5 Done

Manager backend 已经可以在完全没有 Web UI 的情况下，通过 API/CLI 完成：

```text
编辑 composition
build image
create runtime
start/stop runtime
调用 plugin management operations
```

---

# M6：Ingot Manager UI / Composition Editor

这一阶段再做真正的 Manager UI。

核心不是传统 settings page，而是：

> Visual Composition Editor

### 主界面

默认展示 Plugin-level Composition View。

节点：

```text
Plugin
Version
Source
Provides
Requires
Status
```

边展示 capability relationship。

不要声称 Plugin-level graph 本身一定是严格 DAG。

Advanced View 才展示真正 Component / Capability Graph。

### Inspector

选择 Plugin 后显示：

```text
metadata
version
source
components
provides
requires
compatibility
runtime setup status
```

操作：

```text
Update
Remove
Inspect
Manage Runtime Setup
```

### Drag & Drop

明确区分：

```text
Canvas drag
=
visual layout only
```

和：

```text
Plugin Order editor
=
semantic Direct Plugin reorder
```

绝不能通过拖动画布偷偷改变 `plugins.toml` 顺序。

### Marketplace / Collection Drag-in

从 Discover 区拖入：

```text
Plugin
Collection
```

只修改 Draft。

Collection 展开成真实 Plugin nodes：

```text
+ added
= satisfied
! conflict
```

Collection 自身不会永久留在 Runtime graph 中。

### Graph Validation

添加或删除 Plugin 后，实时显示：

```text
missing capability
provider conflict
compatibility failure
order issue
```

例如：

```text
BrowserBackend -> missing
```

可以直接：

```text
Find Provider
```

进入 Marketplace 搜索。

### Build / Apply

底部始终显示 Draft Diff：

```text
+ browser-tool
- old-search
tool-shell 1.4 -> 1.5
```

用户执行：

```text
Build
```

创建新的：

```text
Image name
Image version
Image digest
```

之后再选择：

```text
Switch Runtime
Build & Run
```

### M6 Done

用户不需要编辑 TOML 就能完成完整 Image composition 生命周期。

---

# M7：Plugin Registry 与 Marketplace

Manager UI 基础完成后，再正式做 Marketplace。

先 Registry，后 Marketplace。

### Registry

记录：

```text
Plugin identity
publisher
description
repository
license
versions
Ingot compatibility
components
provides
requires
release source
release digest
verification status
```

以及：

```text
Collections
```

### Marketplace

Marketplace 是 Composition Editor 的数据源，而不是独立 App Store。

Plugin 页面必须回答：

```text
它提供什么？
它需要什么？
和当前 Image 是否兼容？
加进去以后 graph 会发生什么？
```

### Trust

至少区分：

```text
Official
Verified
Community
Unverified
```

因为 Ingot Plugin 是编译进入同一 Runtime process 的 Go code，不应向用户制造 sandbox 安全假象。

### Source Distribution Decision Gate

到这里再决定现有 Go Module distribution 是否足够。

如果足够：

```text
Registry
↓
Go Module Source
```

继续使用。

如果不足，再引入：

```text
Plugin Release
↓
Source Descriptor
↓
Downloader
↓
Digest Verification
↓
Content-addressed Plugin Cache
```

可能支持：

```text
go-module
archive
git snapshot
```

不要为了官方 monorepo提前实现完整 package transport。

### Collections Marketplace

Marketplace 同时展示：

```text
Plugins
Collections
```

Collection 的核心价值是社区策展：

```text
Coding Essentials
Research Stack
Local-first Stack
Safe Execution Tools
```

用户无需逐个研究 Plugin。

---

# M8：Ecosystem Hardening

当 Marketplace 开始真正承载第三方生态后，再补齐治理能力。

包括：

```text
Plugin release provenance
signed artifacts
SBOM
publisher verification
compatibility matrix
deprecated/yanked releases
state migration policy
plugin diagnostics
registry mirrors
offline package cache
```

以及：

```text
ingot doctor
```

用于检查：

```text
Runtime Home
Image consistency
Plugin state
running processes
IPC
cache
registry
build environment
```

---

# 持续支线：现有 Agent Web UI 改进

现有 Application Web UI 的体验优化不作为主路线图的架构里程碑。

作为持续并行支线推进：

```text
交互体验
Session UX
Operation Forms
Execution visualization
mobile / responsive
accessibility
performance
reconnect
error/loading/empty state
design system
```

这条线可以持续做，但不阻塞 Runtime / Manager / Marketplace 主线。

---

# 推荐实施顺序

主依赖关系建议保持：

```text
M0 Architecture Freeze
        ↓
M1 Standalone Runtime + Plugin-owned State
        ↓
M2 Image / Runtime / Process
        ↓
M5 Manager Backend
        ↓
M6 Composition UI
        ↓
M7 Registry / Marketplace
```

Plugin Ecosystem 可以并行推进：

```text
M0
 ↓
M3 Plugin Repo Split
 ↓
M4 Collections
 ↓
M7 Registry / Marketplace
```

最终汇合：

```text
Runtime Platform ───────┐
                       ├── Manager + Marketplace
Plugin Ecosystem ──────┘
```

---

# 近期最值得做的三个里程碑

如果从现在开始排开发优先级，我建议先集中在：

### 1. Runtime Contract Refactor

先完成：

```text
Runtime Home
Plugin-owned State
Unconfigured Plugin
Operation-based Setup
standalone binary
```

这是后面所有工作的根。

### 2. Image / Runtime Model

紧接着完成：

```text
named/versioned Image
Runtime registry
multi-runtime
isolated state
process lifecycle
```

这是 Manager 的数据模型。

### 3. Official Plugins + Collections

随后完成：

```text
official plugin monorepo
independent releases
collection parser/planner/applier
```

这会把插件生态真正从“仓库里的一批内置模块”变成独立发行体系。

完成这三步以后，Ingot Manager 和 Marketplace 基本就不再需要重新发明底层概念，而只是把已经存在的 Runtime / Image / Plugin / Collection 模型产品化。
