# ingot `init` 与默认发行集设计方案 v0.1

> 状态：Draft；实现说明见文末「9. 实现说明（v0.1 落地）」——落地时与本文档的差异（官方插件随主仓库分发、物化到 home）已在第 9 节记录。
> 目标：让用户安装 ingot 后，不需要先理解 Plugin / Component / Capability，也不需要手动寻找和组装插件，就能得到一个可运行的开箱即用环境。

## 1. 背景与目标

ingot 的核心是 Plugin/Component 组合系统，但最终产品不应只是一套“零件”。

用户期望的使用路径是：

```text
安装 ingot
  -> ingot init
  -> 填写必要的运行配置（例如模型 API key）
  -> ingot chat
  -> 直接开始使用
```

因此需要引入一个概念：

> **官方默认发行集 / Default Bundle**  
> 由 ingot 发行版维护一组默认插件，并在初始化时自动写入用户环境。

用户不需要知道：

- 什么是 Component Graph；
- 什么是 `Dependencies` / `Exports`；
- 什么是 `tool.runtime`、`model.runtime`、`agent.default`；
- 应该去哪里寻找和安装哪些插件。

如果用户想扩展或替换，仍可打开 `plugins.toml` 自行修改。

## 2. `ingot init` 的职责

`ingot init` 建议负责以下事情：

1. 创建 standard ingot home 目录结构；
2. 生成一份默认 `builder.toml` 配置 scaffold（v0.1 仅含 schema version）；
3. 生成一份可用的 `plugins.toml`，内容为官方默认插件集合；
4. 生成一份默认 `config.toml`，包含运行所需配置的占位或默认值；
5. 可选：自动执行一次 `resolve`，生成 `plugins.lock`；
6. 可选：自动执行一次 `build` 或 `apply`，生成初始 Runtime Image；
7. 向用户显示下一步操作，例如“请填写 API key 后运行 `ingot chat`”；
8. 幂等：重复执行不破坏用户已有配置。

`init` 应当保持“可审查、可修改”。生成的 `plugins.toml` 是普通用户文件，用户可以增删插件、调整顺序、替换实现。

## 3. 默认插件集的定位

默认插件集应分为两部分：

| 类型 | 作用 | 用户是否需要了解 |
|---|---|---|
| 系统核心插件 | 保证一个最小可用 Agent Runtime 能跑起来 | 不需要 |
| 常用适配器/能力插件 | 提供模型、文件系统、工具、交互等实现 | 按需配置，但默认给出可用示例 |

建议的默认最小集合（以 v0.3 文档中的官方插件为蓝本）：

- `agent.default`
- `model.runtime`
- `tool.runtime`
- `prompt.default`
- `session.jsonl`
- `app.cli`

这些是“运行时骨架”，没有它们很难形成开箱即用的 Agent。

可以再附带的默认示例插件：

- `model.openai-compatible`
- `filesystem.local`
- `tool.shell`
- `tool.fs`
- `tool.ask`
- `interceptor.approval`

具体默认集合需要在 Phase 2/3 根据实际垂直切片确定。`init` 生成的集合应与 ingot 版本一起发布和维护。

## 4. 插件不放到主仓库

核心插件仍然使用标准 Plugin 机制，每个插件是一个独立 Go Module，拥有自己的：

- `go.mod`
- `ingot.plugin.toml`
- Component 实现代码

它们不直接编译进 ingot 主程序，也不放在 ingot 主仓库的普通 Go package 中。原因：

1. 一个 Go Module 只能声明一个 ingot Plugin；
2. 插件应可独立升级、替换、发布；
3. ingot 主仓库应只关注 Bootstrap、Builder、Runtime Image 和 CLI；
4. SDK 是独立的公共契约仓库，插件作者直接依赖 SDK。

`ingot init` 负责把“官方默认插件集”写入用户的 `plugins.toml`，而不是把这些插件源代码复制进主仓库。

## 5. 推荐的用户流程

```text
$ ingot init
Creating ingot home at ~/.ingot
Writing default plugin set...
Creating default config...

Next steps:
  1. Edit ~/.ingot/config.toml to add your model provider settings.
  2. Run: ingot apply
  3. Run: ingot chat
```

`init` 也可以提供参数：

```text
ingot init --profile default
ingot init --profile minimal
ingot init --no-build
```

其中：

- `default`：包含系统核心 + 常用示例插件；
- `minimal`：只包含最小可运行核心；
- `--no-build`：只生成文件，不自动构建。

## 6. 与现有文件的关系

| 文件 | init 的行为 |
|---|---|
| `~/.ingot/builder.toml` | 写入默认 Builder 配置 scaffold |
| `~/.ingot/plugins.toml` | 写入默认 Direct Plugin Set |
| `~/.ingot/plugins.lock` | 由 resolve/build 生成 |
| `~/.ingot/config.toml` | 写入默认运行配置模板 |
| `~/.ingot/current` | 由 apply/build 切换 |
| `~/.ingot/images/` | 存放不可变 Runtime Image |

