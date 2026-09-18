# ingot M2 使用说明

> 中文版 · [English version](./USAGE.md)

M2 将项目 Recipe、不可变 Image、持久 Runtime 与单次 Process 明确分离。不再存在全局
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
`-Version` 可指定精确版本，包括 prerelease；重装同版本或降级必须显式添加 force：

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

若要从源码构建 Core，则需要 Go 1.24 或更高版本：

```sh
GOWORK=off go build -o ingot ./cmd/ingot
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

旧布局或非空的不兼容 Home 会被拒绝。M2 不迁移预发布阶段的 `current`、顶层
`state` 或旧 Image manifest。

## Core 版本与更新

Core 更新检查始终由用户显式触发。只有以下 update 命令会访问 GitHub，并继承当前
进程的 HTTP(S) 代理配置：

```text
ingot --version
ingot version
ingot update --check
ingot update
ingot update --version v0.3.1
ingot update --version v0.3.1 --force
```

`--version` 输出简短的 Core 版本；`version` 输出 Core 版本与来源、构建协议版本和
Builder 版本。需要稳定机器输出时使用全局 `--json`。这些身份彼此独立演进。

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
ingot up [runtime] [-d] [-f recipe.toml] [--lock recipe.lock] [--locked] [-- argv...]
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
ingot plugin ls|show ... [-f ...] [--lock ...]
ingot plugin add <module[@query]|path> [-f ...] [--lock ...]
ingot plugin rm|update|move ... [-f ...] [--lock ...]
```

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
`plugins.lock`。获取、摘要、解析、冲突或 resolve 失败时两个文件均不改变。M4 不保存
Collection receipt，也不提供 Collection remove/update。

## Image 命令

```text
ingot image list
ingot image inspect <ref>
ingot image verify <ref-or-digest>
ingot image tag <ref-or-digest> <name>:<tag>
ingot image untag <name>:<tag>
ingot image pin|unpin <ref-or-digest>
ingot image remove <digest>
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

GC 保留全部 tag variant、pin、Runtime desired/rollback、live Process actual Image 和指定
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
