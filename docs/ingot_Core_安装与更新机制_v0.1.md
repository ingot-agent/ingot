# ingot Core 安装与更新机制 v0.1

> **历史设计 / Historical design。** 本文保留原设计范围，状态和里程碑指撰写时的语境，不是当前 CLI/API 参考。
> 当前命令见[使用指南](USAGE.zh.md)，字段与构造函数见[当前文件格式](FILE_FORMATS.md)，已定稿决策见 [ADR](adr/README.md)。

> 状态：已实现  
> 日期：2026-09-15  
> 范围：只覆盖 ingot Core；Plugin 的分发与更新机制不在本文范围内。

## 1. 目标与边界

ingot Core 通过 GitHub Releases 分发预编译原生可执行文件，并由同一个 Release
提供安装脚本、校验信息和更新 Manifest。

安装和更新只允许修改 Core 可执行文件，不得自动执行以下操作：

- 创建、迁移或修改 `INGOT_HOME`；
- 安装、解析或更新 Plugin；
- 构建、切换或删除 Image；
- 创建、重启或停止 Runtime 与 Process；
- 修改 Plugin State 或用户配置。

所有网络检查都必须由用户显式触发。Core 不进行后台版本检查，也不因执行其他命令
访问 Release 服务。

## 2. 版本模型

Core 版本与 Builder、构建协议版本是三个独立身份：

```text
core_version     Core 可执行文件版本，例如 0.3.1-dev 或 0.3.1
builder_version  Builder 实现版本
ingot_version    Plugin Manifest 与 Lock 使用的构建协议版本
```

源码中的 Core 开发版本使用 `X.Y.Z-dev`。正式构建通过 linker flags 注入以下信息：

```text
CoreVersion = X.Y.Z[-prerelease]
Official    = true
Revision    = Release tag 对应的 commit
Modified    = false
```

`ingot --version` 输出简短的人类可读版本；`ingot version` 输出包含上述身份、Go
版本和目标平台的 JSON。更新器只信任结构化输出。

## 3. 支持平台与 Release 资产

每个 Core Release 固定包含六个平台归档：

```text
ingot-vX.Y.Z-linux-amd64.tar.gz
ingot-vX.Y.Z-linux-arm64.tar.gz
ingot-vX.Y.Z-darwin-amd64.tar.gz
ingot-vX.Y.Z-darwin-arm64.tar.gz
ingot-vX.Y.Z-windows-amd64.zip
ingot-vX.Y.Z-windows-arm64.zip
```

归档中只能包含以下两个根级普通文件：

```text
ingot[.exe]
LICENSE
```

Release 还包含：

```text
VERSION
release-manifest.json
checksums.txt
install.sh
install.ps1
```

打包器使用 tag commit 时间统一归档时间戳，并固定成员名称、顺序和权限，以产生确定性
归档。输出目录必须为空，防止旧资产被意外带入新的 `checksums.txt` 或 Release。

## 4. Manifest 契约

`release-manifest.json` 使用严格 JSON schema。未知字段、非规范 SemVer、重复或缺失
平台、非规范资产名、非法摘要和异常大小都会被拒绝。

```json
{
  "schema_version": 1,
  "tag": "v0.3.1",
  "version": "0.3.1",
  "commit": "<full commit hash>",
  "artifacts": [
    {
      "goos": "linux",
      "goarch": "amd64",
      "name": "ingot-v0.3.1-linux-amd64.tar.gz",
      "sha256": "<64 lowercase hex>",
      "size": 123456,
      "executable": "ingot"
    }
  ]
}
```

实际 Manifest 必须包含完整六平台记录。Schema 或平台集合发生不兼容变化时必须提升
`schema_version`，不能静默放宽旧解析器。

## 5. 安装流程

Unix 安装器默认写入 `~/.local/bin/ingot`；Windows 默认写入
`%LOCALAPPDATA%\ingot\bin\ingot.exe`。用户可显式覆盖 prefix、binary directory 和
staging root。

安装流程固定为：

1. 解析最新稳定版本，或校验用户指定的精确 SemVer；
2. 从精确 tag 路径下载 `VERSION`、`checksums.txt` 和当前平台归档；
3. 校验 `VERSION` 和归档的 SHA-256；
4. 校验归档成员、类型和大小；
5. 执行候选文件的 `--version` 并确认版本；
6. 检测已安装 Core 的版本；
7. 通过同目录临时文件替换目标文件。

