# ingot 使用说明

> 中文版 · [English version](./USAGE.md)

ingot 将项目 Recipe、不可变 Image、持久 Runtime 与单次 Process 明确分离。不再存在全局
当前镜像，未知命令也不会隐式派发到某个 Runtime。

## 安装与初始化

官方安装器从 GitHub Releases 下载当前平台对应的不可变 Core 归档，校验 SHA-256
后只安装 `ingot` 可执行文件。安装过程不依赖 Go，也不会创建或修改
`INGOT_HOME`、插件、Image 或 Runtime。

```sh
# Linux 与 macOS；默认安装到 ~/.local/bin/ingot
curl -fsSL https://github.com/ingot-agent/ingot/releases/latest/download/install.sh | sh
```

```powershell
# Windows PowerShell；默认安装到 %LOCALAPPDATA%\ingot\bin\ingot.exe
$installer = Join-Path $env:TEMP 'install-ingot.ps1'
Invoke-WebRequest https://github.com/ingot-agent/ingot/releases/latest/download/install.ps1 -OutFile $installer
& $installer
Remove-Item $installer
```

两个安装器默认选择最新稳定 Release。Unix 使用 `--version`、Windows 使用
`-Version` 可指定精确版本，包括 prerelease；重装同版本或降级必须显式添加 force。
下方 `v0.3.1` 仅演示版本语法，请从 [Releases 页面](https://github.com/ingot-agent/ingot/releases)选择实际已发布的版本：

```sh
curl -fsSL https://github.com/ingot-agent/ingot/releases/latest/download/install.sh | \
  sh -s -- --version v0.3.1
```

```powershell
Invoke-WebRequest https://github.com/ingot-agent/ingot/releases/latest/download/install.ps1 -OutFile $installer
& $installer -Version v0.3.1 -Force
Remove-Item $installer
```

Unix 参数为 `--prefix`、`--bindir`、`--destdir`、`--version` 和 `--force`；
PowerShell 对应 `-Prefix`、`-BinaryDir`、`-DestDir`、`-Version` 与 `-Force`。
已有预发布 Home 会被原样保留；已移除的 Home、Profile、配置与 Runtime 安装参数
不会被自动替代。

若要从本仓库 checkout 构建 Core，则需要 Go 1.24.2 或更高版本：

```sh
GOWORK=off go build -o ingot ./cmd/ingot
```

Release workflow 生成 Linux、macOS 和 Windows 的 `amd64` 与 `arm64` 归档。
Unix 需要确保安装目录位于 `PATH` 中，脚本不会修改 Shell 启动文件；Windows 安装器会
更新用户与当前进程的 `PATH`，使用 `-DestDir` 暂存安装时除外。

安装 Core 或运行已有 Image 不需要 Go。构建或解析组合需要 `PATH` 中的 `go`、足够的
模块缓存与构建空间，以及通过 `GOPROXY` 访问所选模块的能力（或已填充的缓存）。当前
Builder 只构建并校验自身宿主 OS/架构的 Image，关闭 CGO，设置 `GOWORK=off` 与
`GOTOOLCHAIN=local`，不会自动下载另一套 Go toolchain。插件依赖可能要求比 Core 更新的
Go 版本。Node 仅在 plugins 仓库中开发或重建浏览器前端时需要，运行已嵌入的发布产物无需 Node。

用于开发时，Builder 还会独立扫描 CLI 当前工作目录及其祖先目录中的 `go.work`，
读取 ABI 或已选依赖的无版本本地 `replace`。因此即使设置 `GOWORK=off`，lock 中仍可能
出现本地替换。核验已发布模块时，应从这类工作区之外的目录运行 CLI 并检查 lock；
仅修改 `--home` 不会隔离这种源码查找。完整验证流程见 [RELEASE.md（英文）](../RELEASE.md)。

PowerShell 对应的源码构建命令：

```powershell
$env:GOWORK = 'off'
go build -o ingot.exe ./cmd/ingot
```

无论使用哪种安装方式，都需要显式初始化 Managed Home：

```sh
ingot setup
```

`setup` 在 `INGOT_HOME` 指向的目录初始化 schema v2 Home；未设置该变量时使用
`~/.ingot`。所选官方 Profile recipe 位于 Home 的 `profiles/` 下。Recipe 会把官方插件
精确固定到已发布的模块版本。`setup` 不会写入当前目录，也不会创建 Runtime。

在当前目录创建项目自己的 Recipe，也可以显式指定其他目录：

```sh
ingot init [DIR] [--profile default|minimal] [--force]
```

`init` 同时确保 Managed Home 已存在；除非传入 `--force`，否则不会覆盖已有项目 Recipe。

全局 `--home` 会覆盖 `INGOT_HOME`：

```sh
ingot --home /path/to/home setup
```

旧布局或非空的不兼容 Home 会被拒绝。当前 Core 不迁移预发布阶段的 `current`、顶层
`state` 或旧 Image manifest。

## Profile 与配置归属

当前 checkout 的初始 Recipe 以
[`default.toml`](../internal/profiles/default.toml) 和
[`minimal.toml`](../internal/profiles/minimal.toml) 为准。两者的所有插件目前均固定到
`v0.1.0`；后续 Core Release 可以提供不同的精确版本选择。

| Profile | 选中的插件 |
| --- | --- |
| `minimal` | `asset.local`、`http.default`、`model.openai-compatible`、`model.runtime`、`tool.runtime`、`prompt.default`、`session.sqlite`、`agent.default`、`app.backend` |
| `default` | `minimal` 的全部插件，加上 `tool.shell` 与 `tool.ask` |

两种 Profile 都使用浏览器应用。`minimal` 保留 Tool Runtime，但没有 Tool Provider。
审批、脚本策略、上下文压缩、用量记录、编辑工具、Sub-Agent 等其他官方插件需要显式选择；
位于 plugins 仓库不代表默认加入 Profile。

```sh
ingot setup --profile minimal
ingot init ./my-agent --profile minimal
# 也可直接构建 Managed Profile，无需创建项目 Recipe：
ingot build --profile minimal
```

`setup` 在内置 Recipe 内容变化时刷新选中的 Managed Profile；`setup --force` 还会使用
分发默认值重写 `builder.toml`。由 `init` 创建的项目 Recipe 归用户所有，更新 Core 或运行
`setup` 不会更新已有项目的插件版本。

配置分为三个独立边界：

| 文件 | 所有者与用途 |
| --- | --- |
| `<home>/builder.toml` | Core Builder 设置；当前严格 schema 只有 `builder_config_version = 1`。 |
| `<project>/plugins.toml` | 有序插件选择；每项包含 `module`，并且在精确 `version` 与本地 `path` 中二选一。相对路径以此 Recipe 所在目录为基准。 |
| `<home>/runtimes/<name>/state/<plugin>/` | 按 Runtime 隔离的插件配置和持久化数据；文件名、校验、修改即时生效还是需要重启，均由插件决定。 |

没有全局 `config.toml`、`[plugins.<name>]` 配置包裹层、Builder SDK 列表或 Core `config`
命令。不要将 API key 写入 Recipe 或 Builder 配置。当前 schema 见
[文件格式参考](./FILE_FORMATS.md)与相应所有者的
[插件文档](https://github.com/ingot-agent/plugins/tree/main/docs)。

## Core 版本与更新

实际升级前请阅读[升级、备份与恢复说明（英文）](./UPGRADING.md)，其中覆盖停止全部
写入者、备份状态与工作区、Image 回滚限制，以及在新 Managed Home 中恢复的完整流程。

Core 更新检查始终由用户显式触发。`version` 与 `--version` 是本地查询；下方只有
`update` 命令会为更新而访问 GitHub，并继承当前进程的 HTTP(S) 代理配置：

```text
ingot --version
ingot version
ingot update --check
ingot update
ingot update --version v0.3.1
ingot update --version v0.3.1 --force
```

`--version` 输出简短的 Core 版本；`version` 输出 Core、ingot 协议、Builder 与 target
身份。`ingot --json version` 还会输出 Go 版本、源码 revision、official/modified 等来源
信息。这些身份独立演进；协议身份与固定的 `github.com/ingot-agent/ingot-abi` 模块版本不同。

未指定 `--version` 时，`update` 只解析最新稳定 Release，不会选择 prerelease；精确版本
可以选择 prerelease。降级和同版本重装需要 `--force`；`--check` 绝不修改二进制，且
不能与 `--force` 同时使用。版本与更新命令不会读写 Home。

替换前，updater 会校验归档摘要，并执行候选 Core，将其版本、官方构建标记、源码
revision、clean 状态和平台 target 与 `release-manifest.json` 对照。Unix 上替换经过锁
串行化并原子完成；Windows 会保留回滚副本，直到新 Core 启动。Core 更新不会修改
插件、Image、Runtime 定义、State 或正在运行的 Process。

Release 资产还带有 GitHub artifact attestation。下载资产后，可以使用 GitHub CLI
校验其 workflow 来源：

```sh
gh attestation verify ingot-v0.3.1-linux-amd64.tar.gz \
  --repo ingot-agent/ingot \
  --signer-workflow ingot-agent/ingot/.github/workflows/release.yml
```

## 存储布局

项目拥有：

```text
<project>/
  plugins.toml
  plugins.lock
```

机器级 Managed Home：

```text
~/.ingot/
  home.json
  builder.toml
  profiles/
    <profile>.toml
    <profile>.lock
  cache/gomod/
  images/
    catalog.json
    <image-id>/
      manifest.json
      ingot-runtime[.exe]
  runtimes/<name>/
    runtime.json
    state/<plugin>/
    run/
    logs/
  .transactions/
```

`plugins.lock` 只保存 target-neutral 解析事实。实际 target、Go toolchain、构建参数、
Runtime ABI 与 target-specific Graph projection 进入每个 Image 的 BuildManifest。
State 永远属于 Runtime，不属于 Image。

## 标准流程

```sh
ingot setup
ingot init .
ingot up -- web
```

`up` 会构建最近的项目 Recipe，把结果绑定到 `default` Runtime，将 `--` 后的 argv
持久化为该 Runtime 的默认命令，停止旧 Process，再以前台模式启动新 Process。添加
`-d` 可在后台启动：

```sh
ingot up -d -- web
ingot logs -f
ingot stop
```

命名 Runtime 便于并行调试，无需为每次构建发明 Image tag：

```sh
ingot up work -d -- web
ingot logs work -f
ingot stop work
```

Runtime binding 始终保存 concrete Image 与 Artifact digest。`default` 只是生命周期命令
省略可选 Runtime 参数时选中的默认 Runtime 名称，不是 mutable Image 引用。
如果 `up` 无法停止旧 Process，会返回错误，并保留新构建的 Image 与 desired Runtime
binding，同时显示 `restart_required`；它不会在仍运行旧 Process 时把 binding 悄悄回滚。

## 配置浏览器 Agent

执行 `ingot up -d -- web` 后，打开
[http://127.0.0.1:7316/](http://127.0.0.1:7316/)。模型尚未配置是正常初始状态：
浏览器可以启动，但模型请求需要有效的 Provider 与模型。

以下步骤对应当前 Profile 精确选择的插件 `v0.1.0`；其浏览器 Operation 位于
`configuration` 分组，请在 Operation 选择器中选择。

1. 选择 `model.openai-compatible.config`，先添加**一个 Provider** 的名称、绝对
   HTTP(S) base URL、需要时使用的 API key，以及模型 ID；模型列表为空表示不做本地
   模型白名单限制。该发布版本返回 `restart_required: true`，执行
   `ingot restart default` 并刷新浏览器后再继续。
2. 选择 `model.runtime.config`，设置默认 Provider 和模型。只有一个 Provider 时可以
   自动选择 Provider，但模型请求仍须提供实际模型 ID。修改默认值会返回
   `restart_required: true`，再次执行 `ingot restart default` 并刷新页面。
3. 创建或选择 Session，在 Workspace Binding 固定前选择工作区，然后发送消息。Shell
   工具在该 Session 的工作区中运行，不一定是执行 `ingot up` 时的当前目录。

该发布版本在 Runtime 构造时固定 Provider 列表与默认值，保存配置不会更新当前 Process。
如果一次添加多个 Provider，必须在**首次重启之前**通过 `model.runtime.config` 保存明确的
`default_provider`，否则该版本会因多个 Provider 且无默认项而拒绝启动。使用命名 Runtime
时，将上述 `default` 换成实际名称；这些配置修改无需重新构建 Image。

内置应用对应的 Operation 为 `configuration` 分组下的 `app.backend.config`，修改应用
配置后也需要重启。遵循 Operation 自己返回的 `restart_required`；它与 Core
`runtime show` 根据 binding/generation 输出的同名字段不同，Core 不检查插件配置变化。

plugins 仓库 `main` 的更新源码使用 `/model-openai-compatible config`、
`/model-runtime config`、`/app-webui config`，其中 Provider 和默认模型配置可以动态生效。
不要将这些新命令名和热更新行为套用到当前 Profile 的已发布 `v0.1.0` 模块；插件文档和
变更记录应与 recipe/lock 中的模块版本对应。

若要手工编辑配置文件，先停止 Runtime，按照对应
[插件指南](https://github.com/ingot-agent/plugins/tree/main/docs)编辑，再启动 Runtime。
默认值和密钥属于该 Runtime，创建另一个 Runtime 不会复制它们。两个浏览器 Runtime
不能同时监听相同地址；第二个 Runtime 应配置不同的 backend 端口。默认应用面向可信的
本机单用户场景。

## 项目命令

Recipe 命令从当前目录向上搜索，并使用最近的 `plugins.toml`；默认 lock 是相邻的
`plugins.lock`。使用 `-f/--file` 与 `--lock` 可指定路径，使用 `--profile` 可选择 Home
管理的 Profile。`--profile` 不能与 `--file` 或 `--lock` 同时使用。

```text
ingot project resolve [-f recipe.toml] [--lock recipe.lock]
ingot project status [-f recipe.toml] [--lock recipe.lock]
ingot project show [-f recipe.toml] [--lock recipe.lock]
ingot project generate -o runtime-source [-f recipe.toml] [--lock recipe.lock] [--locked]
ingot build [runtime] [-f recipe.toml] [--lock recipe.lock] [--locked] [--tag name:tag]
ingot up [runtime] [-d] [-f recipe.toml] [--lock recipe.lock] [--locked] [--tag name:tag] [-- argv...]
```

普通 build 会刷新缺失或 stale 的 lock。`--locked` 要求 lock 与全部源码事实完全匹配，
且绝不改写 lock。`--tag` 会在构建成功后额外移动当前主机 target slot。

`project generate` 使用相同的 lock 刷新与 `--locked` 规则，加载并类型检查完整
Component Graph，然后把 generated Runtime 写成独立的 `package main` Go module。该命令
不会执行 `go build`、不会运行 Runtime validation check、不会创建 Image，也不会移动 tag
或绑定 Runtime。显式输出目录必须不存在或为空，且不能位于任一本地 replacement 源码树内。

导出 module 包含 `go.mod`、`go.sum`、generated Go 文件、精确的
`ingot-build-manifest.json`，以及放在 `dev/` 下并通过相对 `replace` 引用的本地
replacement 副本。远程 module 不会 vendor，其精确版本与摘要继续由 module 文件锁定。
Build manifest 记录复现 expected Image identity 所需的 target、Go 版本、tags 与编译 flags。

`build` 始终把结果绑定到且只绑定到一个 Runtime。Runtime 默认为 `default`；不存在时
自动创建，已存在时切换 binding。`build` 不启动或重启 Process。重复构建同一个 Image
不会递增 Runtime generation。切换正在运行的 Runtime 时旧 Process 保持运行，并显示
`restart_required`，直到显式重启。

Plugin mutation 使用同一套项目选择规则：

```text
ingot plugin ls
ingot plugin show <plugin>
ingot plugin add <module[@query]|path>
ingot plugin rm <plugin>
ingot plugin update <plugin[@query]>
ingot plugin move <plugin> --before <anchor>
ingot plugin move <plugin> --after <anchor>
```

全部 Plugin 命令均接受 `--file`、`--lock` 或 `--profile` 选择器。`@latest` 等 Module
query 在写入 Recipe 前会解析为精确版本。`plugin update` 省略 query 时使用 `latest`，
它不会一次更新全部插件。`move` 必须且只能指定 `--before` 或 `--after` 中的一个。
`plugin add` 需要明确的本地路径（Unix 的 `./my-plugin` 或绝对路径；Windows 的
`.\my-plugin` 或绝对路径）。

成功的 Plugin mutation 会解析并提交 Recipe/lock 变更，不会重新构建或重启已有 Runtime；
使用 `ingot up` 才会采用新组合。Module 解析成功不代表 Component Graph 有效，完整的
类型与依赖检查在 `build` 或 `project generate` 时执行。构建验证以临时空 Runtime Home
执行生成的本机程序及其 `--ingot-check` 模式。这验证的是检查模式下的构造过程，不能
验证生产 Runtime 已保存的配置或 Provider 凭据。检查中会执行插件代码，因此只构建可信插件。

## Plugin Collections

Collection 是由已发布 Plugin exact module version 组成的可复用有序 Recipe。它只负责
输入变换；Apply 后唯一权威 desired state 仍然是 `plugins.toml`。

```toml
collection_schema = 1
id = "github.com/example/ingot-collections/coding"
version = "v1.0.0"

[metadata]
name = "Coding Essentials"

[[plugins]]
module = "github.com/ingot-agent/plugins/tool-shell"
version = "v0.1.0"
```

```text
ingot collection inspect [--expect-digest sha256:...] <path-or-https-url>
ingot collection plan [-f ...] [--lock ...] [--expect-digest sha256:...] [--accept-order] <source>
ingot collection apply [-f ...] [--lock ...] [--expect-digest sha256:...] [--accept-order] <source>
```

`inspect` 不要求已初始化 Home。`plan` 不解析 module source，只区分 Add、Satisfied、
VersionConflict、SourceConflict 与 OrderConflict；存在冲突的有效 Plan 仍输出 JSON，
并通过 `applicable: false` 表示不可应用。

Collection 顺序是严格子序列约束，不要求形成连续块。默认绝不改变已有 Plugin 顺序；
`--accept-order` 显式授权 Plan 中展示的确定性最小反转重排，但不授权版本变化，也不允许
Collection 替换 Local Path source。

`apply` 会在项目锁内重新规划、执行完整 resolve preflight，并原子提交 `plugins.toml` 与
`plugins.lock`。获取、摘要、解析、冲突或 resolve 失败时两个文件均不改变。当前实现不保存
Collection receipt，也不提供 Collection remove/update。

## Image 命令

```text
ingot image ls
ingot image show <ref>
ingot image verify <ref-or-digest>
ingot image tag <ref-or-digest> <name>:<tag>
ingot image untag <name>:<tag>
ingot image pin|unpin <ref-or-digest>
ingot image rm <digest>
ingot image export <ref-or-digest> [--target os/arch] --output file.ingot-image
ingot image import file.ingot-image [--no-tag]
```

引用格式为 `sha256:<64 位小写 hex>`、`name:tag` 或 `name:tag@goos/goarch`。
名称与 tag 只允许小写 ASCII；系统不会隐式补 `latest`。

一个 tag 是可移动的多 target 指针。Runtime 与 Process 保存 concrete Image/Artifact
digest，因此 tag 移动不会改变已有 binding。Bundle 是只包含一个 target variant 的确定性
ZIP；import 会在提交前校验路径、entry 数量、大小、Build Input identity、target 与可执行
文件摘要。校验通过只代表内容完整，不代表本机代码可信。

## Runtime 命令

```text
ingot run <name> <image> [-d] [-- <default-argv>]
ingot runtime create <name> <image> [-- <default-argv>]
ingot runtime ls
ingot runtime show [name]
ingot runtime switch <name> <ref>
ingot runtime rollback [name]
ingot runtime command set [name] -- <argv...>
ingot runtime command clear [name]
ingot runtime rm <name> [--purge]
ingot start [name] [--foreground] [-- <temporary-argv>]
ingot restart [name]
ingot logs [name] [--process <id>] [-f]
```

`switch` 原子更新 desired/rollback binding，但不会重启 live Process。当实际 Image 或
Runtime generation 与期望值不同时，`runtime show` 输出 `restart_required: true`。
`rollback` 只交换 binding，不复制或解释 State。

生命周期和检查命令省略可选 Runtime 名称时默认使用 `default`。`run` 用于从已构建的
Image 引用创建 Runtime；普通的构建并重启流程应使用 `up`。

每个 Runtime 都有独立 Runtime Home。Generated Image 在构造任何 Plugin 之前获取
`run/writer.lock`，因此 standalone 与 managed launch 遵守同一单 writer 契约。

`start` 默认后台启动；`start --foreground` 连接当前终端。它的 `--` 后参数仅对本次
启动有效。`run` 创建新 Runtime，默认前台运行，`-d` 切换到后台；其 argv 会持久化为
Runtime 默认命令。`restart` 使用持久化默认 argv，并在后台启动。`up` 不带 `--` 时保留
已有 Runtime 的默认 argv。无需构建即可用 `runtime command set` 或 `clear` 修改默认值。

`runtime rm` 要求 Runtime 已停止；若 `state/` 或 `logs/` 非空，则必须指定 `--purge`。
`--purge` 永久删除该 Runtime 的状态和日志，不会删除其 Image。Image rollback 恢复代码
binding，不恢复插件数据；它不是状态备份或 schema 降级机制。

## Process 命令

```text
ingot ps
ingot ps -a
ingot stop [runtime] [--timeout 10s]
ingot stop --process <process-id> [--timeout 10s]
```

前台 CLI 与后台 `ingot supervise` 负责 Process record、退出记录、日志，以及带随机 token
鉴权的 IPv4 loopback control endpoint。`running` 只表示 OS child 已启动，不代表应用
ready。Stop 只请求正常 termination，不基于 PID 猜测并强制杀死进程。

Runtime 状态包括 `stopped`、`starting`、`running`、`stopping`、`unresponsive`、
`orphaned`、`external` 与 `failed`。外部 standalone writer 的实际 Image 不可知，因此
相关 mutation 与 GC 会 fail-closed。

## GC

```sh
ingot gc [--keep-recent N]
```

`--keep-recent` 默认值为 `3`。GC 保留全部 tag variant、pin、Runtime desired/rollback、live Process actual Image 和指定
数量的最近未引用 Image。任何 root 缺失/损坏或存在 external Runtime writer 时，本次 sweep
不删除任何内容。

## 输出与退出码

命令默认输出简洁的人类可读文本；需要稳定机器输出时传入全局 `--json`。前台 `run`、
前台 `up`、`start --foreground` 与原始 `logs` 会拒绝 `--json`，因为 stdout 属于 Runtime
或日志流。

Usage error 返回 `2`；domain、I/O 与 verification error 返回 `1`；前台运行原样传播
Runtime exit code。

## Shell 自动补全

Cobra 可生成 Bash、Zsh、Fish 与 PowerShell 补全脚本：

```sh
ingot completion bash
ingot completion zsh
ingot completion fish
ingot completion powershell
```

动态补全会提示已有 Runtime 名称、Image 引用、Plugin 与 Profile。补全严格只读：不会
初始化 Home、恢复 transaction、解析 module、构建 Image 或访问网络。

## 排查与文档范围

| 现象 | 检查 |
| --- | --- |
| 找不到 `ingot` | 确认安装目录已加入 `PATH`；必要时重新打开 Shell。 |
| 找不到项目 Recipe | 执行 `ingot init .`，或使用 `--file` / Managed `--profile`。 |
| Module 下载或 toolchain 失败 | 检查 `go version`、模块发布情况、凭据和 `GOPROXY`。Builder 禁止自动下载 toolchain。 |
| 浏览器无法打开 | 查看 `ingot logs`、`ingot ps -a` 并检查监听地址是否被占用；Process 已启动不代表应用 ready。 |
| Provider/模型不可用 | 在浏览器 Operation 中配置 Provider 访问参数及默认模型，并查看插件错误。 |
| 代码改动没有生效 | 使用 `ingot up` 构建项目；`start` 使用 Runtime 已绑定的 Image。 |
| Runtime 显示 `restart_required` | 检查 `ingot runtime show`，再重启目标 Runtime。 |
| 使用 `--locked` 时 lock 已过期 | 审查源码/Recipe 变更，执行 `ingot project resolve` 或普通构建，然后审查新 lock。 |

本文描述已实现的 Core CLI。插件配置、HTTP API、前端开发和实现设计记录在
[plugins 文档](https://github.com/ingot-agent/plugins/tree/main/docs)中维护。
[Core 文档索引](./README.md)区分当前参考与历史设计；历史提案不代表已支持的 API。
