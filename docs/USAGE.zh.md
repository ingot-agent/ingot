# ingot M2 使用说明

> 中文版 · [English version](./USAGE.md)

M2 将项目 Recipe、不可变 Image、持久 Runtime 与单次 Process 明确分离。不再存在全局
当前镜像，未知命令也不会隐式派发到某个 Runtime。

## 安装与初始化

需要 Go 1.24 或更高版本。

```sh
go build -o ingot ./cmd/ingot
./ingot init
```

`init` 在 `~/.ingot` 初始化 schema v2 Home、物化官方插件 Bundle，并在
`~/.ingot/profiles/` 下维护所选官方 Profile recipe。它不会写入当前目录，也不会创建
Runtime。

只有显式指定项目目录时才创建项目自己的 Recipe：

```sh
ingot project init . [--profile default|minimal] [--force]
```

全局 `--home` 必须放在命令之前：

```sh
ingot --home /path/to/home init
```

旧布局或非空的不兼容 Home 会被拒绝。M2 不迁移预发布阶段的 `current`、顶层
`state` 或旧 Image manifest。

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
  bundled-plugins/
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
ingot init
ingot project init .
ingot build --tag acme/coding-agent:1.0.0
ingot runtime create work --image acme/coding-agent:1.0.0 -- web
ingot runtime start work
ingot runtime logs work --follow
ingot stop work
```

也可以用 Docker 风格便利命令一次创建并启动：

```sh
ingot run --name work --detach acme/coding-agent:1.0.0 -- web
```

## 项目命令

Recipe 命令默认只读取当前目录的 `plugins.toml` 与相邻 `plugins.lock`，不向父目录搜索，
也不回退到 Home。Home 中由 Ingot 管理的官方 Profile recipe 必须像安装脚本一样通过
`--use` 显式选择。

```text
ingot resolve [--use recipe.toml] [--lock recipe.lock]
ingot build [--use recipe.toml] [--lock recipe.lock] [--locked] [--tag name:tag]
ingot status [--use recipe.toml] [--lock recipe.lock]
ingot inspect [--use recipe.toml] [--lock recipe.lock] [plugin]
```

普通 build 会刷新缺失或 stale 的 lock。`--locked` 要求 lock 与全部源码事实完全匹配，
且绝不改写 lock。`--tag` 只在构建成功后移动当前主机 target slot。

Plugin mutation 使用同一套 `--use/--lock` 规则：

```text
ingot plugin list|inspect ...
ingot plugin add module@version
ingot plugin add --path ../plugin
ingot plugin remove|update|reorder ...
```

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
ingot runtime create <name> --image <ref> [-- <default-argv>]
ingot runtime list
ingot runtime inspect <name>
ingot runtime switch <name> <ref>
ingot runtime rollback <name>
ingot runtime command set <name> -- <argv...>
ingot runtime command clear <name>
ingot runtime run <name> [-- <temporary-argv>]
ingot runtime start <name> [--timeout 30s] [-- <temporary-argv>]
ingot runtime restart <name> [--timeout 30s]
ingot runtime logs <name> [--process <id>] [--follow]
ingot runtime delete <name> [--purge]
```

`switch` 原子更新 desired/rollback binding，但不会重启 live Process。当实际 Image 或
Runtime generation 与期望值不同时，`runtime inspect` 输出 `restart_required: true`。
`rollback` 只交换 binding，不复制或解释 State。

每个 Runtime 都有独立 Runtime Home。Generated Image 在构造任何 Plugin 之前获取
`run/writer.lock`，因此 standalone 与 managed launch 遵守同一单 writer 契约。

## Process 命令

```text
ingot ps
ingot stop <runtime> [--timeout 10s]
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

除前台 Runtime stdio 与原始日志流外，命令成功时输出稳定 JSON。Usage error 返回 `2`；
domain、I/O 与 verification error 返回 `1`；前台运行原样传播 Runtime exit code。
