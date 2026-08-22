# ingot `init` 与默认发行集设计方案 v0.1

> 状态：Draft  
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
2. 生成一份可用的 `plugins.toml`，内容为官方默认插件集合；
3. 生成一份默认 `config.toml`，包含运行所需配置的占位或默认值；
4. 可选：自动执行一次 `resolve`，生成 `plugins.lock`；
5. 可选：自动执行一次 `build` 或 `apply`，生成初始 Runtime Image；
6. 向用户显示下一步操作，例如“请填写 API key 后运行 `ingot chat`”；
7. 幂等：重复执行不破坏用户已有配置。

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
