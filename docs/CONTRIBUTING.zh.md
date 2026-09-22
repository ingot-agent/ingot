# 为 ingot 贡献

[**English**](../CONTRIBUTING.md) · [**中文**](./CONTRIBUTING.zh.md)

感谢你帮助改进 ingot。我们欢迎代码、测试、文档、Bug 报告和设计反馈。

## 开始之前

- 提交新 Issue 前，先搜索已有 Issue 和 Pull Request。
- 小型修复和文档改进可以直接提交 Pull Request。
- 大型功能、架构调整、新的文件格式行为和破坏性变更，应先与维护者讨论，再投入实现。
- 不要在公开 Issue 中报告漏洞细节。当前报告渠道及其可用性见 [SECURITY.md](../SECURITY.md)，不要假定私密报告功能已经启用。

## 项目原则

所有贡献都必须保持 ingot 的核心性质：

- **插件一律平等。** 核心行为不得依赖特定插件名称、Module Path、Package Path 或官方插件身份。
- **在构建期完成组合。** Builder 解析并校验静态依赖图，生成普通 Go wiring；运行时不发现或动态加载插件。
- **构建确定且可追溯。** 顺序、规范化输入、锁定源码、镜像身份和产物校验必须保持稳定。
- **镜像不可变。** 激活、回滚、恢复和垃圾回收必须保持镜像完整性。

如果一项功能需要新的 Builder 行为，应将其表达为所有插件都可使用的通用 Manifest 规则、类型 Contract 或机制。不得为最先提出需求的插件添加特殊分支。

## 仓库归属与前置要求