默认允许首次安装和升级。同版本重装或降级必须使用 `--force` / `-Force`。精确版本
可以选择 prerelease；未指定版本时只选择最新稳定 Release。

安装器不会隐式兼容旧的一键初始化行为。安装完成后，用户根据需要显式执行
`ingot init`、`ingot build` 和 Runtime 命令。

## 6. Core 自更新流程

```text
ingot update --check
ingot update
ingot update --version vX.Y.Z
ingot update --version vX.Y.Z --force
```

更新器按以下顺序工作：

1. 读取当前 Core 的结构化构建身份；
2. 下载 latest stable 或精确 tag 的 Manifest；
3. 比较规范 SemVer，执行升级、同版本和降级规则；
4. `--check` 只返回比较结果，不定位或修改当前可执行文件；
5. 获取可执行文件目录内的更新锁；
6. 下载当前平台归档并限制最大字节数；
7. 校验 Manifest 声明的大小和 SHA-256；
8. 安全提取候选 Core；
9. 执行候选的 `ingot version`；
10. 对照版本、official 标记、commit、dirty 状态和 target；
11. 替换当前可执行文件。

`version` 和 `update` 不接受 `--home`，从命令分派开始就绕过 Home 打开流程。

## 7. 替换与恢复

### Unix

候选文件与目标文件位于同一目录。候选内容落盘并 `fsync` 后通过 `rename` 原子替换，
随后同步父目录。运行中的旧进程继续使用已打开的旧 inode，新进程使用新文件。

### Windows

运行中的 `.exe` 不能按 Unix 方式直接覆盖。更新器执行：

1. 删除上一次已经失效的 `.old`；
2. 将当前 `ingot.exe` 移动为 `ingot.exe.old`；
3. 将候选文件移动到 `ingot.exe`；
4. 若第 3 步失败，立即把 `.old` 移回原位置；
5. 新 Core 下次启动时清理 `.old`。

更新锁防止同一安装目录中的多个更新进程并发替换。

## 8. 信任与安全模型

Core 分发依赖以下多层校验：

- 固定官方 GitHub Release 地址，并继承标准 HTTP(S) proxy 环境；
- Repository 启用 immutable releases，并保护 `v*` tag 创建权限；
- 所有资产发布 SHA-256；
- Manifest 使用严格 schema 和固定资产名称；
- 更新候选必须自证 official、commit、clean 和 target 身份；
- Release 资产生成 GitHub artifact attestation；
- 归档拒绝路径、目录、symlink、重复成员和超限内容。

SHA-256 用于确认下载内容与同一 Release 的声明一致；attestation 用于验证资产由预期
GitHub Actions workflow 产生。两者不等价，也不能替代 Repository 权限控制。

本方案暂不提供 Apple notarization、Windows Authenticode 或 Linux 发行版签名。

## 9. Release Workflow

正式发布由受保护的 `v*` tag push 触发，分为五个阶段：

1. `validate`：校验 tag、源码开发版本、main ancestry，并运行完整 race tests；
2. `build`：在只读权限下交叉编译六个平台；
3. `package`：确定性打包并生成 Manifest 与 checksums；
4. `smoke`：在 Linux、macOS、Windows 原生 runner 上验证归档、摘要和构建身份；
5. `publish`：单独获得写入与 OIDC 权限，生成 attestation 并发布 Release。

普通 Pull Request 不运行额外的安装或更新平台矩阵。常规 `go test -race ./...` 会执行
Core 更新器的单元测试；六平台构建、原生产物 smoke 和发布权限只属于 tag Release
流程。

## 10. 发布步骤

以首个新机制 Release `v0.3.1` 为例：

1. 确认源码 `CoreVersion` 为 `0.3.1-dev`；
2. 合并变更到 `main`，等待分支保护要求的检查通过；
3. 确认 Repository 已启用 immutable releases 和受保护的 `v*` tag 规则；
4. 在目标 `main` commit 创建并推送 `v0.3.1`；
5. 等待 `Release Core` workflow 完成；
6. 验证 Release 包含完整资产、checksums 和 attestations；
7. 使用干净环境执行安装与 `ingot update --check` smoke。

Release 一旦发布不得替换资产或移动对应 tag。任何修复都必须使用新版本。

## 11. 后续工作

Plugin 更新需要独立设计其来源、依赖解析、兼容性、锁文件变更和回滚语义，不复用
Core 二进制自替换机制。Core updater 不为未来 Plugin 更新预留隐式行为。