`init` 只负责“初始可用状态”的建立，不改变已有用户配置。

## 7. 后续演进方向

1. **Profile / Bundle 文件化**
   - 维护 `default.toml`、`minimal.toml` 等 profile 模板；
   - 允许社区或团队自定义 profile。

2. **离线开箱即用**
   - 后续可考虑发行版内嵌核心插件源码或 module cache；
   - 或者安装器预填充 `~/.ingot/cache/gomod`；
   - 这样首次构建不依赖网络。

3. **`ingot doctor`**
   - 检查默认插件是否完整；
   - 检查配置是否缺失；
   - 检查当前 image 是否可运行。

4. **升级同步**
   - ingot 升级时，可以提示是否需要更新默认插件集合；
   - 但不自动覆盖用户已经修改的 `plugins.toml`。

## 8. 设计原则

1. 开箱即用，但不隐藏灵活性。
2. 默认集由 ingot 发行版维护，而不是让用户自行拼装。
3. 核心插件仍是标准 Plugin，不引入特殊运行时旁路。
4. SDK 独立仓库，插件作者面向 SDK 开发。
5. 用户修改过的 `plugins.toml` 是最高优先级。
6. `init` 幂等、可审查、可回滚。

## 9. 实现说明（v0.1 落地）

落地时与本文档的主要差异与决策如下：

### 9.1 官方插件随主仓库分发（与第 4 节不同）

官方插件作为独立 Go Module 位于主仓库 `plugins/` 下（每个目录一个插件），但**不作为单独 module 版本发布**，而是随 ingot 二进制一起分发：

- `scripts/install.sh` / `scripts/install.ps1` 将 CLI 安装到 `<prefix>/bin/ingot`，将插件源码树安装到 `<prefix>/share/ingot/plugins`；
- 仓库内开发构建时，二进制位于仓库根目录，`plugins/` 即为其邻近分发目录。

### 9.2 插件集定位

`ingot init` 通过以下顺序定位官方插件集目录：

1. `--bundle PATH` 显式指定；
2. 可执行文件相对位置探测（`<bin>/plugins`、`<bin>/share/ingot/plugins`、`<bin>/../plugins`、`<bin>/../share/ingot/plugins`）。

定位成功的判据：目录包含全部官方插件目录（每个都有 `go.mod` 与 `ingot.plugin.toml`）。

### 9.3 物化到 home

`init` 将官方插件集**复制**到 `~/.ingot/bundled-plugins/`（幂等）：

- 目录内容摘要写入 `bundled-plugins/.ingot-bundle-digest`；摘要与全部插件目录都存在时跳过重写；
- 插件集发生变化（例如升级 ingot 二进制）时整体重写；
- `plugins.toml` 中每个官方插件声明为本地开发源码（`module` + `path = "bundled-plugins/<name>"`），因此 build identity 由物化后的源码内容决定，home 在初始化后不依赖安装位置。

### 9.4 Profile

已实现两个 profile：

| Profile | 插件数 | 内容 |
|---|---|---|
| `default` | 14 | 骨架（`asset.local`、`http.default`、`model.openai-compatible`、`model.runtime`、`tool.runtime`、`tool.shell`、`tool.fs`、`tool.ask`、`interceptor.approval`、`filesystem.local`、`prompt.default`、`session.jsonl`、`agent.default`、`app.cli`） |
| `minimal` | 9 | 最小组合：骨架 + 一个模型 Provider + 其 HTTP 与 Asset 依赖 |

### 9.5 默认配置模板

生成的 `config.toml` 为每个插件写一个 `[plugins."<short-name>"]` 表（运行时要求每个锁定插件恰好一个表），并为需要机器相关值的插件提供可用默认值：

- `model.openai-compatible`：示例 Provider（占位 `base_url`/`api_key`，格式校验可通过，真实调用需用户填写）；
- `asset.local`：默认使用应用 State 目录，配置中提供对象大小、总容量与 I/O 并发的注释示例；
- `filesystem.local`：`root = "."`（以启动 `ingot chat` 的工作目录为工作区）；
- `tool.shell`：`working_directory` 与 `shell` 使用 init 时的本机值（绝对路径，保证 `--ingot-check` 通过）。

### 9.6 幂等与覆盖

- 已存在 `plugins.toml` 时拒绝重新初始化（除非 `--force`）；
- 已存在 `builder.toml` 时保留 Builder 配置（除非 `--force`）；
- 已存在 `config.toml` 时保留用户配置（除非 `--force`）；
- `init` 默认不构建；`--apply` 会立即执行 resolve + build + switch。