| 变更 | 仓库 |
| --- | --- |
| CLI、Builder、图解析、文件格式、Image、Runtime/Process 管理及内置 Profile 选择 | [ingot](https://github.com/ingot-agent/ingot)（本仓库） |
| 官方插件实现、配置、HTTP/UI 行为、前端资产与插件设计记录 | [plugins](https://github.com/ingot-agent/plugins) |
| 可替换的 Agent/领域 Capability Contract | [sdk](https://github.com/ingot-agent/sdk) |
| 固定的 Component 构造函数与宿主 ABI Contract | [ingot-abi](https://github.com/ingot-agent/ingot-abi) |

使用 [`go.mod`](../go.mod) 声明的 Go 1.24.2 或更高版本、Git，以及运行 race 测试所需的
C 编译器。构建 Core 不需要 Node 或 plugins 仓库 checkout。已发布 Profile 的 smoke
测试需要通过网络/模块缓存获取 `internal/profiles/` 固定的精确插件版本。

```sh
git clone https://github.com/ingot-agent/ingot.git
cd ingot
GOWORK=off go build -o ingot ./cmd/ingot
./ingot --help
```

变更规范见 [AGENT.md](../AGENT.md)，当前参考见[文档索引](./README.md)。历史设计记录
用于解释决策，可能包含已被替代或尚未实现的行为；实现提案前必须对照代码与测试核实契约。

实现边界见[当前架构与源码导航（英文）](./ARCHITECTURE.md)。维护者发布前应遵循
[RELEASE.md（英文）](../RELEASE.md)，核验依赖版本、发布检查与分发流程。影响持久化
状态的变更还必须更新[升级、备份与恢复说明（英文）](./UPGRADING.md)。

## 开发流程

### 1. 创建分支

禁止直接向 `main` 提交或推送。切换分支前检查 `git status` 并保留已有未提交工作，
然后从最新的 `main` 创建一个目标明确的分支：

```sh
git switch main
git pull --ff-only
git switch -c feat/short-description
```

常用前缀包括 `feat/`、`fix/`、`docs/`、`test/` 和 `refactor/`。

### 2. 完成一项聚焦的变更

- 一个 Pull Request 只处理一个连贯目标。
- 遵循已有 Package 边界和局部代码风格。
- 为变化的行为新增或更新测试，包括失败场景。
- 同步更新受影响的用户文档、示例和设计文档。
- 不要提交密钥、本地配置、IDE 元数据、构建产物或无关的格式化变更。

使用 `gofmt` 格式化每个修改过的 Go 文件：

```sh
gofmt -w path/to/changed_file.go
```

### 3. 执行完整测试

开发过程中可以运行定向测试。每次提交前，按照 [AGENT.md](../AGENT.md) 对仓库内每个
Go Module 执行完整的 race 测试，禁用 workspace 解析。当前仓库只有 Core Module；CI
为其执行 `GOWORK=off go test -race ./...`，并另行执行已发布 Profile 的解析/构建 smoke
任务。覆盖全部 Module 的写法如下：

```bash
while IFS= read -r -d '' mod_file; do
  module_dir="$(dirname "$mod_file")"
  (
    cd "$module_dir"
    GOWORK=off go test -race ./...
  ) || exit 1
done < <(find . -type f -name go.mod -not -path '*/vendor/*' -print0 | sort -z)
git diff --check
```

对于当前单 Module checkout，PowerShell 用户可执行：

```powershell
$env:GOWORK = 'off'
go test -race ./...
if ($LASTEXITCODE -ne 0) { throw 'Core tests failed' }
git diff --check
```

race detector 要求受支持的平台和 C 编译器。请使用兼容的编译器/CGO 配置，或在 Linux CI
环境验证；不带 `-race` 的成功结果不能替代此项要求。修改 Profile 时还必须通过
[`.github/workflows/test.yml`](../.github/workflows/test.yml) 的 released-profile smoke 任务。

如果完整测试无法执行或未能通过，必须清楚记录阻塞原因，不得把变更描述为已经可以合并。

### 4. 清晰地提交

Commit Subject 应简洁、使用祈使语气，并遵循仓库已有风格：

```text
feat(builder): support a general component rule
fix(cli): preserve runtime command arguments
docs: clarify plugin composition
```

保持 Commit 易于审查，不要混入无关变更。未经其他贡献者协调，不要重写共享分支的历史。

### 5. 创建 Pull Request

以 `main` 为目标分支创建 Pull Request，并说明：

- 修改了什么以及为什么；
- 相关 Issue 链接；
- 已执行的测试与验证；
- 用户可见影响或兼容性影响；
- 已知限制或后续工作。

所有必要的 CI 检查都必须通过。Review 开始后，尽量通过追加 Commit 响应意见；除非已经和 Reviewer 协调，否则不要 Force Push。

## 代码与测试要求

- 编写符合 Go 习惯的代码，并准确记录所有导出标识符。
- 在阻塞调用中保持 `context.Context` 的取消和 Deadline 语义。
- 包装错误时，确保调用者仍可使用 `errors.Is` 和 `errors.As`。
- 保持 Component 构造和清理的确定性；Cleanup 按创建顺序的逆序执行。
- 使用正例和负例测试保护严格解析与校验行为。
- 优先测试公开行为。核心插件机制测试应使用任意或合成身份，不应依赖官方插件。
- 当行为依赖顺序或并发时，应显式断言并运行 race detector。

## 贡献插件

官方插件位于独立的 [`ingot-agent/plugins`](https://github.com/ingot-agent/plugins) 仓库。每个插件都是独立 Go Module，拥有自己的 `go.mod`、`ingot.plugin.toml`、Component 实现和测试，并且必须遵循与所有第三方插件相同的 Manifest 与 Component 规则。

从[插件开发文档](https://github.com/ingot-agent/plugins/tree/main/docs)开始。插件配置、
State schema、Operation 和前端说明应与插件实现一起维护。Component 构造函数为
`New(context.Context, Dependencies) (Exports, ingotabi.Cleanup, error)`；配置由插件通过
自己的 Runtime state scope 管理，不通过 Builder 所有的 `Config` 参数注入。

把插件加入官方 Profile 是一项独立的产品决策。Profile 可以选择插件，但不能赋予它不同的 Builder 或运行时行为。

## 文档

确保命令与示例可执行，并与当前行为一致。修改同时存在中英文版本的文档时，应同步更新两版；如果只修改其中一版，Pull Request 必须明确说明原因。

Core 工作流和文件格式参考留在本仓库。插件用户/开发指南放到 plugins 仓库，并从 Core
链接过去；SDK 与 ABI 契约文档留在各自仓库。迁移记录时保留历史状态，加入目标索引，更新
全部入链和出链，并指出当前使用参考。不要移除历史上下文，使提案看起来像已经支持的保证。

提交文档前，对照当前 `--help` 和命令处理器核验每条命令，对照 parser/测试核验 schema，
检查本地链接，并执行 `git diff --check`。使用占位凭据，明确标注示例版本。不要发布个人
路径、保存的模型密钥、Runtime state 或本地测试产物。

## 许可证

提交贡献即表示你同意该贡献可按照仓库的 [MIT License](../LICENSE) 分发。
